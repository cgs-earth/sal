// Package importation implements `sal import` and owns the project ontology
// file it maintains, .sal/ontology.jsonld. `import` is a Go keyword, so the
// package cannot be named after its subcommand, the same reason `init` lives in
// initialization.
package importation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/cgs-earth/sal/pkg"
	"github.com/cgs-earth/sal/salmodule"
	rdflibgo "github.com/tggo/goRDFlib"
	"github.com/tggo/goRDFlib/jsonld"
)

const (
	owlNamespace = "http://www.w3.org/2002/07/owl#"
	dcNamespace  = "http://purl.org/dc/elements/1.1/"
)

var (
	owlOntology = rdflibgo.NewURIRefUnsafe(owlNamespace + "Ontology")
	owlImports  = rdflibgo.NewURIRefUnsafe(owlNamespace + "imports")
	dcTitle     = rdflibgo.NewURIRefUnsafe(dcNamespace + "title")
)

// ImportCmd records what a project imports in its .sal/ontology.jsonld. An http or
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
	path, err := pkg.SalOntologyPath()
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

// Ontology is the parsed contents of a project's .sal/ontology.jsonld.
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
	// Graph is every statement the file makes.
	Graph *rdflibgo.Graph
}

// ReadOntology parses the project ontology that `sal import` maintains. It
// returns nil when the file does not exist, since a project is not required to
// declare one.
func ReadOntology(path string, base string) (*Ontology, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	graph := rdflibgo.NewGraph(rdflibgo.WithBase(base))
	if err := jsonld.Parse(graph, bytes.NewReader(content), jsonld.WithBase(base)); err != nil {
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

	return ontology, nil
}

func writeOntology(path string, ontology *Ontology) error {
	content, err := serializeOntology(ontology)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

// serializeOntology renders the ontology in the shape `sal import` maintains. A
// file a user has added other statements to is serialized from its own graph
// instead, so that hand written triples survive an import at the cost of the
// generated file's formatting. The file carries no autogenerated banner the way
// the Turtle it used to be written as did, since JSON has no comment syntax to
// carry one in; a hand edit still only survives when it is a statement, exactly
// as before.
func serializeOntology(ontology *Ontology) ([]byte, error) {
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

	doc := ontologyDocument{
		Context: map[string]string{"dc": dcNamespace, "owl": owlNamespace},
		ID:      id,
		Type:    "owl:Ontology",
		Title:   ontology.Title,
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
