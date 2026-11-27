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
	limiter   *ratelimit.Limiter // nil without a rate limit
	limit     config.RateLimit
}

// Router dispatches requests to the route with the longest matching path
// prefix and responds 404 when none match.
type Router struct {
	routes     []route // sorted by prefix length, longest first
	upstreams  []*balancer.Upstream
	metrics    *metrics
	breakerCfg breaker.Config
}

// RouterOption configures NewRouter.
type RouterOption func(*routerConfig)

type routerConfig struct {
	meterProvider metric.MeterProvider
	breakerNow    func() time.Time
	previous      *Router
}

// WithPrevious carries per-upstream and per-route state over from the router
// being replaced by a config reload, where its configuration is unchanged:
//   - an upstream's circuit breaker and health status, when the route prefix,
//     the upstream URL and the circuit breaker settings are the same;
//   - a route's rate-limit buckets, when the prefix and the limits are the same.
//
// Anything whose configuration changed starts fresh.
func WithPrevious(prev *Router) RouterOption {
	return func(c *routerConfig) { c.previous = prev }
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

	effective := cfg.EffectiveCircuitBreaker()
	r := &Router{metrics: m, breakerCfg: breaker.Config{
		FailureThreshold: effective.FailureThreshold,
		OpenTimeout:      effective.OpenTimeout,
		HalfOpenRequests: effective.HalfOpenRequests,
	}}
	for _, rcfg := range cfg.Routes {
		prefix := config.NormalizePrefix(rcfg.Prefix)
		prevRoute := rc.previous.route(prefix)
		claimed := make(map[*balancer.Upstream]bool) // an old upstream carries over once
		ups := make([]*balancer.Upstream, 0, len(rcfg.Upstreams))
		for _, raw := range rcfg.Upstreams {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("route %q: %w", prefix, err)
			}
			up := balancer.NewUpstream(u)
			if old := prevRoute.upstream(u.String(), claimed); old != nil && rc.previous.breakerCfg == r.breakerCfg {
				claimed[old] = true
				up.SetBreaker(old.Breaker())
				up.SetHealthy(old.Healthy())
			} else {
				cb, err := breaker.New(r.breakerCfg, rc.breakerNow)
				if err != nil {
					return nil, fmt.Errorf("route %q: create circuit breaker: %w", prefix, err)
				}
				up.SetBreaker(cb)
			}
			ups = append(ups, up)
		}
		h := NewBalanced(balancer.NewRoundRobin(ups), rt)
		rte := route{prefix: prefix, upstreams: ups}
		if rl := rcfg.RateLimit; rl != nil {
			rte.limit = *rl
			if prevRoute != nil && prevRoute.limiter != nil && prevRoute.limit == *rl {
				rte.limiter = prevRoute.limiter
			} else {
				rte.limiter = ratelimit.New(rl.RequestsPerSecond, rl.Burst, nil)
			}
			h = ratelimit.Middleware(rte.limiter, ratelimit.WithOnLimited(func(req *http.Request) {
				m.rateLimited(req.Context(), prefix)
			}))(h)
		}
		rte.handler = m.instrument(prefix, h)
		r.routes = append(r.routes, rte)
		r.upstreams = append(r.upstreams, ups...)
	}
	sort.SliceStable(r.routes, func(i, j int) bool {
		return len(r.routes[i].prefix) > len(r.routes[j].prefix)
	})

	return r, nil
}

// route returns the route with prefix, or nil. It is nil-safe on r.
func (r *Router) route(prefix string) *route {
	if r == nil {
		return nil
	}
	for i := range r.routes {
		if r.routes[i].prefix == prefix {
			return &r.routes[i]
		}
	}
	return nil
}

// upstream returns the route's first upstream with the given URL that is not
// in claimed, or nil. It is nil-safe on rte.
func (rte *route) upstream(rawURL string, claimed map[*balancer.Upstream]bool) *balancer.Upstream {
	if rte == nil {
		return nil
	}
	for _, u := range rte.upstreams {
		if u.URL.String() == rawURL && !claimed[u] {
			return u
		}
	}
	return nil
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

// Ready reports whether at least one upstream is healthy.
func (r *Router) Ready() bool {
	for _, u := range r.upstreams {
		if u.Healthy() && u.BreakerState() != breaker.Open {
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
