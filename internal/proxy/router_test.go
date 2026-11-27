package proxy_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gateway/internal/proxy"
	"gateway/pkg/config"
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

func TestRouterBreakerStopsTrafficThenProbesRecovery(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusServiceUnavailable)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer upstream.Close()

	now := time.Unix(1_700_000_000, 0)
	cfg := &config.Config{
		CircuitBreaker: config.CircuitBreaker{
			FailureThreshold: 1,
			OpenTimeout:      time.Minute,
			HalfOpenRequests: 1,
		},
		Routes: []config.Route{{Prefix: "/", Upstreams: []string{upstream.URL}}},
	}
	r, err := proxy.NewRouter(cfg, nil, proxy.WithBreakerClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}

	if code, _ := get(t, r, "/"); code != http.StatusServiceUnavailable {
		t.Fatalf("first failure status = %d, want 503", code)
	}
	if code, _ := get(t, r, "/"); code != http.StatusServiceUnavailable {
		t.Fatalf("open circuit status = %d, want 503", code)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream saw %d requests while open, want 1", got)
	}

	now = now.Add(time.Minute)
	status.Store(http.StatusOK)
	if code, _ := get(t, r, "/"); code != http.StatusOK {
		t.Fatalf("trial status = %d, want 200", code)
	}
	if code, _ := get(t, r, "/"); code != http.StatusOK {
		t.Fatalf("post-recovery status = %d, want 200", code)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("upstream saw %d requests after recovery, want 3", got)
	}
}

func TestRouterBreakerDoesNotCountClientErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		CircuitBreaker: config.CircuitBreaker{
			FailureThreshold: 1,
			OpenTimeout:      time.Minute,
			HalfOpenRequests: 1,
		},
		Routes: []config.Route{{Prefix: "/", Upstreams: []string{upstream.URL}}},
	}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if code, _ := get(t, r, "/"); code != http.StatusBadRequest {
			t.Fatalf("4xx response opened breaker: %d", code)
		}
	}
}

type errorTransport struct {
	err   error
	calls atomic.Int64
}

func (t *errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, t.err
}

func TestRouterBreakerCountsTransportErrors(t *testing.T) {
	for _, err := range []error{errors.New("connection refused"), contextDeadlineExceeded{}} {
		t.Run(err.Error(), func(t *testing.T) {
			rt := &errorTransport{err: err}
			cfg := &config.Config{
				CircuitBreaker: config.CircuitBreaker{
					FailureThreshold: 1,
					OpenTimeout:      time.Minute,
					HalfOpenRequests: 1,
				},
				Routes: []config.Route{{Prefix: "/", Upstreams: []string{"http://unreachable.invalid"}}},
			}
			r, err := proxy.NewRouter(cfg, rt)
			if err != nil {
				t.Fatal(err)
			}
			if code, _ := get(t, r, "/"); code != http.StatusBadGateway {
				t.Fatalf("transport error status = %d, want 502", code)
			}
			if code, _ := get(t, r, "/"); code != http.StatusServiceUnavailable {
				t.Fatalf("open circuit status = %d, want 503", code)
			}
			if got := rt.calls.Load(); got != 1 {
				t.Errorf("transport calls = %d, want 1", got)
			}
		})
	}
}

type contextDeadlineExceeded struct{}

func (contextDeadlineExceeded) Error() string   { return "context deadline exceeded" }
func (contextDeadlineExceeded) Timeout() bool   { return true }
func (contextDeadlineExceeded) Temporary() bool { return true }
