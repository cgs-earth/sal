package load

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
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
