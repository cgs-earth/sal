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

	require.Equal(t, 3, arrowSchema.NumFields())
	require.Equal(t, "object", arrowSchema.Field(2).Name)
	require.Equal(t, "object", icebergSchema.Field(2).Name)
}

func TestGetSchemasUsesDataTypeColumnsWhenEnabled(t *testing.T) {
	arrowSchema, icebergSchema, err := GetSchemas(true)
	require.NoError(t, err)

	require.Equal(t, 6, arrowSchema.NumFields())
	require.Equal(t, "object_iri", arrowSchema.Field(2).Name)
	require.Equal(t, "object_float", arrowSchema.Field(3).Name)
	require.Equal(t, "object_string", arrowSchema.Field(4).Name)
	require.Equal(t, "object_geometry", arrowSchema.Field(5).Name)
	require.Equal(t, "object_geometry", icebergSchema.Field(5).Name)
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

	require.NoError(t, appendGraph(ctx, tbl, graph, arrowSchema, cfg.BatchSize))
	loaded, err := cat.LoadTable(ctx, tbl.Identifier())
	require.NoError(t, err)
	require.NotNil(t, loaded.CurrentSnapshot())
	require.NotNil(t, loaded.CurrentSnapshot().Summary)
	require.Equal(t, 3, loaded.Metadata().Version())
	require.Equal(t, "geometry", loaded.Schema().Field(5).Type.String())
	require.Equal(t, "1", loaded.CurrentSnapshot().Summary.Properties["added-records"])
}
