// Package importation implements `sal import` and owns the project ontology
// node it maintains in .sal/config.jsonld, the graph node with no
// owl:versionIRI. `import` is a Go keyword, so the package cannot be named
// after its subcommand, the same reason `init` lives in initialization.
package importation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
)

const (
	owlNamespace  = "http://www.w3.org/2002/07/owl#"
	dcNamespace   = "http://purl.org/dc/elements/1.1/"
	rdfsNamespace = "http://www.w3.org/2000/01/rdf-schema#"
)

var (
	owlOntology = rdflibgo.NewURIRefUnsafe(owlNamespace + "Ontology")
	owlImports  = rdflibgo.NewURIRefUnsafe(owlNamespace + "imports")
	dcTitle     = rdflibgo.NewURIRefUnsafe(dcNamespace + "title")
	rdfsComment = rdflibgo.NewURIRefUnsafe(rdfsNamespace + "comment")
)

// ImportCmd records what a project imports in its .sal/config.jsonld. An http or
// https URL is an ontology document that `sal build` merges into the data
// product, as is a salmodule:// reference, whose document is obtained by
// building the module rather than over HTTP; an oci:// reference is an artifact
// that is pulled to disk instead.
type ImportCmd struct {
	References []string `arg:"positional,required" help:"URLs of the ontologies to import, salmodule:// references to the SAL modules whose ontologies to import, or oci:// references to the OCI artifacts to import"`
	Title      string   `arg:"--title" help:"Title recorded for this project's own ontology; defaults to the git project name"`
	Username   string   `arg:"--username,env:OCI_USERNAME" help:"Username for the OCI registry"`
	Password   string   `arg:"--password,env:OCI_PASSWORD" help:"Password for the OCI registry"`
}

func (cmd *ImportCmd) Credentials() ArtifactCredentials {
	return ArtifactCredentials{Username: cmd.Username, Password: cmd.Password}
}

func (cmd *ImportCmd) Run() error {
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	path, err := pkg.SalConfigPath()
	if err != nil {
		return err
	}
	importsDir, err := pkg.SalImportsDir()
	if err != nil {
		return err
	}

	ontology, err := ReadOntology(path, base)
	if err != nil {
		return err
	}
	if ontology == nil {
		title := cmd.Title
		if title == "" {
			if title, err = pkg.GitProjectName(); err != nil {
				return err
			}
		}
		ontology = &Ontology{IRI: base, Base: base, Title: title}
	} else if cmd.Title != "" {
		ontology.Title = cmd.Title
	}

	projectName, err := pkg.GitProjectName()
	if err != nil {
		return err
	}
	ontology.Comment = fmt.Sprintf(
		"Represents the ontology for the %s project overall and any vocabularies that are directly materialized into the graph.",
		projectName,
	)

	for _, reference := range cmd.References {
		imported, err := importIRI(reference)
		if err != nil {
			return err
		}
		// an artifact is pulled even when it is already recorded, so that an
		// import missing from disk is restored rather than silently skipped
		if IsOciImport(imported) {
			if err := supersedeArtifactImport(ontology, imported, importsDir); err != nil {
				return fmt.Errorf("import %s: %w", imported, err)
			}
			if err := PullArtifact(context.Background(), importsDir, imported, cmd.Credentials()); err != nil {
				return fmt.Errorf("import %s: %w", imported, err)
			}
		}
		if slices.Contains(ontology.Imports, imported) {
			slog.Warn(imported + " is already imported by " + path)
			continue
		}
		ontology.Imports = append(ontology.Imports, imported)
	}

	if err := writeOntology(path, ontology); err != nil {
		return err
	}
	slog.Info("Wrote " + path + "; run `sal build` to merge the imported ontologies into the data product")
	return nil
}

// Ontology is the parsed contents of a project's ontology node in
// .sal/config.jsonld.
type Ontology struct {
	// IRI is the subject the file declares as an owl:Ontology, which is the
	// project base unless the file names something else.
	IRI string
	// Base is the project base that relative IRIs in the file resolve against.
	Base  string
	Title string
	// Imports are the IRIs the ontology lists with owl:imports, sorted so that
	// a rewrite of the file is stable.
	Imports []string
	// Comment describes the ontology node for a human reading the graph; `sal
	// import` always sets it to the same generated text.
	Comment string
	// Graph is every statement the ontology's own node makes, isolated from
	// the pinned vocabulary nodes the rest of .sal/config.jsonld's @graph
	// carries.
	Graph *rdflibgo.Graph
}

// ReadOntology parses the project ontology node that `sal import` maintains
// out of .sal/config.jsonld. It returns nil when the file does not exist, or
// exists but declares no ontology node yet, since a project is not required
// to declare one.
func ReadOntology(path string, base string) (*Ontology, error) {
	doc, err := pkg.ReadConfigDocument(path)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON-LD: %w", path, err)
	}
	node, _, err := pkg.PartitionConfigGraph(doc.Graph)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if node == nil {
		return nil, nil
	}

	standalone, err := pkg.JoinJSONLDDocument(doc.Context, node)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	if err := jsonld.Parse(graph, bytes.NewReader(standalone), jsonld.WithBase(base)); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON-LD: %w", path, err)
	}

	ontology := &Ontology{IRI: base, Base: base, Graph: graph}
	rdfType := rdflibgo.RDF.Type
	graph.Triples(nil, &rdfType, owlOntology)(func(triple rdflibgo.Triple) bool {
		subject, ok := triple.Subject.(rdflibgo.URIRef)
		if !ok {
			return true
		}
		ontology.IRI = subject.Value()
		return false
	})

	subject := rdflibgo.NewURIRefUnsafe(ontology.IRI)
	graph.Triples(subject, &dcTitle, nil)(func(triple rdflibgo.Triple) bool {
		literal, ok := triple.Object.(rdflibgo.Literal)
		if !ok {
			return true
		}
		ontology.Title = literal.Lexical()
		return false
	})
	graph.Triples(subject, &owlImports, nil)(func(triple rdflibgo.Triple) bool {
		if object, ok := triple.Object.(rdflibgo.URIRef); ok {
			ontology.Imports = append(ontology.Imports, object.Value())
		}
		return true
	})
	sort.Strings(ontology.Imports)
	graph.Triples(subject, &rdfsComment, nil)(func(triple rdflibgo.Triple) bool {
		literal, ok := triple.Object.(rdflibgo.Literal)
		if !ok {
			return true
		}
		ontology.Comment = literal.Lexical()
		return false
	})

	return ontology, nil
}

// writeOntology writes the ontology node into .sal/config.jsonld, preserving
// whatever pinned vocabulary nodes are already there byte for byte, since
// `sal import` never inspects them.
func writeOntology(path string, ontology *Ontology) error {
	standalone, err := SerializeOntology(ontology)
	if err != nil {
		return err
	}
	_, node, err := pkg.SplitJSONLDDocument(standalone)
	if err != nil {
		return err
	}

	doc, err := pkg.ReadConfigDocument(path)
	if err != nil {
		return err
	}
	_, pinned, err := pkg.PartitionConfigGraph(doc.Graph)
	if err != nil {
		return err
	}
	if doc.Graph, err = pkg.AssembleConfigGraph(node, pinned); err != nil {
		return err
	}
	doc.Context = pkg.SalConfigContext

	return pkg.WriteConfigDocument(path, doc)
}

// SerializeOntology renders the ontology node as a standalone JSON-LD
// document, with its own @context, in the shape `sal import` maintains. A
// node a user has added other statements to is serialized from its own graph
// instead, so that hand written triples survive an import at the cost of the
// generated file's formatting. This is also what build validates the
// project's own ontology statements against and folds into its source hash,
// since the node has no file of its own to do either against directly.
func SerializeOntology(ontology *Ontology) ([]byte, error) {
	if ontology.Graph == nil || !ontology.hasExtraStatements() {
		return renderOntology(ontology)
	}

	graph := ontology.Graph
	graph.Bind("dc", rdflibgo.NewURIRefUnsafe(dcNamespace))
	graph.Bind("owl", rdflibgo.NewURIRefUnsafe(owlNamespace))
	subject := rdflibgo.NewURIRefUnsafe(ontology.IRI)
	graph.Add(subject, rdflibgo.RDF.Type, owlOntology)
	if ontology.Title != "" {
		// Set rather than Add so that --title replaces the recorded title
		graph.Set(subject, dcTitle, rdflibgo.NewLiteral(ontology.Title))
	}
	for _, iri := range ontology.Imports {
		graph.Add(subject, owlImports, rdflibgo.NewURIRefUnsafe(iri))
	}
	if ontology.Comment != "" {
		graph.Bind("rdfs", rdflibgo.NewURIRefUnsafe(rdfsNamespace))
		graph.Set(subject, rdfsComment, rdflibgo.NewLiteral(ontology.Comment))
	}

	var buf bytes.Buffer
	if err := jsonld.Serialize(graph, &buf, jsonld.WithBase(ontology.Base)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hasExtraStatements reports whether the file says anything beyond the ontology
// header the generated template covers.
func (ontology *Ontology) hasExtraStatements() bool {
	subject := rdflibgo.NewURIRefUnsafe(ontology.IRI)
	extra := false
	ontology.Graph.Triples(nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if !triple.Subject.Equal(subject) {
			extra = true
			return false
		}
		switch {
		case triple.Predicate.Equal(rdflibgo.RDF.Type) && triple.Object.Equal(owlOntology):
		case triple.Predicate.Equal(dcTitle):
		case triple.Predicate.Equal(owlImports):
		case triple.Predicate.Equal(rdfsComment):
		default:
			extra = true
			return false
		}
		return true
	})
	return extra
}

// ontologyDocument is the JSON-LD shape renderOntology writes. The ontology is
// written as the relative IRI "." when it is the project base, which is how a
// SAL project names itself, resolved the same way `<.>` was in the Turtle this
// file used to be written as.
type ontologyDocument struct {
	Context map[string]string   `json:"@context"`
	ID      string              `json:"@id"`
	Type    string              `json:"@type"`
	Title   string              `json:"dc:title,omitempty"`
	Imports []ontologyImportRef `json:"owl:imports,omitempty"`
	Comment string              `json:"rdfs:comment,omitempty"`
}

type ontologyImportRef struct {
	ID string `json:"@id"`
}

// renderOntology writes the ontology header as the flat JSON-LD document `sal
// import` generates.
func renderOntology(ontology *Ontology) ([]byte, error) {
	id := ontology.IRI
	if ontology.IRI == ontology.Base {
		id = "."
	}

	context := map[string]string{"dc": dcNamespace, "owl": owlNamespace}
	if ontology.Comment != "" {
		context["rdfs"] = rdfsNamespace
	}
	doc := ontologyDocument{
		Context: context,
		ID:      id,
		Type:    "owl:Ontology",
		Title:   ontology.Title,
		Comment: ontology.Comment,
	}
	for _, iri := range ontology.Imports {
		doc.Imports = append(doc.Imports, ontologyImportRef{ID: iri})
	}

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// importIRI resolves what an import was given as into the IRI recorded with
// owl:imports. An http or https URL is an ontology document that build merges,
// as is a salmodule:// reference; an oci:// reference is an artifact that build
// pulls to disk instead.
func importIRI(reference string) (string, error) {
	value := strings.TrimSpace(reference)
	value = strings.TrimSuffix(strings.TrimPrefix(value, "<"), ">")
	if value == "" {
		return "", fmt.Errorf("import: empty ontology reference")
	}

	if IsOciImport(value) {
		if _, err := artifactName(value); err != nil {
			return "", fmt.Errorf("import: %w", err)
		}
		return value, nil
	}

	if salmodule.IsModuleIRI(value) {
		ref, err := salmodule.ParseModuleIRI(value)
		if err != nil {
			return "", fmt.Errorf("import: %w", err)
		}
		// the module is recorded under the IRI that names it rather than under
		// its vocabulary base, which is the same IRI with a trailing slash. A
		// project that binds a prefix to the base would otherwise have the
		// import statement itself checked against the module's own terms.
		return strings.TrimSuffix(ref.Namespace, "/"), nil
	}

	parsed, err := url.Parse(value)
	if err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return value, nil
	}
	if looksLikeArtifactReference(value) {
		return "", fmt.Errorf("import: %s looks like an OCI artifact reference; write it as %s%s", value, OciScheme, value)
	}
	return "", fmt.Errorf("import: %s is not an http or https URL, a %s:// reference, or an %s reference", value, salmodule.ProtocolScheme, OciScheme)
}

// looksLikeArtifactReference reports whether a reference with no scheme names an
// OCI artifact, so that a missing oci:// can be reported as the real problem. As
// with docker, a leading path component only counts as a registry when it
// carries a dot or a port, or is localhost.
func looksLikeArtifactReference(value string) bool {
	if _, err := pkg.ParseArtifact(value); err != nil {
		return false
	}
	host, repository, ok := strings.Cut(value, "/")
	if !ok || repository == "" || strings.HasPrefix(host, ".") {
		return false
	}
	return host == "localhost" || strings.ContainsAny(host, ".:")
}
