package build

import (
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

// ImportOntologies merges every ontology the project's .sal/ontology.ttl lists
// with owl:imports into the graph being built, so that an ontology a project
// depends on is carried by the data product rather than only referenced from it.
// The statements the file makes about the project itself arrive with it as a
// source file; see appendProjectOntology.
func ImportOntologies(graph *rdflibgo.Graph) error {
	path, err := pkg.SalOntologyPath()
	if err != nil {
		return err
	}
	base, err := pkg.DefaultSalBase()
	if err != nil {
		return err
	}
	return importOntologies(graph, path, base, validate.FetchGraph)
}

func importOntologies(graph *rdflibgo.Graph, path string, base string, fetch func(string) (*rdflibgo.Graph, error)) error {
	ontology, err := importation.ReadOntology(path, base)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if ontology == nil {
		return nil
	}

	for _, iri := range ontology.Imports {
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
