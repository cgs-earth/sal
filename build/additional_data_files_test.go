package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestAddAdditionalDataFileMetadataAddsFlatGeobufTriples(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "countries.fgb"), []byte("flatgeobuf"), 0644))

	graph := rdflibgo.NewGraph()
	require.NoError(t, addAdditionalDataFileMetadata(graph, dataDir, "sal-cli", "https://example.com/sal-cli/"))

	countries := rdflibgo.NewURIRefUnsafe("countries.fgb")
	requireTriple(t, graph, countries, rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe(dcatNamespace+"Dataset"))
	requireTriple(t, graph, countries, rdflibgo.NewURIRefUnsafe(schemaNamespace+"name"), rdflibgo.NewLiteral("countries.fgb"))
	requireTriple(t, graph, countries, rdflibgo.NewURIRefUnsafe(dctNamespace+"format"), rdflibgo.NewLiteral("application/vnd.flatgeobuf"))
	requireTriple(t, graph, countries, rdflibgo.NewURIRefUnsafe(dcatNamespace+"downloadURL"), countries)
	requireTriple(t, graph, countries, rdflibgo.NewURIRefUnsafe(schemaNamespace+"isAccessibleForFree"), rdflibgo.NewLiteral(true))
}

func TestAddAdditionalDataFileMetadataSkipsBuiltProjectOutputs(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sal-cli", "triples"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sal-cli.nq"), []byte("nquads"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "countries.fgb"), []byte("flatgeobuf"), 0644))

	graph := rdflibgo.NewGraph()
	require.NoError(t, addAdditionalDataFileMetadata(graph, dataDir, "sal-cli", "https://example.com/sal-cli/"))

	requireTriple(t, graph,
		rdflibgo.NewURIRefUnsafe("countries.fgb"),
		rdflibgo.NewURIRefUnsafe(schemaNamespace+"name"),
		rdflibgo.NewLiteral("countries.fgb"),
	)
	requireNoTripleForSubject(t, graph, rdflibgo.NewURIRefUnsafe("sal-cli.nq"))
	requireNoTripleForSubject(t, graph, rdflibgo.NewURIRefUnsafe("sal-cli"))
}

func requireTriple(t *testing.T, graph *rdflibgo.Graph, subject rdflibgo.Subject, predicate rdflibgo.URIRef, object rdflibgo.Term) {
	t.Helper()
	found := false
	graph.Triples(subject, &predicate, object)(func(triple rdflibgo.Triple) bool {
		found = true
		return false
	})
	require.True(t, found)
}

func requireNoTripleForSubject(t *testing.T, graph *rdflibgo.Graph, subject rdflibgo.Subject) {
	t.Helper()
	graph.Triples(subject, nil, nil)(func(triple rdflibgo.Triple) bool {
		require.Failf(t, "unexpected triple", "%s", triple.String())
		return false
	})
}
