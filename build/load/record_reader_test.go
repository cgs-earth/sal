package load

import (
	"context"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestGetSchemasUseNativeIcebergGeometry(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)

	convertedType, err := table.ArrowTypeToIceberg(arrowSchema.Field(4).Type, false)
	require.NoError(t, err)
	require.Equal(t, icebergSchema.Field(4).Type.String(), convertedType.String())
	require.Equal(t, "geometry", icebergSchema.Field(4).Type.String())
}

func TestGetSchemasSplitsObjectsByDatatype(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)

	names := []string{"subject", "predicate", "object_string", "object_iri", "object_geometry", "object_byte", "object_integer", "object_float", "object_time", "object_type", "triple_hash"}
	require.Equal(t, len(names), arrowSchema.NumFields())
	require.Equal(t, len(names), len(icebergSchema.Fields()))
	for i, name := range names {
		require.Equal(t, name, arrowSchema.Field(i).Name)
		require.Equal(t, name, icebergSchema.Field(i).Name)
	}
	require.Equal(t, []int{11}, icebergSchema.IdentifierFieldIDs)
}

func TestAppendGraphIngestsSimpleWKTGeometry(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := NewIcebergTableFromCfg(ctx, icebergSchema, cat, cfg)
	require.NoError(t, err)

	graph := rdflibgo.NewGraph()
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s"),
		rdflibgo.NewURIRefUnsafe("http://example.com/hasGeometry"),
		rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(geoSPARQLWKTLiteral))),
	)

	require.NoError(t, appendGraph(ctx, tbl, graph, arrowSchema, cfg.BatchSize, nil))
	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.NotNil(t, loaded.CurrentSnapshot())
	require.NotNil(t, loaded.CurrentSnapshot().Summary)
	require.Equal(t, 3, loaded.Metadata().Version())
	require.Equal(t, "geometry", loaded.Schema().Field(4).Type.String())
	require.Equal(t, "1", loaded.CurrentSnapshot().Summary.Properties["added-records"])
}

func TestProcessGraphDiffAddsAndRemovesByTripleHash(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	arrowSchema, icebergSchema, err := GetSchemas()
	require.NoError(t, err)
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := NewIcebergTableFromCfg(ctx, icebergSchema, cat, cfg)
	require.NoError(t, err)

	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	first := rdflibgo.NewGraph()
	first.Add(rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	first.Add(rdflibgo.NewURIRefUnsafe("http://example.com/drop"), predicate, rdflibgo.NewLiteral("old"))
	require.NoError(t, processGraph(ctx, first, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))
	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	firstSnapshotID := loaded.CurrentSnapshot().SnapshotID

	second := rdflibgo.NewGraph()
	second.Add(rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	second.Add(rdflibgo.NewURIRefUnsafe("http://example.com/add"), predicate, rdflibgo.NewLiteral("new"))
	require.NoError(t, processGraph(ctx, second, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))

	loaded, err = cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.NotNil(t, loaded.CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, firstSnapshotID, *loaded.CurrentSnapshot().ParentSnapshotID)
	require.Equal(t, table.OpOverwrite, loaded.CurrentSnapshot().Summary.Operation)
	hashes, err := readExistingTripleHashes(ctx, loaded)
	require.NoError(t, err)

	xsdString := rdflibgo.XSDString.Value()
	require.Contains(t, hashes, tripleHash("http://example.com/keep", "http://example.com/p", "same", xsdString))
	require.Contains(t, hashes, tripleHash("http://example.com/add", "http://example.com/p", "new", xsdString))
	require.NotContains(t, hashes, tripleHash("http://example.com/drop", "http://example.com/p", "old", xsdString))
	require.Len(t, hashes, 2)
}

func TestWriteGraphToIcebergDoesNotRewriteEquivalentBlankNodeGraph(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	require.NoError(t, WriteGraphToIceberg(ctx, graphWithGeometryBlankNode("first"), cfg, map[string]string{"sal.hash": "first"}))
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)
	firstHashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)

	require.NoError(t, WriteGraphToIceberg(ctx, graphWithGeometryBlankNode("second"), cfg, map[string]string{"sal.hash": "second"}))
	tbl, err = cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)
	secondHashes, err := readExistingTripleHashes(ctx, tbl)
	require.NoError(t, err)

	require.Equal(t, firstHashes, secondHashes)
	require.Len(t, secondHashes, 3)
}

// TestWriteGraphToIcebergStoresBlankNodesWithNTriplesPrefix scans the written
// table and checks a blank node landed in the subject and object columns as
// "_:sal_..." rather than as a bare label, since the "_:" prefix is what lets
// readers tell a blank node apart from an IRI or a plain string.
func TestWriteGraphToIcebergStoresBlankNodesWithNTriplesPrefix(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:          10,
		ParquetCompression: "snappy",
		Warehouse:          t.TempDir(),
		Namespace:          "default",
	}

	require.NoError(t, WriteGraphToIceberg(ctx, graphWithGeometryBlankNode("b1"), cfg, map[string]string{"sal.hash": "h"}))
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := cat.LoadTable(ctx, table.Identifier{"default", "triples"})
	require.NoError(t, err)

	var subjects, objects []string
	// a blank node object is stored in object_string with its _: prefix
	_, records, err := tbl.Scan(table.WithSelectedFields("subject", "object_string")).ToArrowRecords(ctx)
	require.NoError(t, err)
	for rec, err := range records {
		require.NoError(t, err)
		subjectColumn := rec.Column(0).(*array.String)
		objectColumn := rec.Column(1).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			subjects = append(subjects, subjectColumn.Value(i))
			objects = append(objects, objectColumn.Value(i))
		}
		rec.Release()
	}

	require.Len(t, subjects, 3)
	blankID := ""
	for _, object := range objects {
		if strings.HasPrefix(object, "_:sal_") {
			blankID = object
		}
	}
	require.NotEmpty(t, blankID, "expected the hasGeometry object to be stored as a _:sal_ blank node, got objects %v", objects)
	require.Contains(t, subjects, blankID)
	require.NotContains(t, subjects, strings.TrimPrefix(blankID, "_:"))
}
