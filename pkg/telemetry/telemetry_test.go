package telemetry

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// clearTelemetryEnv unsets every variable setup reads, so a test sees only the
// ones it sets itself rather than whatever the developer's shell exports.
func clearTelemetryEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
		"OTEL_METRIC_EXPORT_INTERVAL", "OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES",
	} {
		t.Setenv(name, "")
	}
}

func TestSetupDoesNothingWhenNoExporterIsConfigured(t *testing.T) {
	clearTelemetryEnv(t)
	var console bytes.Buffer

	shutdown, err := setup(context.Background(), &console)

	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
	require.Empty(t, console.String())
}

func TestExporterDefaultsToOTLPOnlyWhenAnEndpointIsConfigured(t *testing.T) {
	clearTelemetryEnv(t)
	require.Equal(t, "none", exporterFor("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	require.Equal(t, "otlp", exporterFor("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	require.Equal(t, "otlp", exporterFor("OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"))
}

func TestExporterForASignalFollowsItsOwnEndpointAndVariable(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://teley.dev/r/room")

	require.Equal(t, "otlp", exporterFor("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	require.Equal(t, "none", exporterFor("OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"))

	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	require.Equal(t, "console", exporterFor("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
}

func TestOTLPProtocolPrefersTheSignalSpecificVariable(t *testing.T) {
	clearTelemetryEnv(t)
	require.Equal(t, "http/protobuf", otlpProtocol("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"))

	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	require.Equal(t, "grpc", otlpProtocol("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"))

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	require.Equal(t, "http/protobuf", otlpProtocol("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"))
	require.Equal(t, "grpc", otlpProtocol("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"))
}

func TestMetricIntervalReadsMillisecondsFromTheEnvironment(t *testing.T) {
	clearTelemetryEnv(t)
	require.Equal(t, defaultMetricInterval, metricInterval())

	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "2500")
	require.Equal(t, 2500*time.Millisecond, metricInterval())

	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "soon")
	require.Equal(t, defaultMetricInterval, metricInterval())
}

func TestSetupRejectsAnUnknownExporter(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "zipkin")

	_, err := setup(context.Background(), &bytes.Buffer{})

	require.ErrorContains(t, err, `OTEL_TRACES_EXPORTER "zipkin"`)
}

func TestSetupRejectsAnUnsupportedOTLPProtocol(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")

	_, err := setup(context.Background(), &bytes.Buffer{})

	require.ErrorContains(t, err, `"http/json"`)
}

func TestSetupExportsSpansToTheConsoleAsTheSalService(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	var console bytes.Buffer
	shutdown, err := setup(context.Background(), &console)
	require.NoError(t, err)

	_, span := Start(context.Background(), "test.step", attribute.String("sal.test", "value"))
	End(span, nil)
	require.NoError(t, shutdown(context.Background()))

	output := console.String()
	require.Contains(t, output, `"Name": "test.step"`)
	require.Contains(t, output, `"Key": "sal.test"`)
	require.Contains(t, output, `"Key": "service.name"`)
	require.Contains(t, output, `"Value": "sal"`)
}

func TestSetupExportsRuntimeAndHostMetricsToTheConsole(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("OTEL_METRICS_EXPORTER", "console")
	var console bytes.Buffer
	shutdown, err := setup(context.Background(), &console)
	require.NoError(t, err)

	// shutting the reader down collects and exports one last time, which is
	// also how a short lived sal invocation gets its metrics out
	require.NoError(t, shutdown(context.Background()))

	output := console.String()
	require.Contains(t, output, "go.memory.used")
	require.Contains(t, output, "process.cpu.time")
	require.Contains(t, output, "system.memory.usage")
}

func TestEndRecordsAFailureAsTheSpanStatus(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := provider.Tracer("test").Start(context.Background(), "failing")

	End(span, errors.New("docker build failed"))

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Error, ended[0].Status().Code)
	require.Equal(t, "docker build failed", ended[0].Status().Description)
	require.Len(t, ended[0].Events(), 1)
	require.Equal(t, "exception", ended[0].Events()[0].Name)
}

func TestEndLeavesASuccessfulSpanUnset(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := provider.Tracer("test").Start(context.Background(), "fine")

	End(span, nil)

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Unset, ended[0].Status().Code)
	require.Empty(t, ended[0].Events())
}
