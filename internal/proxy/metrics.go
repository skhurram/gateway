package proxy

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	routeAttr    = "http.route"
	statusAttr   = "http.response.status_code"
	upstreamAttr = "upstream"
	stateAttr    = "circuit_breaker.state"

	unmatchedRoute = "unmatched"
)

// metrics holds the gateway's OpenTelemetry instruments.
type metrics struct {
	requests metric.Int64Counter
	duration metric.Float64Histogram
	limited  metric.Int64Counter
}

func newMetrics(m metric.Meter) (*metrics, error) {
	requests, err := m.Int64Counter("gateway.requests",
		metric.WithDescription("Requests handled, by route and response status."),
		metric.WithUnit("{request}"))
	if err != nil {
		return nil, err
	}
	duration, err := m.Float64Histogram("gateway.request.duration",
		metric.WithDescription("Time to serve a request, by route and response status."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10))
	if err != nil {
		return nil, err
	}
	limited, err := m.Int64Counter("gateway.ratelimited",
		metric.WithDescription("Requests rejected by the rate limiter, by route."),
		metric.WithUnit("{request}"))
	if err != nil {
		return nil, err
	}
	return &metrics{requests: requests, duration: duration, limited: limited}, nil
}

// instrument records request count and latency for route.
func (m *metrics) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		m.record(r.Context(), route, rec.statusCode(), time.Since(start))
	})
}

func (m *metrics) record(ctx context.Context, route string, status int, d time.Duration) {
	attrs := metric.WithAttributes(attribute.String(routeAttr, route), attribute.Int(statusAttr, status))
	m.requests.Add(ctx, 1, attrs)
	m.duration.Record(ctx, d.Seconds(), attrs)
}

func (m *metrics) rateLimited(ctx context.Context, route string) {
	m.limited.Add(ctx, 1, metric.WithAttributes(attribute.String(routeAttr, route)))
}

// RegisterHealthyUpstreamGauge registers the gateway-wide health gauge once.
// current must read the active router so config reloads do not create duplicate
// observable instruments.
func RegisterHealthyUpstreamGauge(current func() *Router) error {
	return RegisterHealthyUpstreamGaugeWithProvider(otel.GetMeterProvider(), current)
}

// RegisterHealthyUpstreamGaugeWithProvider is RegisterHealthyUpstreamGauge with
// an explicit meter provider for tests and alternate exporters.
func RegisterHealthyUpstreamGaugeWithProvider(mp metric.MeterProvider, current func() *Router) error {
	meter := mp.Meter("gateway/proxy")
	_, err := meter.Int64ObservableGauge("gateway.upstreams.healthy",
		metric.WithDescription("Upstreams currently in rotation, by route."),
		metric.WithUnit("{upstream}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if r := current(); r != nil {
				r.ObserveHealthy(o)
			}
			return nil
		}))
	return err
}

// RegisterBreakerStateGauge registers the gateway-wide breaker-state gauge
// once. current must read the active router across config reloads.
func RegisterBreakerStateGauge(current func() *Router) error {
	return RegisterBreakerStateGaugeWithProvider(otel.GetMeterProvider(), current)
}

// RegisterBreakerStateGaugeWithProvider is RegisterBreakerStateGauge with an
// explicit meter provider for tests and alternate exporters.
func RegisterBreakerStateGaugeWithProvider(mp metric.MeterProvider, current func() *Router) error {
	meter := mp.Meter("gateway/proxy")
	_, err := meter.Int64ObservableGauge("gateway.upstreams.breaker",
		metric.WithDescription("Upstream circuit breaker state, by route and upstream."),
		metric.WithUnit("{upstream}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if r := current(); r != nil {
				r.ObserveBreakerStates(o)
			}
			return nil
		}))
	return err
}

// statusRecorder remembers the response status. Unwrap lets
// http.ResponseController (used by ReverseProxy for flushing) reach the
// underlying writer.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) statusCode() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}
