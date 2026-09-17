package load

import (
	"context"
	"testing"

	"github.com/apache/iceberg-go/catalog/hadoop"
	"github.com/stretchr/testify/require"
	rdflibgo "github.com/tggo/goRDFlib"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The diff against the table is what decides how much of a build is new, so
// its span says how many triples it found on each side.
func TestDiffSpanReportsHowManyTriplesChanged(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	ctx := context.Background()
	cfg := &LoadConfig{BatchSize: 10, ParquetCompression: "snappy", Warehouse: t.TempDir(), Namespace: "default"}
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
	second := rdflibgo.NewGraph()
	second.Add(rdflibgo.NewURIRefUnsafe("http://example.com/keep"), predicate, rdflibgo.NewLiteral("same"))
	second.Add(rdflibgo.NewURIRefUnsafe("http://example.com/add"), predicate, rdflibgo.NewLiteral("new"))
	require.NoError(t, processGraph(ctx, second, cat, tbl.Identifier(), arrowSchema, cfg.BatchSize))

	var names []string
	var lastDiff attribute.Set
	for _, span := range recorder.Ended() {
		names = append(names, span.Name())
		if span.Name() == "iceberg.diff" {
			lastDiff = attribute.NewSet(span.Attributes()...)
		}
	}
	require.Equal(t, []string{
		"iceberg.diff", "iceberg.write_data_files", "iceberg.commit",
		"iceberg.diff", "iceberg.write_data_files", "iceberg.commit",
	}, names)
	for key, want := range map[attribute.Key]int64{
		"sal.triples.existing":  2,
		"sal.triples.added":     1,
		"sal.triples.removed":   1,
		"sal.triples.unchanged": 1,
	} {
		value, ok := lastDiff.Value(key)
		require.True(t, ok, "missing attribute %s", key)
		require.Equal(t, want, value.AsInt64(), "attribute %s", key)
	}
}
