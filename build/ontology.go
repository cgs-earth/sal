package build

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// projectOntologyContent renders the project ontology node .sal/config.jsonld
// carries as a standalone JSON-LD document, so it can be validated and hashed
// like a source file even though it is a node in a shared file rather than a
// file of its own. It returns nil when the project has not declared an
// ontology yet.
func projectOntologyContent(base string) ([]byte, error) {
	path, err := pkg.SalConfigPath()
	if err != nil {
		return nil, err
	}
	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if ontology == nil {
		return nil, nil
	}
	return importation.SerializeOntology(ontology)
}

// ImportOntologies resolves everything the project's ontology node in
// .sal/config.jsonld lists with owl:imports, at the version the project pins
// for it. An ontology document is merged into the graph being built, so that
// an ontology a project depends on is carried by the data product rather than
// only referenced from it; a vocabulary the project pins but does not import
// stays out of the graph. A salmodule:// import is such a document too;
// validate.FetchGraph obtains it by cloning and building the module rather
// than over HTTP. An OCI artifact is pulled to disk instead and kept out of
// the table entirely. The statements the ontology node makes about the
// project itself arrive separately, validated directly from
// projectOntologyContent rather than through this function.
func ImportOntologies(graph *rdflibgo.Graph, pins *validate.PinnedVocabularies) error {
	path, err := pkg.SalConfigPath()
	if err != nil {
		return err
	}
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	importsDir, err := pkg.SalImportsDir()
	if err != nil {
		return err
	}
	pull := func(iri string) error {
		// build has no registry flags, so an artifact behind a private registry
		// is authenticated with the environment the other commands fall back to
		return importation.PullArtifact(context.Background(), importsDir, iri, importation.ArtifactCredentialsFromEnv())
	}
	fetch := func(iri string) (*rdflibgo.Graph, error) {
		return validate.PinnedGraph(pins, iri)
	}
	return importOntologies(graph, path, base, fetch, pull)
}

func importOntologies(graph *rdflibgo.Graph, path string, base string, fetch func(string) (*rdflibgo.Graph, error), pull func(string) error) error {
	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if ontology == nil {
		return nil
	}

	for _, iri := range ontology.Imports {
		if importation.IsOciImport(iri) {
			if err := pull(iri); err != nil {
				return fmt.Errorf("build: import %s: %w", iri, err)
			}
			dropImportStatement(graph, iri)
			continue
		}

		imported, err := fetch(iri)
		if err != nil {
			return fmt.Errorf("build: import %s: %w", iri, err)
		}
		var count int
		imported.Triples(nil, nil, nil)(func(rdflibgo.Triple) bool {
			count++
			return true
		})
		mergeGraph(graph, imported)
		slog.Info(fmt.Sprintf("Imported %d triples from %s", count, iri))
	}
	return nil
}

// dropImportStatement removes the owl:imports statement naming an OCI artifact
// from the graph. The artifact is a file the project pulls rather than an
// ontology it merges, so nothing about it belongs in the triples table; the
// project's ontology node in .sal/config.jsonld stays the record of what was imported.
func dropImportStatement(graph *rdflibgo.Graph, iri string) {
	imports := rdflibgo.NewURIRefUnsafe(owlImportsIRI)
	graph.Remove(nil, &imports, rdflibgo.NewURIRefUnsafe(iri))
}

const owlImportsIRI = "http://www.w3.org/2002/07/owl#imports"
