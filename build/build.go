package build

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/cgs-earth/json-gold/ld"
	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

type BuildCmd struct {
	Paths      []string          `arg:"positional" help:"RDF files to validate"`
	PrefixMaps []string          `arg:"--prefix-maps" help:"prefix mappings to apply as source target pairs or source=target entries"`
	Format     GraphExportFormat `arg:"--format" help:"output format: nq or iceberg" default:"iceberg"`
}

var findSALProjectDir = pkg.SALProjectDir

// Run validates RDF files for terms that are not defined by their vocabularies and returns their merged RDF graph.
func Run(cfg *BuildCmd) (*rdflibgo.Graph, error) {
	if cfg == nil {
		return nil, fmt.Errorf("build: missing arguments")
	}

	var paths []string
	if len(cfg.Paths) > 0 {
		paths = cfg.Paths
	} else {
		projectDir, err := findSALProjectDir(os.UserHomeDir)
		if err != nil {
			return nil, fmt.Errorf("build: find SAL project directory: %w", err)
		}
		paths = []string{projectDir}
	}

	base, err := pkg.DefaultSalBase()
	if err != nil {
		return nil, err
	}
	files, err := pkg.FindRdfDataInPaths(paths)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no JSON-LD or TTL files found in %s", strings.Join(paths, ", "))
	}

	finalGraph := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	var errs validate.MultiError
	for _, file := range files {
		// TODO do this in parallel and fill in the vocabularies to replace logic
		graph, err := validate.ValidateRDFFile(file, make(map[string]string), base)
		if err != nil {
			if nested, ok := err.(validate.MultiError); ok {
				errs = append(errs, nested...)
			} else {
				errs = append(errs, err)
			}
			continue
		}
		mergeGraph(finalGraph, graph)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if err := NewTermsHaveClassDefinitions(finalGraph); err != nil {
		return nil, err
	}

	slog.Info("Validated " + fmt.Sprint(len(files)) + " file(s)")

	if err := NewTermsHaveClassDefinitions(finalGraph); err != nil {
		return nil, err
	}

	if err := ExportGraph(finalGraph, cfg.Format); err != nil {
		return nil, err
	}

	return finalGraph, err
}

func collectContext(doc any, loader ld.DocumentLoader) (validate.RdfContext, error) {
	ctx := validate.RdfContext{prefixes: map[string]string{}}
	if err := collectContextFromNode(doc, &ctx, loader, map[string]bool{}); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func collectContextFromNode(node any, ctx *validate.RdfContext, loader ld.DocumentLoader, seen map[string]bool) error {
	switch n := node.(type) {
	case map[string]any:
		if value, ok := n["@context"]; ok {
			if err := readContext(value, ctx, loader, seen); err != nil {
				return err
			}
		}
		for key, value := range n {
			if key != "@context" {
				if err := collectContextFromNode(value, ctx, loader, seen); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, value := range n {
			if err := collectContextFromNode(value, ctx, loader, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func readContext(value any, ctx *RdfContext, loader ld.DocumentLoader, seen map[string]bool) error {
	switch c := value.(type) {
	case string:
		if seen[c] {
			return nil
		}
		seen[c] = true
		doc, err := loader.LoadDocument(c)
		if err != nil {
			return err
		}
		if remoteCtx, ok := documentContext(doc.Document); ok {
			return readContext(remoteCtx, ctx, loader, seen)
		}
		return readContext(doc.Document, ctx, loader, seen)
	case []any:
		for _, item := range c {
			if err := readContext(item, ctx, loader, seen); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range c {
			switch {
			case key == "@vocab":
				if vocab, ok := item.(string); ok {
					ctx.vocab = vocab
				}
			case strings.HasPrefix(key, "@"):
				continue
			case !strings.Contains(key, ":"):
				if base, ok := contextTermBase(item); ok {
					ctx.prefixes[key] = base
				}
			}
		}
	}
	return nil
}

func documentContext(doc any) (any, bool) {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, false
	}
	ctx, ok := m["@context"]
	return ctx, ok
}

func contextTermBase(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		if looksLikeVocabularyBase(v) {
			return v, true
		}
	case map[string]any:
		id, ok := v["@id"].(string)
		if ok && looksLikeVocabularyBase(id) {
			return id, true
		}
	}
	return "", false
}

func collectJSONLDTerms(content []byte, loader ld.DocumentLoader) ([]usedTerm, error) {
	provenance := map[string]int{}
	processor := ld.NewJsonLdProcessor()
	options := ld.NewJsonLdOptions("")
	options.DocumentLoader = loader

	addJSONLDProvenanceTerm := func(provenance map[string]int, node ld.Node, line int) {
		if line <= 0 || !ld.IsIRI(node) {
			return
		}
		iri := node.GetValue()
		if existing, ok := provenance[iri]; ok && existing <= line {
			return
		}
		provenance[iri] = line
	}

	options.RDFQuadProvenanceCallback = func(quad *ld.Quad, prov ld.RDFQuadProvenance) {
		addJSONLDProvenanceTerm(provenance, quad.Subject, prov.SubjectLine)
		addJSONLDProvenanceTerm(provenance, quad.Predicate, prov.PredicateLine)
		addJSONLDProvenanceTerm(provenance, quad.Object, prov.ObjectLine)
		if quad.Graph != nil {
			addJSONLDProvenanceTerm(provenance, quad.Graph, prov.GraphLine)
		}
	}

	if _, err := processor.ToRDF(bytes.NewReader(content), options); err != nil {
		return nil, err
	}

	terms := make([]usedTerm, 0, len(provenance))
	for iri, line := range provenance {
		terms = append(terms, usedTerm{iri: iri, line: line})
	}
	sort.Slice(terms, func(i, j int) bool {
		if terms[i].line != terms[j].line {
			return terms[i].line < terms[j].line
		}
		return terms[i].iri < terms[j].iri
	})
	return terms, nil
}

func vocabularyDocumentURL(base string) string {
	if before, _, ok := strings.Cut(base, "#"); ok {
		return before
	}
	if strings.Contains(base, "opengis.net") && strings.HasSuffix(base, "/") {
		return strings.TrimSuffix(base, "/")
	}
	return base
}

// serializeRdfDataAndGetVocabWithLoader parses RDF with an optional JSON-LD document loader.
func serializeRdfDataAndGetVocab(contentType string, body []byte, base string) (map[string]bool, *rdflibgo.Graph, error) {
	parsersToTry := []RDFFormat{}

	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch {
	case mediaType == "application/ld+json" || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		parsersToTry = append(parsersToTry, JSONLD)
	case mediaType == "text/turtle" || mediaType == "application/n-triples" || mediaType == "application/n-quads":
		parsersToTry = append(parsersToTry, TURTLE)
	case mediaType == "application/rdf+xml" || strings.HasSuffix(mediaType, "+xml") || strings.Contains(mediaType, "xml"):
		parsersToTry = append(parsersToTry, RDFXML)
	// if it looks like a specific RDF format,
	// you default to parsing that option first,
	// but also fall back to the other options if needed
	// (i.e. if the input has no content type)
	case looksLikeJSON(body):
		parsersToTry = append(parsersToTry, JSONLD)
	case looksLikeTurtle(body):
		parsersToTry = append(parsersToTry, TURTLE)
	default:
		parsersToTry = append(parsersToTry, RDFXML)
	}

	var errs []string
	for _, parser := range parsersToTry {
		graph, err := parseRdf(body, base, parser)
		if err == nil {
			terms := extractVocabularyTermsFromGraph(graph)
			return terms, graph, nil
		}
		errs = append(errs, fmt.Errorf("failed to parse as %s: %w", parser, err).Error())
	}
	return nil, nil, fmt.Errorf("unsupported vocabulary serialization (%s): %s", contentType, strings.Join(errs, "; "))
}

// extractVocabularyTermsFromGraph collects every URI-backed vocabulary term after RDF parsing has completed.
func extractVocabularyTermsFromGraph(g *rdflibgo.Graph) map[string]bool {
	terms := map[string]bool{}
	g.Namespaces()(func(_ string, ns rdflibgo.URIRef) bool {
		if ns.Value() != "" {
			terms[ns.Value()] = true
		}
		return true
	})
	g.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if subj, ok := triple.Subject.(rdflibgo.URIRef); ok {
			terms[subj.Value()] = true
		}
		terms[triple.Predicate.Value()] = true
		if obj, ok := triple.Object.(rdflibgo.URIRef); ok {
			terms[obj.Value()] = true
		}
		if lit, ok := triple.Object.(rdflibgo.Literal); ok {
			terms[lit.Datatype().Value()] = true
		}
		return true
	})
	return terms
}

func fetchVocabularyDocument(u string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/ld+json, application/json;q=0.9, text/turtle;q=0.8, application/rdf+xml;q=0.7, text/plain;q=0.6, */*;q=0.1")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, res.Header.Get("Content-Type"), err
	}
	if res.StatusCode != http.StatusOK {
		return nil, res.Header.Get("Content-Type"), fmt.Errorf("bad response status code: %d", res.StatusCode)
	}
	return body, res.Header.Get("Content-Type"), nil
}

func looksLikeTurtle(body []byte) bool {
	s := strings.TrimSpace(string(body))
	return strings.HasPrefix(s, "@prefix") || strings.HasPrefix(s, "PREFIX") || strings.HasPrefix(s, "@base") || strings.HasPrefix(s, "BASE ")
}

func looksLikeVocabularyBase(value string) bool {
	return strings.HasSuffix(value, "/") || strings.HasSuffix(value, "#")
}
