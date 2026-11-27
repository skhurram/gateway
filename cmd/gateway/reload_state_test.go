package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gateway/internal/proxy"
	"gateway/pkg/config"
)

// failingUpstream passes health checks but answers 500 on every other path,
// like an upstream that is up but broken during an incident.
func failingUpstream(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	return s
}

func stateConfig(upstream string, failureThreshold, rps, burst int, extraRoute bool) string {
	extra := ""
	if extraRoute {
		extra = fmt.Sprintf("  - prefix: /other\n    upstreams: [%q]\n", upstream)
	}
	return fmt.Sprintf(`
listen: ":8080"
health: {path: /health, interval: 1h, timeout: 1s}
circuit_breaker: {failure_threshold: %d, open_timeout: 1h, half_open_requests: 1}
routes:
  - prefix: /api
    upstreams: [%q]
    rate_limit: {requests_per_second: %d, burst: %d}
%s`, failureThreshold, upstream, rps, burst, extra)
}

func startManager(t *testing.T, body string) (*runtimeManager, string) {
	t.Helper()
	cfg, err := config.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := newRuntimeManager(ctx, proxy.NewTransport())
	if err := manager.initialize(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		manager.wait()
	})
	return manager, filepath.Join(t.TempDir(), "gateway.yaml")
}

func reloadWith(t *testing.T, manager *runtimeManager, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.reload(path); err != nil {
		t.Fatal(err)
	}
}

func status(manager *runtimeManager, path string) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://gw"+path, nil)
	req.RemoteAddr = "192.0.2.1:1234"
	manager.serveHTTP(rec, req)
	return rec.Code
}

// A reload during an incident must not send traffic back to an upstream
// whose breaker is open, when nothing about that upstream changed (B4).
func TestReloadKeepsOpenBreakerForUnchangedUpstream(t *testing.T) {
	var hits atomic.Int32
	up := failingUpstream(t, &hits)
	manager, path := startManager(t, stateConfig(up.URL, 1, 1000, 1000, false))

	if got := status(manager, "/api/x"); got != http.StatusInternalServerError {
		t.Fatalf("first request: %d, want 500 from the upstream", got)
	}
	if got := status(manager, "/api/x"); got != http.StatusServiceUnavailable {
		t.Fatalf("second request: %d, want 503 from the open breaker", got)
	}

	// An unrelated change, such as adding a route, reloads the config.
	reloadWith(t, manager, path, stateConfig(up.URL, 1, 1000, 1000, true))
	if got := status(manager, "/api/x"); got != http.StatusServiceUnavailable {
		t.Fatalf("after reload: %d, want 503; the reload re-admitted the failing upstream", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("upstream received %d requests, want 1", n)
	}
}

// Changing the breaker settings starts a fresh breaker (decision 004).
func TestReloadResetsBreakerWhenItsSettingsChange(t *testing.T) {
	var hits atomic.Int32
	up := failingUpstream(t, &hits)
	manager, path := startManager(t, stateConfig(up.URL, 1, 1000, 1000, false))
	status(manager, "/api/x") // opens the breaker

	reloadWith(t, manager, path, stateConfig(up.URL, 2, 1000, 1000, false))
	if got := status(manager, "/api/x"); got != http.StatusInternalServerError {
		t.Fatalf("after reload with new breaker settings: %d, want 500 (fresh breaker)", got)
	}
}

// A reload must not hand every client a fresh rate-limit bucket.
func TestReloadKeepsRateLimitBuckets(t *testing.T) {
	up := healthyUpstream(t)
	manager, path := startManager(t, stateConfig(up.URL, 5, 1, 1, false))
	if got := status(manager, "/api/x"); got != http.StatusOK {
		t.Fatalf("first request: %d, want 200", got)
	}

	reloadWith(t, manager, path, stateConfig(up.URL, 5, 1, 1, true))
	if got := status(manager, "/api/x"); got != http.StatusTooManyRequests {
		t.Fatalf("after reload: %d, want 429; the reload refilled the client's bucket", got)
	}

	// New limits start fresh buckets.
	reloadWith(t, manager, path, stateConfig(up.URL, 5, 2, 2, true))
	if got := status(manager, "/api/x"); got != http.StatusOK {
		t.Fatalf("after reload with new limits: %d, want 200 (fresh bucket)", got)
	}
}
