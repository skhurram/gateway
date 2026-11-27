package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"gateway/internal/balancer"
	"gateway/internal/breaker"
	"gateway/internal/ratelimit"
	"gateway/pkg/config"
)

type route struct {
	prefix    string
	handler   http.Handler
	upstreams []*balancer.Upstream
}

// Router dispatches requests to the route with the longest matching path
// prefix and responds 404 when none match.
type Router struct {
	routes    []route // sorted by prefix length, longest first
	upstreams []*balancer.Upstream
	metrics   *metrics
}

// RouterOption configures NewRouter.
type RouterOption func(*routerConfig)

type routerConfig struct {
	meterProvider metric.MeterProvider
	breakerNow    func() time.Time
}

// WithMeterProvider sets where request metrics are recorded. The default is
// the global OpenTelemetry meter provider.
func WithMeterProvider(mp metric.MeterProvider) RouterOption {
	return func(c *routerConfig) { c.meterProvider = mp }
}

// WithBreakerClock injects the clock used by all upstream circuit breakers.
func WithBreakerClock(now func() time.Time) RouterOption {
	return func(c *routerConfig) { c.breakerNow = now }
}

// NewRouter builds a router from cfg. Each route gets a round-robin balancer
// over its upstreams and, if configured, a per-client-IP rate limiter.
// Request count, latency, rate-limited count and healthy-upstream gauges are
// recorded per route.
func NewRouter(cfg *config.Config, rt http.RoundTripper, opts ...RouterOption) (*Router, error) {
	rc := routerConfig{meterProvider: otel.GetMeterProvider()}
	for _, o := range opts {
		o(&rc)
	}
	meter := rc.meterProvider.Meter("gateway/proxy")
	m, err := newMetrics(meter)
	if err != nil {
		return nil, err
	}

	r := &Router{metrics: m}
	breakerCfg := cfg.EffectiveCircuitBreaker()
	for _, rcfg := range cfg.Routes {
		prefix := config.NormalizePrefix(rcfg.Prefix)
		ups := make([]*balancer.Upstream, 0, len(rcfg.Upstreams))
		for _, raw := range rcfg.Upstreams {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("route %q: %w", prefix, err)
			}
			up := balancer.NewUpstream(u)
			cb, err := breaker.New(breaker.Config{
				FailureThreshold: breakerCfg.FailureThreshold,
				OpenTimeout:      breakerCfg.OpenTimeout,
				HalfOpenRequests: breakerCfg.HalfOpenRequests,
			}, rc.breakerNow)
			if err != nil {
				return nil, fmt.Errorf("route %q: create circuit breaker: %w", prefix, err)
			}
			up.SetBreaker(cb)
			ups = append(ups, up)
		}
		h := NewBalanced(balancer.NewRoundRobin(ups), rt)
		if rl := rcfg.RateLimit; rl != nil {
			limiter := ratelimit.New(rl.RequestsPerSecond, rl.Burst, nil)
			h = ratelimit.Middleware(limiter, ratelimit.WithOnLimited(func(req *http.Request) {
				m.rateLimited(req.Context(), prefix)
			}))(h)
		}
		r.routes = append(r.routes, route{prefix: prefix, handler: m.instrument(prefix, h), upstreams: ups})
		r.upstreams = append(r.upstreams, ups...)
	}
	sort.SliceStable(r.routes, func(i, j int) bool {
		return len(r.routes[i].prefix) > len(r.routes[j].prefix)
	})

	return r, nil
}

// ObserveHealthy records the healthy upstream count for each route.
func (r *Router) ObserveHealthy(o metric.Int64Observer) {
	for _, rte := range r.routes {
		n := 0
		for _, u := range rte.upstreams {
			if u.Healthy() {
				n++
			}
		}
		o.Observe(int64(n), metric.WithAttributes(attribute.String(routeAttr, rte.prefix)))
	}
}

// ObserveBreakerStates records one gauge point for each upstream's current
// passive circuit breaker state.
func (r *Router) ObserveBreakerStates(o metric.Int64Observer) {
	for _, rte := range r.routes {
		for _, u := range rte.upstreams {
			state := u.BreakerState()
			o.Observe(1, metric.WithAttributes(
				attribute.String(routeAttr, rte.prefix),
				attribute.String(upstreamAttr, u.URL.String()),
				attribute.String(stateAttr, string(state)),
			))
		}
	}
}

// Upstreams returns every upstream across all routes, for health checking.
func (r *Router) Upstreams() []*balancer.Upstream { return r.upstreams }

// Ready reports whether at least one upstream can take a request now: it is
// healthy and its breaker would admit one (see Upstream.Available).
func (r *Router) Ready() bool {
	for _, u := range r.upstreams {
		if u.Available() {
			return true
		}
	}
	return false
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Route on the cleaned path and forward that same path, so "/a/../b"
	// cannot be matched as /a but resolved as /b by the upstream.
	if p := cleanPath(req.URL.Path); p != req.URL.Path {
		req = req.Clone(req.Context())
		req.URL.Path = p
		req.URL.RawPath = ""
	}
	for _, rt := range r.routes {
		if matchPrefix(rt.prefix, req.URL.Path) {
			trace.SpanFromContext(req.Context()).SetAttributes(attribute.String(routeAttr, rt.prefix))
			rt.handler.ServeHTTP(w, req)
			return
		}
	}
	r.metrics.instrument(unmatchedRoute, http.NotFoundHandler()).ServeHTTP(w, req)
}

// matchPrefix reports whether p falls under prefix on a path-segment
// boundary: "/api" matches "/api" and "/api/x" but not "/apiary".
func matchPrefix(prefix, p string) bool {
	if prefix == "/" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	c := path.Clean("/" + p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}
