package load

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/apache/iceberg-go/table"
	geoarrow "github.com/geoarrow/geoarrow-go"
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

func TestNQuadRecordReaderStreamsFiles(t *testing.T) {
	dir := t.TempDir()
	first := writeGzipNQuads(t, dir, "first.nq.gz", []string{
		`<http://example.com/s1> <http://example.com/p> "one" .`,
		`not valid`,
	})
	second := writeGzipNQuads(t, dir, "second.nq.gz", []string{
		`<http://example.com/s2> <http://example.com/p> "two" .`,
		`<http://example.com/s3> <http://example.com/p> "three" .`,
	})

	arrowSchema, _, err := GetSchemas(false)
	require.NoError(t, err)
	rdr := newNQuadRecordReader([]string{first, second}, arrowSchema, 2)
	defer rdr.Release()

	var batches int
	var rows int64
	for rdr.Next() {
		batches++
		rows += rdr.RecordBatch().NumRows()
	}
	require.NoError(t, rdr.Err())
	require.Equal(t, 2, batches)
	require.Equal(t, int64(3), rows)
	require.Equal(t, int64(3), rdr.RowsRead())
}

func TestNQuadRecordReaderSerializesLegacyObjectColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeGzipNQuads(t, dir, "objects.nq.gz", []string{
		`<http://example.com/s1> <http://example.com/p> <http://example.com/o> .`,
		`<http://example.com/s2> <http://example.com/p> "label" .`,
	})

	arrowSchema, _, err := GetSchemas(false)
	require.NoError(t, err)
	rdr := newNQuadRecordReader([]string{path}, arrowSchema, 10)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	require.Equal(t, int64(2), rec.NumRows())

	objects := rec.Column(2).(*array.String)
	require.Equal(t, "http://example.com/o", objects.Value(0))
	require.Equal(t, "label", objects.Value(1))

	require.False(t, rdr.Next())
	require.NoError(t, rdr.Err())
}

func TestNQuadRecordReaderSerializesObjectColumns(t *testing.T) {
	dir := t.TempDir()
	path := writeGzipNQuads(t, dir, "objects.nq.gz", []string{
		`<http://example.com/s1> <http://example.com/p> <http://example.com/o> .`,
		`<http://example.com/s2> <http://example.com/p> "42.5" .`,
		`<http://example.com/s3> <http://example.com/p> "label" .`,
		`<http://example.com/s4> <http://example.com/p> "POINT (1 2)"^^<http://www.opengis.net/ont/geosparql#wktLiteral> .`,
	})

	arrowSchema, _, err := GetSchemas(true)
	require.NoError(t, err)
	rdr := newNQuadRecordReader([]string{path}, arrowSchema, 10)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	require.Equal(t, int64(4), rec.NumRows())

	objectIRI := rec.Column(2).(*array.String)
	objectFloat := rec.Column(3).(*array.Float64)
	objectString := rec.Column(4).(*array.String)
	objectGeometry := rec.Column(5).(*geoarrow.WKBArray)

	require.Equal(t, "http://example.com/o", objectIRI.Value(0))
	require.True(t, objectFloat.IsNull(0))
	require.True(t, objectString.IsNull(0))
	require.True(t, objectGeometry.IsNull(0))

	require.True(t, objectIRI.IsNull(1))
	require.Equal(t, 42.5, objectFloat.Value(1))
	require.True(t, objectString.IsNull(1))
	require.True(t, objectGeometry.IsNull(1))

	require.True(t, objectIRI.IsNull(2))
	require.True(t, objectFloat.IsNull(2))
	require.Equal(t, "label", objectString.Value(2))
	require.True(t, objectGeometry.IsNull(2))

	expectedWKB, err := wktObjectToWKB("POINT (1 2)")
	require.NoError(t, err)
	require.True(t, objectIRI.IsNull(3))
	require.True(t, objectFloat.IsNull(3))
	require.True(t, objectString.IsNull(3))
	require.Equal(t, geoarrow.WKBBytes(expectedWKB), objectGeometry.Value(3))

	require.False(t, rdr.Next())
	require.NoError(t, rdr.Err())
}

func TestAppendGraphIngestsSimpleWKTGeometry(t *testing.T) {
	ctx := context.Background()
	cfg := &LoadCmd{
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

func writeGzipNQuads(t *testing.T, dir, name string, lines []string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	require.NoError(t, err)
	gz := gzip.NewWriter(f)
	for _, line := range lines {
		_, err := gz.Write([]byte(line + "\n"))
		require.NoError(t, err)
	}
	require.NoError(t, gz.Close())
	require.NoError(t, f.Close())
	return path
}
