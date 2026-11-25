// Package telemetry wires up OpenTelemetry tracing and metrics export.
package telemetry

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// endpointEnvVars are the standard variables that enable OTLP export.
var endpointEnvVars = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
}

// Setup installs the W3C trace-context propagator and, when an OTLP endpoint
// is configured through the standard OTEL_EXPORTER_OTLP_* environment
// variables, global tracer and meter providers that export over OTLP/HTTP.
// Without an endpoint instrumentation stays a no-op, so local runs and tests
// need no collector.
//
// The returned shutdown function flushes and stops the exporters.
func Setup(ctx context.Context, service string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if !exportEnabled() {
		return func(context.Context) error { return nil }, nil
	}

	// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override the default.
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExp), sdktrace.WithResource(res))

	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	// The export interval comes from OTEL_METRIC_EXPORT_INTERVAL (default 60s).
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

func exportEnabled() bool {
	for _, k := range endpointEnvVars {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}
