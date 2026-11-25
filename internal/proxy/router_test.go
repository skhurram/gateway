package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gateway/internal/config"
	"gateway/internal/proxy"
)

// namedUpstream replies with "<name> <path>" so tests can see both which
// upstream served a request and what path it received.
func namedUpstream(t *testing.T, name string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, name+" "+r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://gw"+path, nil)
	h.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func TestRouterLongestPrefixWins(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Prefix: "/", Upstreams: []string{namedUpstream(t, "root").URL}},
		{Prefix: "/api", Upstreams: []string{namedUpstream(t, "api").URL}},
		{Prefix: "/api/users", Upstreams: []string{namedUpstream(t, "users").URL}},
	}}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, path, want string
	}{
		{"deepest prefix", "/api/users/42", "users /api/users/42"},
		{"exact deepest prefix", "/api/users", "users /api/users"},
		{"middle prefix", "/api/orders", "api /api/orders"},
		{"exact middle prefix", "/api", "api /api"},
		{"segment boundary, not string prefix", "/apiary", "root /apiary"},
		{"sibling of nested prefix", "/api/usersX", "api /api/usersX"},
		{"root fallback", "/static/app.js", "root /static/app.js"},
		{"root itself", "/", "root /"},
		{"traversal is resolved before routing", "/api/users/../orders", "api /api/orders"},
		{"traversal out of prefix", "/api/../x", "root /x"},
		{"trailing slash kept", "/api/users/", "users /api/users/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := get(t, r, tt.path)
			if code != http.StatusOK || body != tt.want {
				t.Errorf("GET %s = %d %q, want 200 %q", tt.path, code, body, tt.want)
			}
		})
	}
}

func TestRouterRegistrationOrderDoesNotMatter(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Prefix: "/api/users", Upstreams: []string{namedUpstream(t, "users").URL}},
		{Prefix: "/api", Upstreams: []string{namedUpstream(t, "api").URL}},
	}}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, r, "/api/users/1"); body != "users /api/users/1" {
		t.Errorf("body = %q", body)
	}
}

func TestRouterNoMatchReturns404(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Prefix: "/api", Upstreams: []string{namedUpstream(t, "api").URL}},
	}}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/other", "/apiary", "/ap"} {
		if code, _ := get(t, r, p); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, code)
		}
	}
}

func TestRouterBalancesAcrossUpstreams(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{{
		Prefix:    "/",
		Upstreams: []string{namedUpstream(t, "a").URL, namedUpstream(t, "b").URL},
	}}}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for range 10 {
		_, body := get(t, r, "/x")
		seen[strings.Fields(body)[0]]++
	}
	if seen["a"] != 5 || seen["b"] != 5 {
		t.Errorf("distribution = %v, want 5/5", seen)
	}
}

func TestRouterReturns503WhenNoHealthyUpstream(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Prefix: "/", Upstreams: []string{namedUpstream(t, "a").URL}},
	}}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range r.Upstreams() {
		u.SetHealthy(false)
	}
	if code, _ := get(t, r, "/"); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
}

func TestRouterRateLimitsPerRoute(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Prefix: "/limited", Upstreams: []string{namedUpstream(t, "l").URL}, RateLimit: &config.RateLimit{RequestsPerSecond: 0.001, Burst: 2}},
		{Prefix: "/open", Upstreams: []string{namedUpstream(t, "o").URL}},
	}}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var codes []int
	for range 3 {
		c, _ := get(t, r, "/limited")
		codes = append(codes, c)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Errorf("limited route codes = %v, want [200 200 429]", codes)
	}
	for range 5 {
		if c, _ := get(t, r, "/open"); c != 200 {
			t.Fatalf("unlimited route returned %d", c)
		}
	}
}
