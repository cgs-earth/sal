package load

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	geoarrow "github.com/geoarrow/geoarrow-go"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
)

func TestGraphRecordReaderStreamsGraphTriples(t *testing.T) {
	graph := rdflibgo.NewGraph()
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s1"),
		predicate,
		rdflibgo.NewLiteral("one"),
	)
	graph.Add(
		rdflibgo.NewBNode("subject"),
		predicate,
		rdflibgo.NewURIRefUnsafe("http://example.com/o2"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s3"),
		predicate,
		rdflibgo.NewLiteral("three", rdflibgo.WithLang("en")),
	)

	arrowSchema, _, err := GetSchemas(false)
	require.NoError(t, err)
	rdr := newGraphRecordReader(graph, arrowSchema, 2)
	defer rdr.Release()

	var batches int
	var rows [][3]string
	for rdr.Next() {
		batches++
		rec := rdr.RecordBatch()
		subjects := rec.Column(0).(*array.String)
		predicates := rec.Column(1).(*array.String)
		objects := rec.Column(2).(*array.String)
		for i := 0; i < int(rec.NumRows()); i++ {
			rows = append(rows, [3]string{subjects.Value(i), predicates.Value(i), objects.Value(i)})
		}
	}

	require.NoError(t, rdr.Err())
	require.Equal(t, 2, batches)
	require.Equal(t, int64(3), rdr.RowsRead())
	require.ElementsMatch(t, [][3]string{
		{"http://example.com/s1", "http://example.com/p", "one"},
		{"subject", "http://example.com/p", "http://example.com/o2"},
		{"http://example.com/s3", "http://example.com/p", "three"},
	}, rows)
}

func TestGraphRecordReaderSerializesObjectColumns(t *testing.T) {
	graph := rdflibgo.NewGraph()
	predicate := rdflibgo.NewURIRefUnsafe("http://example.com/p")
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s1"),
		predicate,
		rdflibgo.NewURIRefUnsafe("http://example.com/o"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s2"),
		predicate,
		rdflibgo.NewLiteral("42.5"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s3"),
		predicate,
		rdflibgo.NewLiteral("label"),
	)
	graph.Add(
		rdflibgo.NewURIRefUnsafe("http://example.com/s4"),
		predicate,
		rdflibgo.NewLiteral("POINT (1 2)", rdflibgo.WithDatatype(rdflibgo.NewURIRefUnsafe(geoSPARQLWKTLiteral))),
	)

	arrowSchema, _, err := GetSchemas(true)
	require.NoError(t, err)
	rdr := newGraphRecordReader(graph, arrowSchema, 10)
	defer rdr.Release()

	require.True(t, rdr.Next())
	rec := rdr.RecordBatch()
	require.Equal(t, int64(4), rec.NumRows())

	objectIRI := rec.Column(2).(*array.String)
	objectFloat := rec.Column(3).(*array.Float64)
	objectString := rec.Column(4).(*array.String)
	objectGeometry := rec.Column(5).(*geoarrow.WKBArray)
	subjects := rec.Column(0).(*array.String)

	expectedWKB, err := wktObjectToWKB("POINT (1 2)")
	require.NoError(t, err)

	rowsBySubject := map[string]int{}
	for i := 0; i < int(rec.NumRows()); i++ {
		rowsBySubject[subjects.Value(i)] = i
	}
	require.Contains(t, rowsBySubject, "http://example.com/s1")
	require.Contains(t, rowsBySubject, "http://example.com/s2")
	require.Contains(t, rowsBySubject, "http://example.com/s3")
	require.Contains(t, rowsBySubject, "http://example.com/s4")

	iriRow := rowsBySubject["http://example.com/s1"]
	require.Equal(t, "http://example.com/o", objectIRI.Value(iriRow))
	require.True(t, objectFloat.IsNull(iriRow))
	require.True(t, objectString.IsNull(iriRow))
	require.True(t, objectGeometry.IsNull(iriRow))

	floatRow := rowsBySubject["http://example.com/s2"]
	require.True(t, objectIRI.IsNull(floatRow))
	require.Equal(t, 42.5, objectFloat.Value(floatRow))
	require.True(t, objectString.IsNull(floatRow))
	require.True(t, objectGeometry.IsNull(floatRow))

	stringRow := rowsBySubject["http://example.com/s3"]
	require.True(t, objectIRI.IsNull(stringRow))
	require.True(t, objectFloat.IsNull(stringRow))
	require.Equal(t, "label", objectString.Value(stringRow))
	require.True(t, objectGeometry.IsNull(stringRow))

	geometryRow := rowsBySubject["http://example.com/s4"]
	require.True(t, objectIRI.IsNull(geometryRow))
	require.True(t, objectFloat.IsNull(geometryRow))
	require.True(t, objectString.IsNull(geometryRow))
	require.Equal(t, geoarrow.WKBBytes(expectedWKB), objectGeometry.Value(geometryRow))

	require.False(t, rdr.Next())
	require.NoError(t, rdr.Err())
}
