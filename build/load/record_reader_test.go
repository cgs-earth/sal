package load

import (
	"context"
	"testing"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestGetSchemasUseNativeIcebergGeometry(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas(true)
	require.NoError(t, err)

	convertedType, err := table.ArrowTypeToIceberg(arrowSchema.Field(5).Type, false)
	require.NoError(t, err)
	require.Equal(t, icebergSchema.Field(5).Type.String(), convertedType.String())
	require.Equal(t, "geometry", icebergSchema.Field(5).Type.String())
}

func TestGetSchemasUsesLegacyObjectColumnByDefault(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas(false)
	require.NoError(t, err)

	require.Equal(t, 4, arrowSchema.NumFields())
	require.Equal(t, "object", arrowSchema.Field(2).Name)
	require.Equal(t, "triple_hash", arrowSchema.Field(3).Name)
	require.Equal(t, "object", icebergSchema.Field(2).Name)
	require.Equal(t, "triple_hash", icebergSchema.Field(3).Name)
	require.Equal(t, []int{4}, icebergSchema.IdentifierFieldIDs)
}

func TestGetSchemasUsesDataTypeColumnsWhenEnabled(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas(true)
	require.NoError(t, err)

	require.Equal(t, 7, arrowSchema.NumFields())
	require.Equal(t, "object_iri", arrowSchema.Field(2).Name)
	require.Equal(t, "object_float", arrowSchema.Field(3).Name)
	require.Equal(t, "object_string", arrowSchema.Field(4).Name)
	require.Equal(t, "object_geometry", arrowSchema.Field(5).Name)
	require.Equal(t, "triple_hash", arrowSchema.Field(6).Name)
	require.Equal(t, "object_geometry", icebergSchema.Field(5).Name)
	require.Equal(t, "triple_hash", icebergSchema.Field(6).Name)
	require.Equal(t, []int{7}, icebergSchema.IdentifierFieldIDs)
}

func TestAppendGraphIngestsSimpleWKTGeometry(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:           10,
		ParquetCompression:  "snappy",
		MetricsMode:         "truncate(16)",
		TargetFileSizeBytes: 0,
		Warehouse:           t.TempDir(),
		Namespace:           "default",
		DataTypeCols:        true,
	}

	arrowSchema, icebergSchema, err := GetSchemas(true)
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
	require.Equal(t, "geometry", loaded.Schema().Field(5).Type.String())
	require.Equal(t, "1", loaded.CurrentSnapshot().Summary.Properties["added-records"])
}

func TestProcessGraphDiffAddsAndRemovesByTripleHash(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:           10,
		ParquetCompression:  "snappy",
		MetricsMode:         "truncate(16)",
		TargetFileSizeBytes: 0,
		Warehouse:           t.TempDir(),
		Namespace:           "default",
	}

	arrowSchema, icebergSchema, err := GetSchemas(false)
	require.NoError(t, err)
	cat, err := hadoop.NewCatalog("local-catalog", cfg.Warehouse, nil)
	require.NoError(t, err)
	tbl, err := NewIcebergTableFromCfg(ctx, icebergSchema, cat, cfg)
	require.NoError(t, err)

	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	first := rdflibgo.NewGraph()
	first.Add(rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	first.Add(rdflibgo.NewURIRefUnsafe("http://example.com/drop"), predicate, rdflibgo.NewLiteral("old"))
	require.NoError(t, processGraph(ctx, first, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize, cfg.DataTypeCols))

	second := rdflibgo.NewGraph()
	second.Add(rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	second.Add(rdflibgo.NewURIRefUnsafe("http://example.com/add"), predicate, rdflibgo.NewLiteral("new"))
	require.NoError(t, processGraph(ctx, second, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize, cfg.DataTypeCols))

	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	hashes, err := readExistingTripleHashes(ctx, loaded)
	require.NoError(t, err)

	require.Contains(t, hashes, tripleHash("http://example.com/keep", "http://example.com/p", "same"))
	require.Contains(t, hashes, tripleHash("http://example.com/add", "http://example.com/p", "new"))
	require.NotContains(t, hashes, tripleHash("http://example.com/drop", "http://example.com/p", "old"))
	require.Len(t, hashes, 2)
}

func TestWriteGraphToIcebergDoesNotRewriteEquivalentBlankNodeGraph(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadConfig{
		BatchSize:           10,
		ParquetCompression:  "snappy",
		MetricsMode:         "truncate(16)",
		TargetFileSizeBytes: 0,
		Warehouse:           t.TempDir(),
		Namespace:           "default",
		DataTypeCols:        true,
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
