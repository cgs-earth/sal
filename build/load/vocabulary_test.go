package load

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go"
	"github.com/apache/iceberg-go/catalog"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

const (
	vocabularyTestClass    = "https://vocab.test/things#Widget"
	vocabularyTestSubClass = "http://www.w3.org/2000/01/rdf-schema#subClassOf"
	vocabularyTestThing    = "https://vocab.test/things#Thing"
)

// subClassGraph is a graph with the one statement `Widget rdfs:subClassOf Thing`.
func subClassGraph() *rdflibgo.Graph {
	graph := rdflibgo.NewGraph()
	graph.Add(rdflibgo.NewURIRefUnsafe(vocabularyTestClass), rdflibgo.NewURIRefUnsafe(vocabularyTestSubClass), rdflibgo.NewURIRefUnsafe(vocabularyTestThing))
	return graph
}

func subClassHash() string {
	return tripleHash(vocabularyTestClass, vocabularyTestSubClass, vocabularyTestThing, "", "")
}

// newTestTable creates an empty triples table in a temporary warehouse.
func newTestTable(t *testing.T) (*LoadConfig, catalog.Catalog, *table.Table) {
	t.Helper()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	_, icebergSchema, err := GetSchemas()
	require.NoError(t, err)
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := NewIcebergTableFromCfg(context.Background(), icebergSchema, cat, cfg)
	require.NoError(t, err)
	return cfg, cat, tbl
}

func TestNewIcebergTableRefusesATableWithoutTheVocabularyColumn(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	require.NoError(t, cat.CreateNamespace(ctx, catalog.ToIdentifier("default"), nil))
	older := iceberg.NewSchema(1,
		iceberg.NestedField{ID: 1, Name: "subject", Type: iceberg.PrimitiveTypes.String, Required: true},
		iceberg.NestedField{ID: 2, Name: "object_language", Type: iceberg.PrimitiveTypes.String, Required: false},
	)
	_, err = cat.CreateTable(ctx, catalog.ToIdentifier("default", "triples"), older)
	require.NoError(t, err)

	_, icebergSchema, err := GetSchemas()
	require.NoError(t, err)
	_, err = NewIcebergTableFromCfg(ctx, icebergSchema, cat, cfg)

	require.ErrorContains(t, err, "without the vocabulary column")
	require.ErrorContains(t, err, "sal clean --wipe")
}

func TestTableRowsLetsAnAssertedStatementWinOverAVocabulary(t *testing.T) {
	asserted := subClassGraph()
	asserted.Add(rdflibgo.NewURIRefUnsafe("https://example.test/w"), rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe(vocabularyTestClass))
	vocabulary := subClassGraph()
	vocabulary.Add(rdflibgo.NewURIRefUnsafe(vocabularyTestClass), rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Class"))

	rows := tableRows(asserted, []VocabularyGraph{{Namespace: "https://vocab.test/things#", Graph: vocabulary}})

	require.Len(t, rows, 3)
	byHash := map[string]string{}
	for _, row := range rows {
		byHash[row.hash] = row.vocabulary
	}
	require.Equal(t, "", byHash[subClassHash()])
	require.Equal(t, "https://vocab.test/things#", byHash[tripleHash(vocabularyTestClass, rdflibgo.RDF.Type.Value(), "http://www.w3.org/2002/07/owl#Class", "", "")])
}

func TestTableRowsAttributesASharedStatementToTheFirstNamespace(t *testing.T) {
	rows := tableRows(rdflibgo.NewGraph(), []VocabularyGraph{
		{Namespace: "https://vocab.test/z#", Graph: subClassGraph()},
		{Namespace: "https://vocab.test/a#", Graph: subClassGraph()},
	})

	require.Len(t, rows, 1)
	require.Equal(t, "https://vocab.test/a#", rows[0].vocabulary)
	require.Equal(t, subClassHash(), rows[0].hash)
}

func TestProcessGraphDiffRewritesARowWhoseVocabularyChanged(t *testing.T) {
	ctx := context.Background()
	cfg, cat, tbl := newTestTable(t)
	arrowSchema, _, err := GetSchemas()
	require.NoError(t, err)
	vocabulary := []VocabularyGraph{{Namespace: "https://vocab.test/things#", Graph: subClassGraph()}}

	require.NoError(t, processGraph(ctx, tableRows(rdflibgo.NewGraph(), vocabulary), cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))
	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	existing, err := readExistingTriples(ctx, loaded)
	require.NoError(t, err)
	require.Equal(t, "https://vocab.test/things#", existing[subClassHash()].vocabulary)

	// the project now asserts the statement the vocabulary makes
	require.NoError(t, processGraph(ctx, tableRows(subClassGraph(), vocabulary), cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))
	loaded, err = cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.Equal(t, table.OpOverwrite, loaded.CurrentSnapshot().Summary.Operation)
	existing, err = readExistingTriples(ctx, loaded)
	require.NoError(t, err)
	require.Len(t, existing, 1)
	require.Equal(t, "", existing[subClassHash()].vocabulary)

	// and stops asserting it again
	require.NoError(t, processGraph(ctx, tableRows(rdflibgo.NewGraph(), vocabulary), cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))
	loaded, err = cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	existing, err = readExistingTriples(ctx, loaded)
	require.NoError(t, err)
	require.Len(t, existing, 1)
	require.Equal(t, "https://vocab.test/things#", existing[subClassHash()].vocabulary)
}

func TestProcessGraphDiffLeavesAnUnchangedVocabularyRowAlone(t *testing.T) {
	ctx := context.Background()
	cfg, cat, tbl := newTestTable(t)
	arrowSchema, _, err := GetSchemas()
	require.NoError(t, err)
	vocabulary := []VocabularyGraph{{Namespace: "https://vocab.test/things#", Graph: subClassGraph()}}

	require.NoError(t, processGraph(ctx, tableRows(rdflibgo.NewGraph(), vocabulary), cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))
	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	first := loaded.CurrentSnapshot().SnapshotID

	require.NoError(t, processGraph(ctx, tableRows(rdflibgo.NewGraph(), vocabulary), cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))
	loaded, err = cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.Equal(t, first, loaded.CurrentSnapshot().SnapshotID)
}

// A vocabulary's blank nodes are relabeled from their structure like the
// project's, so a document parsed again with fresh labels rewrites nothing.
func TestWriteGraphToIcebergStabilizesVocabularyBlankNodes(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
	restriction := func(label string) []VocabularyGraph {
		graph := rdflibgo.NewGraph()
		node := rdflibgo.NewBNode(label)
		graph.Add(rdflibgo.NewURIRefUnsafe(vocabularyTestClass), rdflibgo.NewURIRefUnsafe(vocabularyTestSubClass), node)
		graph.Add(node, rdflibgo.RDF.Type, rdflibgo.NewURIRefUnsafe("http://www.w3.org/2002/07/owl#Restriction"))
		return []VocabularyGraph{{Namespace: "https://vocab.test/things#", Graph: graph}}
	}

	require.NoError(t, WriteGraphToIceberg(ctx, rdflibgo.NewGraph(), restriction("first"), cfg, map[string]string{"sal.hash": "first"}))
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)
	first := tbl.CurrentSnapshot().SnapshotID

	require.NoError(t, WriteGraphToIceberg(ctx, rdflibgo.NewGraph(), restriction("second"), cfg, map[string]string{"sal.hash": "second"}))
	tbl, err = cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)

	require.Equal(t, first, tbl.CurrentSnapshot().SnapshotID)
	existing, err := readExistingTriples(ctx, tbl)
	require.NoError(t, err)
	require.Len(t, existing, 2)
	for _, triple := range existing {
		require.Equal(t, "https://vocab.test/things#", triple.vocabulary)
	}
}

func TestGraphRecordReaderWritesTheVocabularyColumn(t *testing.T) {
	arrowSchema, _, err := GetSchemas()
	require.NoError(t, err)
	triple := rdflibgo.Triple{
		Subject:   rdflibgo.NewURIRefUnsafe(vocabularyTestClass),
		Predicate: rdflibgo.NewURIRefUnsafe(vocabularyTestSubClass),
		Object:    rdflibgo.NewURIRefUnsafe(vocabularyTestThing),
	}
	rdr := newFilteredGraphRecordReader([]tableRow{
		{triple: triple, hash: "asserted", vocabulary: ""},
		{triple: triple, hash: "stated", vocabulary: "https://vocab.test/things#"},
	}, arrowSchema, 10, nil)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	hashes := rec.Column(11).(*array.String)
	vocabularies := rec.Column(12).(*array.String)
	require.Equal(t, int64(2), rec.NumRows())
	require.Equal(t, "asserted", hashes.Value(0))
	require.True(t, vocabularies.IsNull(0))
	require.Equal(t, "stated", hashes.Value(1))
	require.Equal(t, "https://vocab.test/things#", vocabularies.Value(1))
	require.False(t, rdr.Next())
}
