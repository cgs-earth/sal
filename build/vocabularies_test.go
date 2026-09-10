package build

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func classGraph(iri string) *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	graph.Add(rdflibgo.NewURIRefUnsafe(iri), rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"))
	return graph
}

func TestPinnedVocabularyGraphsParsesEveryPinInNamespaceOrder(t *testing.T) {
	var requested []string
	vocabularies, err := pinnedVocabularyGraphs([]string{"https://vocab.test/a#", "https://vocab.test/b#"}, nil, func(iri string) (*rdflibgo.Graph, error) {
		requested = append(requested, iri)
		return classGraph(iri + "Thing"), nil
	})

	require.NoError(t, err)
	require.Equal(t, []string{"https://vocab.test/a#", "https://vocab.test/b#"}, requested)
	require.Len(t, vocabularies, 2)
	require.Equal(t, "https://vocab.test/a#", vocabularies[0].Namespace)
	require.True(t, vocabularies[0].Graph.Contains(
		rdflibgo.NewURIRefUnsafe("https://vocab.test/a#Thing"),
		rdflibgo.RDF.Type,
		rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"),
	))
	require.Equal(t, "https://vocab.test/b#", vocabularies[1].Namespace)
}

// An ontology the project imports is already in the graph as the project's
// own statements, so its pin is not read again only to be deduplicated away.
func TestPinnedVocabularyGraphsSkipsImportedOntologies(t *testing.T) {
	var requested []string
	vocabularies, err := pinnedVocabularyGraphs([]string{"https://vocab.test/imported", "https://vocab.test/things#"}, []string{"https://vocab.test/imported"}, func(iri string) (*rdflibgo.Graph, error) {
		requested = append(requested, iri)
		return classGraph(iri + "Thing"), nil
	})

	require.NoError(t, err)
	require.Equal(t, []string{"https://vocab.test/things#"}, requested)
	require.Len(t, vocabularies, 1)
	require.Equal(t, "https://vocab.test/things#", vocabularies[0].Namespace)
}

func TestPinnedVocabularyGraphsReportsWhichVocabularyCouldNotBeRead(t *testing.T) {
	_, err := pinnedVocabularyGraphs([]string{"https://vocab.test/things#"}, nil, func(iri string) (*rdflibgo.Graph, error) {
		return nil, fmt.Errorf("no such blob")
	})

	require.ErrorContains(t, err, "read pinned vocabulary https://vocab.test/things#")
	require.ErrorContains(t, err, "no such blob")
}
