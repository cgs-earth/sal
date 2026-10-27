// Package importation implements `sal import` and owns the project node it
// maintains in .sal/config.jsonld, the graph node with no owl:versionIRI. The
// node is the project itself, "@id": ".": `sal init` types it schema:Project
// and records the description it asked for, and `sal import` types it
// owl:Ontology and records what the project imports. `import` is a Go keyword,
// so the package cannot be named after its subcommand, the same reason `init`
// lives in initialization.
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
	owlNamespace     = "http://www.w3.org/2002/07/owl#"
	dcNamespace      = "http://purl.org/dc/elements/1.1/"
	dctermsNamespace = "http://purl.org/dc/terms/"
	rdfsNamespace    = "http://www.w3.org/2000/01/rdf-schema#"
	schemaNamespace  = "https://schema.org/"
)

var (
	owlOntology = rdflibgo.NewURIRefUnsafe(owlNamespace + "Ontology")
	owlImports  = rdflibgo.NewURIRefUnsafe(owlNamespace + "imports")
	// dcTitle is the Dublin Core elements spelling the node carried before it
	// was written with dcterms:title; it is read and migrated, never written
	dcTitle            = rdflibgo.NewURIRefUnsafe(dcNamespace + "title")
	dctermsTitle       = rdflibgo.NewURIRefUnsafe(dctermsNamespace + "title")
	dctermsDescription = rdflibgo.NewURIRefUnsafe(dctermsNamespace + "description")
	rdfsComment        = rdflibgo.NewURIRefUnsafe(rdfsNamespace + "comment")
	schemaProject      = rdflibgo.NewURIRefUnsafe(schemaNamespace + "Project")
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
		ontology = &Ontology{IRI: base, Base: base}
	}
	// the node `sal init` wrote is a schema:Project with a description and
	// nothing else; the first import is what makes it an ontology
	ontology.IsOntology = true
	projectName, err := pkg.GitProjectName()
	if err != nil {
		return err
	}
	if cmd.Title != "" {
		ontology.Title = cmd.Title
	} else if ontology.Title == "" {
		ontology.Title = projectName
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

	if err := WriteOntology(path, ontology); err != nil {
		return err
	}
	slog.Info("Wrote " + path + "; run `sal build` to merge the imported ontologies into the data product")
	return nil
}

// Ontology is the parsed contents of a project's own node in
// .sal/config.jsonld, the one node of its @graph with no owl:versionIRI. The
// node names the project, and it is typed by what has been recorded on it:
// `sal init` makes it a schema:Project carrying the project's description,
// and `sal import` makes it an owl:Ontology carrying the title and imports.
type Ontology struct {
	// IRI is the subject the file declares as an owl:Ontology, which is the
	// project base unless the file names something else.
	IRI string
	// Base is the project base that relative IRIs in the file resolve against.
	Base string
	// IsProject reports whether the node is typed schema:Project, which `sal
	// init` makes it.
	IsProject bool
	// Description is the project's dcterms:description, what `sal init` asked
	// for or was given with --description.
	Description string
	// Title is the project's dcterms:title: the git project name, the last
	// segment of the remote URL, which `sal init` records and `sal import
	// --title` replaces.
	Title string
	// IsOntology reports whether the node is typed owl:Ontology, which `sal
	// import` makes it. A node `sal init` wrote and nothing has imported into
	// is a project and not yet an ontology.
	IsOntology bool
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

// ReadOntology parses the project node that `sal init` and `sal import`
// maintain out of .sal/config.jsonld. It returns nil when the file does not
// exist, or exists but holds no project node yet, since a project is not
// required to declare one.
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
	if err := jsonld.Parse(context.Background(), graph, bytes.NewReader(standalone), jsonld.WithBase(base)); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON-LD: %w", path, err)
	}

	ontology := &Ontology{IRI: base, Base: base, Graph: graph}
	rdfType := rdflibgo.RDF.Type
	pkg.Triples(graph, nil, &rdfType, owlOntology)(func(triple rdflibgo.Triple) bool {
		subject, ok := triple.Subject.(rdflibgo.URIRef)
		if !ok {
			return true
		}
		ontology.IRI = subject.Value()
		ontology.IsOntology = true
		return false
	})

	subject := rdflibgo.NewURIRefUnsafe(ontology.IRI)
	ontology.IsProject = pkg.Contains(graph, subject, rdfType, schemaProject)
	pkg.Triples(graph, subject, &dctermsDescription, nil)(func(triple rdflibgo.Triple) bool {
		literal, ok := triple.Object.(rdflibgo.Literal)
		if !ok {
			return true
		}
		ontology.Description = literal.Lexical()
		return false
	})
	for _, predicate := range []rdflibgo.URIRef{dctermsTitle, dcTitle} {
		pkg.Triples(graph, subject, &predicate, nil)(func(triple rdflibgo.Triple) bool {
			literal, ok := triple.Object.(rdflibgo.Literal)
			if !ok {
				return true
			}
			ontology.Title = literal.Lexical()
			return false
		})
		if ontology.Title != "" {
			break
		}
	}
	pkg.Triples(graph, subject, &owlImports, nil)(func(triple rdflibgo.Triple) bool {
		if object, ok := triple.Object.(rdflibgo.URIRef); ok {
			ontology.Imports = append(ontology.Imports, object.Value())
		}
		return true
	})
	sort.Strings(ontology.Imports)
	pkg.Triples(graph, subject, &rdfsComment, nil)(func(triple rdflibgo.Triple) bool {
		literal, ok := triple.Object.(rdflibgo.Literal)
		if !ok {
			return true
		}
		ontology.Comment = literal.Lexical()
		return false
	})

	return ontology, nil
}

// WriteOntology writes the project node into .sal/config.jsonld, preserving
// whatever pinned vocabulary nodes are already there byte for byte, since
// neither `sal init` nor `sal import` inspects them.
func WriteOntology(path string, ontology *Ontology) error {
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

// SerializeOntology renders the project node as a standalone JSON-LD
// document, with its own @context, in the shape `sal init` and `sal import`
// maintain. A node a user has added other statements to is serialized from
// its own graph instead, so that hand written triples survive a rewrite at
// the cost of the generated file's formatting. This is also what build
// validates the project's own statements against and folds into its source
// hash, since the node has no file of its own to do either against directly.
func SerializeOntology(ontology *Ontology) ([]byte, error) {
	if ontology.Graph == nil || !ontology.hasExtraStatements() {
		return renderOntology(ontology)
	}

	graph := ontology.Graph
	subject := rdflibgo.NewURIRefUnsafe(ontology.IRI)
	if ontology.IsProject {
		pkg.Bind(graph, "schema", rdflibgo.NewURIRefUnsafe(schemaNamespace))
		pkg.AddTriple(graph, subject, rdflibgo.RDF.Type, schemaProject)
	}
	if ontology.Description != "" {
		pkg.Bind(graph, "dcterms", rdflibgo.NewURIRefUnsafe(dctermsNamespace))
		// Set rather than Add so that a new description replaces the old one
		pkg.SetTriple(graph, subject, dctermsDescription, rdflibgo.NewLiteral(ontology.Description))
	}
	if ontology.IsOntology {
		pkg.Bind(graph, "owl", rdflibgo.NewURIRefUnsafe(owlNamespace))
		pkg.AddTriple(graph, subject, rdflibgo.RDF.Type, owlOntology)
	}
	if ontology.Title != "" {
		pkg.Bind(graph, "dcterms", rdflibgo.NewURIRefUnsafe(dctermsNamespace))
		pkg.SetTriple(graph, subject, dctermsTitle, rdflibgo.NewLiteral(ontology.Title))
		pkg.RemoveTriples(graph, subject, &dcTitle, nil)
	}
	if len(ontology.Imports) > 0 {
		pkg.Bind(graph, "owl", rdflibgo.NewURIRefUnsafe(owlNamespace))
	}
	for _, iri := range ontology.Imports {
		pkg.AddTriple(graph, subject, owlImports, rdflibgo.NewURIRefUnsafe(iri))
	}
	if ontology.Comment != "" {
		pkg.Bind(graph, "rdfs", rdflibgo.NewURIRefUnsafe(rdfsNamespace))
		pkg.SetTriple(graph, subject, rdfsComment, rdflibgo.NewLiteral(ontology.Comment))
	}

	var buf bytes.Buffer
	if err := jsonld.Serialize(context.Background(), graph, &buf, jsonld.WithBase(ontology.Base)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hasExtraStatements reports whether the file says anything beyond the ontology
// header the generated template covers.
func (ontology *Ontology) hasExtraStatements() bool {
	subject := rdflibgo.NewURIRefUnsafe(ontology.IRI)
	extra := false
	pkg.Triples(ontology.Graph, nil, nil, nil)(func(triple rdflibgo.Triple) bool {
		if !triple.Subject.Equal(subject) {
			extra = true
			return false
		}
		switch {
		case triple.Predicate.Equal(rdflibgo.RDF.Type) && triple.Object.Equal(owlOntology):
		case triple.Predicate.Equal(rdflibgo.RDF.Type) && triple.Object.Equal(schemaProject):
		case triple.Predicate.Equal(dcTitle), triple.Predicate.Equal(dctermsTitle):
		case triple.Predicate.Equal(dctermsDescription):
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

// ontologyDocument is the JSON-LD shape renderOntology writes. The project is
// written as the relative IRI "." when it is the project base, which is how a
// SAL project names itself, resolved the same way `<.>` was in the Turtle this
// file used to be written as. Type is the one type as a string, or both as an
// array, so the node `sal init` alone wrote reads as plainly as it was asked
// for.
type ontologyDocument struct {
	Context     map[string]string   `json:"@context"`
	ID          string              `json:"@id"`
	Type        any                 `json:"@type,omitempty"`
	Description string              `json:"dcterms:description,omitempty"`
	Title       string              `json:"dcterms:title,omitempty"`
	Imports     []ontologyImportRef `json:"owl:imports,omitempty"`
	Comment     string              `json:"rdfs:comment,omitempty"`
}

type ontologyImportRef struct {
	ID string `json:"@id"`
}

// renderOntology writes the project node as the flat JSON-LD document `sal
// init` and `sal import` generate. The @context binds only the prefixes the
// node uses, since build validates this document like a source file and pins
// every prefix it declares.
func renderOntology(ontology *Ontology) ([]byte, error) {
	id := ontology.IRI
	if ontology.IRI == ontology.Base {
		id = "."
	}

	context := map[string]string{}
	var types []string
	if ontology.IsProject {
		context["schema"] = schemaNamespace
		types = append(types, "schema:Project")
	}
	if ontology.IsOntology {
		context["owl"] = owlNamespace
		types = append(types, "owl:Ontology")
	}
	if ontology.Description != "" || ontology.Title != "" {
		context["dcterms"] = dctermsNamespace
	}
	if len(ontology.Imports) > 0 {
		context["owl"] = owlNamespace
	}
	if ontology.Comment != "" {
		context["rdfs"] = rdfsNamespace
	}
	doc := ontologyDocument{
		Context:     context,
		ID:          id,
		Description: ontology.Description,
		Title:       ontology.Title,
		Comment:     ontology.Comment,
	}
	switch len(types) {
	case 1:
		doc.Type = types[0]
	case 2:
		doc.Type = types
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
