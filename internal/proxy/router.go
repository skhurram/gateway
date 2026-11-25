package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"gateway/internal/balancer"
	"gateway/internal/config"
	"gateway/internal/ratelimit"
)

type route struct {
	prefix  string
	handler http.Handler
}

// Router dispatches requests to the route with the longest matching path
// prefix and responds 404 when none match.
type Router struct {
	routes    []route // sorted by prefix length, longest first
	upstreams []*balancer.Upstream
}

// NewRouter builds a router from cfg. Each route gets a round-robin balancer
// over its upstreams and, if configured, a per-client-IP rate limiter.
func NewRouter(cfg *config.Config, rt http.RoundTripper) (*Router, error) {
	r := &Router{}
	for _, rc := range cfg.Routes {
		ups := make([]*balancer.Upstream, 0, len(rc.Upstreams))
		for _, raw := range rc.Upstreams {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("route %q: %w", rc.Prefix, err)
			}
			ups = append(ups, balancer.NewUpstream(u))
		}
		var h http.Handler = NewBalanced(balancer.NewRoundRobin(ups), rt)
		if rl := rc.RateLimit; rl != nil {
			h = ratelimit.Middleware(ratelimit.New(rl.RequestsPerSecond, rl.Burst, nil))(h)
		}
		r.routes = append(r.routes, route{prefix: config.NormalizePrefix(rc.Prefix), handler: h})
		r.upstreams = append(r.upstreams, ups...)
	}
	sort.SliceStable(r.routes, func(i, j int) bool {
		return len(r.routes[i].prefix) > len(r.routes[j].prefix)
	})
	return r, nil
}

// Upstreams returns every upstream across all routes, for health checking.
func (r *Router) Upstreams() []*balancer.Upstream { return r.upstreams }

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
			rt.handler.ServeHTTP(w, req)
			return
		}
	}
	http.NotFound(w, req)
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
