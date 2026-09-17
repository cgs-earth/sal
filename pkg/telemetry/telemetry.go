// Package telemetry configures OpenTelemetry for the sal process and is where
// the rest of sal starts the spans it records around its long running steps:
// building and running SAL modules, validating RDF, diffing and loading the
// Iceberg table, and answering queries.
//
// Nothing is exported unless the environment asks for it with the standard
// OpenTelemetry variables, so a plain `sal build` pays only for no-op spans.
// Setting OTEL_EXPORTER_OTLP_ENDPOINT (or a signal specific
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT / OTEL_EXPORTER_OTLP_METRICS_ENDPOINT)
// turns on OTLP export of traces and metrics; OTEL_TRACES_EXPORTER and
// OTEL_METRICS_EXPORTER pick an exporter explicitly, one of `otlp`, `console`
// (pretty printed to stderr), or `none`. OTEL_EXPORTER_OTLP_PROTOCOL selects
// `http/protobuf` (the default) or `grpc`.
//
// Along with the traces sal records itself, the metrics side reports the Go
// runtime's memory and goroutine figures and the process and system CPU and
// memory usage, so a slow build can be read against what the machine was doing.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is the instrumentation scope every span sal records is attributed to.
const scopeName = "github.com/cgs-earth/sal"

// serviceName is what sal reports itself as; OTEL_SERVICE_NAME overrides it.
const serviceName = "sal"

// defaultMetricInterval is how often metrics are exported when
// OTEL_METRIC_EXPORT_INTERVAL does not say. The SDK's own default of a minute is
// longer than most sal invocations run, which would leave a build with a single
// data point taken as the process exits.
const defaultMetricInterval = 10 * time.Second

// shutdownTimeout bounds how long the process waits at exit for the last spans
// and metrics to be delivered, so an unreachable collector cannot hang a command.
const shutdownTimeout = 5 * time.Second

// Setup installs the OpenTelemetry SDK when the environment configures an
// exporter and returns the function that flushes and stops it, which must run
// before the process exits or the last spans and metrics are lost. With no
// exporter configured, the global providers are left as no-ops and the returned
// function does nothing.
func Setup(ctx context.Context) (func(context.Context) error, error) {
	return setup(ctx, os.Stderr)
}

// setup is Setup with the console exporters' destination injectable for tests.
func setup(ctx context.Context, console io.Writer) (func(context.Context) error, error) {
	tracesExporter := exporterFor("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	metricsExporter := exporterFor("OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	if tracesExporter == "none" && metricsExporter == "none" {
		return func(context.Context) error { return nil }, nil
	}

	// resource attributes given later take precedence, so the environment can
	// rename the service or add its own attributes
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName), semconv.ServiceVersion(version())),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: describe the sal process: %w", err)
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	// export failures are reported the way every other warning in sal is
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("telemetry export failed", "error", err)
	}))

	var shutdowns []func(context.Context) error
	if tracesExporter != "none" {
		exporter, err := newSpanExporter(ctx, tracesExporter, console)
		if err != nil {
			return nil, err
		}
		provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
		otel.SetTracerProvider(provider)
		shutdowns = append(shutdowns, provider.Shutdown)
	}
	if metricsExporter != "none" {
		exporter, err := newMetricExporter(ctx, metricsExporter, console)
		if err != nil {
			return nil, err
		}
		reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(metricInterval()))
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
		otel.SetMeterProvider(provider)
		shutdowns = append(shutdowns, provider.Shutdown)
		// the Go runtime's memory and goroutine figures, and the process and
		// system CPU and memory usage, are what make a slow step explicable
		if err := runtime.Start(runtime.WithMeterProvider(provider)); err != nil {
			return nil, fmt.Errorf("telemetry: start runtime metrics: %w", err)
		}
		if err := host.Start(host.WithMeterProvider(provider)); err != nil {
			return nil, fmt.Errorf("telemetry: start host metrics: %w", err)
		}
	}

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
		var errs []error
		for _, shutdown := range shutdowns {
			errs = append(errs, shutdown(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

// exporterFor reads which exporter a signal uses from its OTEL_*_EXPORTER
// variable. Left unset, OTLP is used when an OTLP endpoint is configured for
// the signal or for everything, and nothing is exported otherwise.
func exporterFor(exporterVar string, endpointVar string) string {
	if name := os.Getenv(exporterVar); name != "" {
		return name
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv(endpointVar) != "" {
		return "otlp"
	}
	return "none"
}

// otlpProtocol reads the OTLP transport for a signal, the signal specific
// variable first and the shared one after it, defaulting to http/protobuf as the
// OpenTelemetry specification does.
func otlpProtocol(signalVar string) string {
	if protocol := os.Getenv(signalVar); protocol != "" {
		return protocol
	}
	if protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"); protocol != "" {
		return protocol
	}
	return "http/protobuf"
}

func newSpanExporter(ctx context.Context, name string, console io.Writer) (sdktrace.SpanExporter, error) {
	switch name {
	case "otlp":
		switch protocol := otlpProtocol("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"); protocol {
		case "grpc":
			return otlptracegrpc.New(ctx)
		case "http/protobuf":
			return otlptracehttp.New(ctx)
		default:
			return nil, fmt.Errorf("telemetry: unsupported OTLP protocol %q for traces; use http/protobuf or grpc", protocol)
		}
	case "console":
		return stdouttrace.New(stdouttrace.WithWriter(console), stdouttrace.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("telemetry: unsupported OTEL_TRACES_EXPORTER %q; use otlp, console, or none", name)
	}
}

func newMetricExporter(ctx context.Context, name string, console io.Writer) (sdkmetric.Exporter, error) {
	switch name {
	case "otlp":
		switch protocol := otlpProtocol("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"); protocol {
		case "grpc":
			return otlpmetricgrpc.New(ctx)
		case "http/protobuf":
			return otlpmetrichttp.New(ctx)
		default:
			return nil, fmt.Errorf("telemetry: unsupported OTLP protocol %q for metrics; use http/protobuf or grpc", protocol)
		}
	case "console":
		return stdoutmetric.New(stdoutmetric.WithWriter(console), stdoutmetric.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("telemetry: unsupported OTEL_METRICS_EXPORTER %q; use otlp, console, or none", name)
	}
}

// metricInterval is how often metrics are exported: OTEL_METRIC_EXPORT_INTERVAL
// in milliseconds when it is set and valid, defaultMetricInterval otherwise.
func metricInterval() time.Duration {
	if value := os.Getenv("OTEL_METRIC_EXPORT_INTERVAL"); value != "" {
		if millis, err := strconv.Atoi(value); err == nil && millis > 0 {
			return time.Duration(millis) * time.Millisecond
		}
		slog.Warn("ignoring an invalid OTEL_METRIC_EXPORT_INTERVAL", "value", value)
	}
	return defaultMetricInterval
}

// version is the module version sal was built at, which is "(devel)" for a
// build from a checkout.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

// Tracer is the tracer every span sal records comes from. It reads the global
// provider on each call, so a span started before Setup ran is a no-op and one
// started after it is exported.
func Tracer() trace.Tracer {
	return otel.Tracer(scopeName)
}

// Start begins a span named name under ctx with the given attributes and
// returns the context the span's children should be started from.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// End ends a span, recording err as its status when the step it covers failed.
// It is meant to be deferred with the function's named error result:
//
//	ctx, span := telemetry.Start(ctx, "step")
//	defer func() { telemetry.End(span, err) }()
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
