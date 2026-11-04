package pkg

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordedAttributes is what the span a transfer reports on carries once it
// has ended.
func recordedAttributes(t *testing.T, run func()) map[string]int64 {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	run()
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attrs := map[string]int64{}
	for _, attr := range spans[0].Attributes() {
		if attr.Value.Type() == attribute.INT64 {
			attrs[string(attr.Key)] = attr.Value.AsInt64()
		}
	}
	return attrs
}

func TestTransferCountsTheBytesReadAndTheFilesDone(t *testing.T) {
	attrs := recordedAttributes(t, func() {
		_, span := otel.Tracer("test").Start(t.Context(), "oci.pull_layers")
		transfer := NewTransfer(span, "Pulled", 2, 11)
		read, err := io.ReadAll(transfer.Reader(strings.NewReader("hello")))
		require.NoError(t, err)
		require.Equal(t, "hello", string(read))
		transfer.FileDone()
		_, err = io.ReadAll(transfer.Reader(strings.NewReader("world!")))
		require.NoError(t, err)
		transfer.FileDone()
		require.EqualValues(t, 2, transfer.FilesDone())
		require.EqualValues(t, 11, transfer.BytesDone())
		span.End()
	})

	require.Equal(t, map[string]int64{
		TransferFilesAttribute:     2,
		TransferBytesAttribute:     11,
		TransferFilesDoneAttribute: 2,
		TransferBytesDoneAttribute: 11,
	}, attrs)
}

// roundTripFunc is an http.RoundTripper that reads the request body, the
// way a registry does, and answers nothing.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestTransferTransportCountsEveryRequestBodySent(t *testing.T) {
	_, span := otel.Tracer("test").Start(t.Context(), "oci.push_layers")
	transfer := NewTransfer(span, "Uploaded", 1, 10)
	var sent []byte
	transport := transfer.Transport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			sent = append(sent, body...)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
	}))

	req, err := http.NewRequest(http.MethodPut, "https://registry.test/v2/repo/blobs/uploads/1", bytes.NewReader([]byte("0123456789")))
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)
	head, err := http.NewRequest(http.MethodHead, "https://registry.test/v2/repo/manifests/latest", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(head)
	require.NoError(t, err)

	require.Equal(t, "0123456789", string(sent))
	require.EqualValues(t, 10, transfer.BytesDone())
}
