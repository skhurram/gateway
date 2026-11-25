package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"gateway/internal/balancer"
	"gateway/internal/config"
	"gateway/internal/ratelimit"
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
}

// WithMeterProvider sets where request metrics are recorded. The default is
// the global OpenTelemetry meter provider.
func WithMeterProvider(mp metric.MeterProvider) RouterOption {
	return func(c *routerConfig) { c.meterProvider = mp }
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
	for _, rcfg := range cfg.Routes {
		prefix := config.NormalizePrefix(rcfg.Prefix)
		ups := make([]*balancer.Upstream, 0, len(rcfg.Upstreams))
		for _, raw := range rcfg.Upstreams {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("route %q: %w", prefix, err)
			}
			ups = append(ups, balancer.NewUpstream(u))
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

	_, err = meter.Int64ObservableGauge("gateway.upstreams.healthy",
		metric.WithDescription("Upstreams currently in rotation, by route."),
		metric.WithUnit("{upstream}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, rte := range r.routes {
				n := 0
				for _, u := range rte.upstreams {
					if u.Healthy() {
						n++
					}
				}
				o.Observe(int64(n), metric.WithAttributes(attribute.String(routeAttr, rte.prefix)))
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Upstreams returns every upstream across all routes, for health checking.
func (r *Router) Upstreams() []*balancer.Upstream { return r.upstreams }

// Ready reports whether at least one upstream is healthy.
func (r *Router) Ready() bool {
	for _, u := range r.upstreams {
		if u.Healthy() {
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
