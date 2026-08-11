package validate

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/cgs-earth/sal/build/vocab"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
	"github.com/tggo/goRDFlib/rdfxml"
	"github.com/tggo/goRDFlib/turtle"
)

// vocabularyCache holds the term sets a run has resolved. The documents those
// terms come from are resolved through the project's pins rather than fetched
// here, so a run validates against the versions the project recorded.
type vocabularyCache struct {
	pins         *PinnedVocabularies
	cache        map[string]Vocabulary
	failures     map[string]error
	replacements map[string]string
	base         string
}

func longestPrefixBase(iri string, ctx RdfContext) (string, string, bool) {
	bestPrefix := ""
	bestBase := ""
	if ctx.Vocab != "" && strings.HasPrefix(iri, ctx.Vocab) {
		bestBase = ctx.Vocab
	}
	for prefix, base := range ctx.Prefixes {
		if strings.HasPrefix(iri, base) && len(base) >= len(bestBase) {
			bestPrefix = prefix
			bestBase = base
		}
	}
	return bestPrefix, bestBase, bestBase != ""
}

func (c *vocabularyCache) isDefined(iri string, ctx RdfContext) (bool, error) {
	if c.base != "" && strings.HasPrefix(iri, c.base) {
		return true, nil
	}
	if iriWithoutXsdNamepace, found := strings.CutPrefix(iri, xsdNamespaceIRI); found {
		return slices.Contains(xsdBuiltinDatatypeLocalNames, iriWithoutXsdNamepace), nil
	}

	_, base, ok := longestPrefixBase(iri, ctx)
	if !ok {
		return true, nil
	}
	lookupIRI := replacementVocabularyTerm(iri, base, c.replacements)
	base = replacementVocabularyBase(base, c.replacements)
	vocab, err := c.load(base)
	if err != nil {
		return false, err
	}
	return vocab.terms[lookupIRI], nil
}

// resolvable reports whether a declared prefix names a vocabulary that has to
// be dereferenced. Terms in the project's own base and the XSD built-in
// datatypes are checked without a vocabulary document, so there is no version
// of them to pin.
func (c *vocabularyCache) resolvable(namespace string) bool {
	if c.base != "" && strings.HasPrefix(namespace, c.base) {
		return false
	}
	return namespace != xsdNamespaceIRI
}

func replacementVocabularyBase(base string, replacements map[string]string) string {
	if replacements == nil {
		return base
	}
	if replacement, ok := replacements[base]; ok {
		return replacement
	}
	return base
}

func replacementVocabularyTerm(iri, base string, replacements map[string]string) string {
	if replacements == nil {
		return iri
	}
	replacement, ok := replacements[base]
	if !ok {
		return iri
	}
	return replacement + strings.TrimPrefix(iri, base)
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

// load resolves the vocabulary a prefix namespace names. The namespace rather
// than the document URL is the key, since that is what a project pins a version
// against; two namespaces served by one document are only fetched once.
func (c *vocabularyCache) load(namespace string) (Vocabulary, error) {
	if vocab, ok := c.cache[namespace]; ok {
		return vocab, nil
	}
	if err, ok := c.failures[namespace]; ok {
		return Vocabulary{}, err
	}

	terms, err := c.loadTerms(namespace)
	if err != nil {
		c.failures[namespace] = err
		return Vocabulary{}, err
	}
	vocab := Vocabulary{terms: terms}
	c.cache[namespace] = vocab
	return vocab, nil
}

// parseBase returns the base that a fetched vocabulary's relative terms resolve
// against. A SAL module ontology declares its terms relative to the module
// itself rather than to the SAL project being built.
func (c *vocabularyCache) parseBase(base string) string {
	if salmodule.IsModuleIRI(base) {
		return base
	}
	return c.base
}

// loadTerms reads the terms a vocabulary defines out of the version the project
// pins for it, pinning what it fetched when there is no pin yet. A document SAL
// cannot parse falls back to the bundled copy in build/vocab, which is then
// pinned in its place; a pinned document that no longer parses is an error,
// since silently validating against something else would defeat the pin.
func (c *vocabularyCache) loadTerms(namespace string) (map[string]bool, error) {
	body, mediaType, pinned, err := c.pins.Document(namespace, vocabularyDocumentURL(namespace))
	if err == nil {
		terms, _, parseErr := serializeRdfDataAndGetVocab(mediaType, body, c.parseBase(namespace))
		if parseErr == nil {
			return terms, nil
		}
		if pinned {
			return nil, parseErr
		}
		// what was just fetched is not RDF SAL can read, so it is not the
		// version this project should record
		c.pins.Unpin(namespace)
		err = parseErr
	}

	bundled, bundledType, ok, bundledErr := vocab.Load(namespace)
	if bundledErr != nil || !ok {
		return nil, err
	}
	terms, _, parseErr := serializeRdfDataAndGetVocab(bundledType, bundled, c.parseBase(namespace))
	if parseErr != nil {
		return nil, err
	}
	slog.Warn("Could not read the vocabulary at " + namespace + ", so SAL's bundled copy is pinned instead: " + err.Error())
	c.pins.Pin(namespace, bundled, bundledType, PinnedVersion{})
	return terms, nil
}

type rdfFormat string

const (
	rdfXMLFormat rdfFormat = "application/rdf+xml"
	turtleFormat rdfFormat = "text/turtle"
	jsonLDFormat rdfFormat = "application/ld+json"
)

// serializeRdfDataAndGetVocab parses a vocabulary document and returns every
// URI-backed term in the resulting graph.
func serializeRdfDataAndGetVocab(contentType string, body []byte, base string) (map[string]bool, *rdflibgo.Graph, error) {
	parsersToTry := []rdfFormat{}

	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch {
	case mediaType == "application/ld+json" || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		parsersToTry = append(parsersToTry, jsonLDFormat)
	case mediaType == "text/turtle" || mediaType == "application/n-triples" || mediaType == "application/n-quads":
		parsersToTry = append(parsersToTry, turtleFormat)
	case mediaType == "application/rdf+xml" || strings.HasSuffix(mediaType, "+xml") || strings.Contains(mediaType, "xml"):
		parsersToTry = append(parsersToTry, rdfXMLFormat)
	case looksLikeJSON(body):
		parsersToTry = append(parsersToTry, jsonLDFormat)
	case looksLikeTurtle(body):
		parsersToTry = append(parsersToTry, turtleFormat)
	default:
		parsersToTry = append(parsersToTry, rdfXMLFormat, jsonLDFormat, turtleFormat)
	}

	var errs []string
	for _, parser := range parsersToTry {
		graph, err := parseVocabularyRDF(body, base, parser)
		if err == nil {
			return extractVocabularyTermsFromGraph(graph), graph, nil
		}
		errs = append(errs, fmt.Errorf("failed to parse as %s: %w", parser, err).Error())
	}
	return nil, nil, fmt.Errorf("unsupported vocabulary serialization (%s): %s", contentType, strings.Join(errs, "; "))
}

func parseVocabularyRDF(body []byte, base string, format rdfFormat) (*rdflibgo.Graph, error) {
	g := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	switch format {
	case rdfXMLFormat:
		if err := rdfxml.Parse(g, bytes.NewReader(body), rdfxml.WithBase(base)); err != nil {
			return nil, err
		}
	case turtleFormat:
		if err := turtle.Parse(g, bytes.NewReader(body), turtle.WithBase(base)); err != nil {
			return nil, err
		}
	case jsonLDFormat:
		if err := jsonld.Parse(g, bytes.NewReader(body), jsonld.WithBase(base), jsonld.WithUnboundedLines()); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown RDF format: %s", format)
	}
	return g, nil
}

// extractVocabularyTermsFromGraph collects URI terms that a vocabulary defines
// after it has been parsed into an RDF graph.
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

// PinnedGraph parses the version a project pins of an ontology document into a
// graph, dereferencing and pinning it when the project has no pin for it yet.
// It is how build merges the ontologies a project's .sal/ontology.jsonld imports,
// so that an import is carried at the exact version the project recorded. Terms
// in the document that are written relative resolve against the document's own
// IRI, not against the SAL project being built.
func PinnedGraph(pins *PinnedVocabularies, iri string) (*rdflibgo.Graph, error) {
	body, contentType, _, err := pins.Document(iri, iri)
	if err != nil {
		return nil, err
	}
	base := iri
	// a module names itself without the trailing slash its vocabulary base
	// carries, so an import written that way still has to parse against the base
	if salmodule.IsModuleIRI(iri) {
		ref, err := salmodule.ParseModuleIRI(iri)
		if err != nil {
			return nil, err
		}
		base = ref.Namespace
	}
	_, graph, err := serializeRdfDataAndGetVocab(contentType, body, base)
	if err != nil {
		return nil, err
	}
	return graph, nil
}

func fetchVocabularyDocument(u string) ([]byte, string, PinnedVersion, error) {
	// a salmodule:// vocabulary is not served over HTTP; it is obtained by
	// building the module's container and asking it for its ontology, and it is
	// pinned by the git commit hash of the module repository rather than by the
	// digest of the ontology document, since code the module runs can change
	// without the ontology itself changing
	if salmodule.IsModuleIRI(u) {
		document, mediaType, commitHash, err := salmodule.FetchOntologyDocument(u)
		if err != nil {
			return nil, "", PinnedVersion{}, err
		}
		return document, mediaType, PinnedVersion{Scheme: gitCommitVersionScheme, Value: commitHash}, nil
	}

	req, err := http.NewRequest(http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, "", PinnedVersion{}, err
	}
	req.Header.Set("Accept", "application/ld+json, application/json;q=0.9, text/turtle;q=0.8, application/rdf+xml;q=0.7, text/plain;q=0.6, */*;q=0.1")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", PinnedVersion{}, err
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", PinnedVersion{}, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", PinnedVersion{}, fmt.Errorf("bad response status code: %d", res.StatusCode)
	}
	return body, res.Header.Get("Content-Type"), PinnedVersion{}, nil
}

func looksLikeJSON(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func looksLikeTurtle(body []byte) bool {
	s := strings.TrimSpace(string(body))
	return strings.HasPrefix(s, "@prefix") || strings.HasPrefix(s, "PREFIX") || strings.HasPrefix(s, "@base") || strings.HasPrefix(s, "BASE ")
}

func looksLikeVocabularyBase(value string) bool {
	return strings.HasSuffix(value, "/") || strings.HasSuffix(value, "#")
}
