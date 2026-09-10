package build

import (
	"fmt"
	"log/slog"

	"github.com/cgs-earth/sal/build/load"
	"github.com/cgs-earth/sal/build/validate"
	rdflibgo "github.com/tggo/goRDFlib"
)

// PinnedVocabularyGraphs parses every vocabulary the project pins into a graph
// of its own, so that build can commit the statements a vocabulary makes
// marked as that vocabulary's rather than as the project's. imported are the
// ontology IRIs ImportOntologies already merged into the graph unmarked; a pin
// recorded under one of those is skipped rather than parsed a second time only
// for every one of its rows to be deduplicated away.
func PinnedVocabularyGraphs(pins *validate.PinnedVocabularies, imported []string) ([]load.VocabularyGraph, error) {
	return pinnedVocabularyGraphs(pins.IDs(), imported, func(iri string) (*rdflibgo.Graph, error) {
		return validate.PinnedGraph(pins, iri)
	})
}

func pinnedVocabularyGraphs(ids []string, imported []string, fetch func(string) (*rdflibgo.Graph, error)) ([]load.VocabularyGraph, error) {
	skip := make(map[string]struct{}, len(imported))
	for _, iri := range imported {
		skip[iri] = struct{}{}
	}
	var vocabularies []load.VocabularyGraph
	for _, id := range ids {
		if _, ok := skip[id]; ok {
			continue
		}
		graph, err := fetch(id)
		if err != nil {
			return nil, fmt.Errorf("build: read pinned vocabulary %s: %w", id, err)
		}
		vocabularies = append(vocabularies, load.VocabularyGraph{Namespace: id, Graph: graph})
		slog.Info(fmt.Sprintf("Carrying %d triples from the pinned %s vocabulary", graphTripleCount(graph), id))
	}
	return vocabularies, nil
}
