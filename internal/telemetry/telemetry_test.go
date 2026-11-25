package telemetry_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"gateway/internal/telemetry"
)

func clearEndpointEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	} {
		t.Setenv(k, "")
	}
}

func TestSetupWithoutEndpointIsNoOpButPropagates(t *testing.T) {
	clearEndpointEnv(t)

	shutdown, err := telemetry.Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The W3C propagator must be installed so trace context passes through.
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{2},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	h := http.Header{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
	if h.Get("traceparent") == "" {
		t.Error("traceparent header was not injected")
	}
}

func TestSetupWithEndpointStartsAndStops(t *testing.T) {
	clearEndpointEnv(t)
	// Exporters connect lazily, so an unreachable endpoint must not fail Setup.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")

	shutdown, err := telemetry.Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
	})
	// Shutdown may report the failed flush to the unreachable endpoint; it
	// must return rather than hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shutdown(ctx)
}
