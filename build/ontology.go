package build

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/cgs-earth/sal/build/validate"
	"github.com/cgs-earth/sal/importation"
	"github.com/cgs-earth/sal/pkg"
	rdflibgo "github.com/tggo/goRDFlib"
)

// appendProjectOntology adds .sal/ontology.ttl to the files being built. The
// project ontology is a source file like any other, so it is validated and its
// own statements land in the data product; only the ontologies it imports are
// resolved later, once the build is known to be committable. The walk that
// collects source files skips .sal, so the ontology is named here rather than
// found there.
func appendProjectOntology(files []string) ([]string, error) {
	path, err := pkg.SalOntologyPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return files, nil
	} else if err != nil {
		return nil, err
	}
	if slices.Contains(files, path) {
		return files, nil
	}
	return append(files, path), nil
}

// ImportOntologies resolves everything the project's .sal/ontology.ttl lists
// with owl:imports. An ontology document is merged into the graph being built,
// so that an ontology a project depends on is carried by the data product rather
// than only referenced from it. A salmodule:// import is such a document too;
// validate.FetchGraph obtains it by cloning and building the module rather than
// over HTTP. An OCI artifact is pulled to disk instead and kept out of the table
// entirely. The statements the file makes about the project itself arrive with
// it as a source file; see appendProjectOntology.
func ImportOntologies(graph *rdflibgo.Graph) error {
	path, err := pkg.SalOntologyPath()
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
	return importOntologies(graph, path, base, validate.FetchGraph, pull)
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
// project's .sal/ontology.ttl stays the record of what was imported.
func dropImportStatement(graph *rdflibgo.Graph, iri string) {
	imports := rdflibgo.NewURIRefUnsafe(owlImportsIRI)
	graph.Remove(nil, &imports, rdflibgo.NewURIRefUnsafe(iri))
}

const owlImportsIRI = "http://www.w3.org/2002/07/owl#imports"
