package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recordSpans routes every span started through the global tracer provider to
// an in-memory recorder for the rest of the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func TestInstrumentRecordsAServerSpanNamedByMethodAndPath(t *testing.T) {
	recorder := recordSpans(t)
	server := httptest.NewServer(instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()

	res, err := http.Post(server.URL+"/sparql", "application/sparql-query", nil)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, "POST /sparql", ended[0].Name())
	require.Equal(t, trace.SpanKindServer, ended[0].SpanKind())
}

func TestInstrumentLeavesUIAssetRequestsOutOfTheTrace(t *testing.T) {
	recorder := recordSpans(t)
	server := httptest.NewServer(instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()

	res, err := http.Get(server.URL + "/assets/index-abc123.js")
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	require.Empty(t, recorder.Ended())
}
