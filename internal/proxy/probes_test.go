package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gateway/internal/breaker"
	"gateway/internal/proxy"
)

func TestProbes(t *testing.T) {
	ready := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "next")
	})
	h := proxy.WithProbes(next, func() bool { return ready })

	tests := []struct {
		name  string
		path  string
		ready bool
		code  int
		body  string
	}{
		{"healthz when not ready", "/healthz", false, http.StatusOK, "ok\n"},
		{"healthz when ready", "/healthz", true, http.StatusOK, "ok\n"},
		{"readyz when ready", "/readyz", true, http.StatusOK, "ready\n"},
		{"readyz when not ready", "/readyz", false, http.StatusServiceUnavailable, "not ready\n"},
		{"other paths pass through", "/api/x", false, http.StatusOK, "next"},
		{"similar path passes through", "/readyz/extra", true, http.StatusOK, "next"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready = tt.ready
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.code || rec.Body.String() != tt.body {
				t.Errorf("GET %s = %d %q, want %d %q", tt.path, rec.Code, rec.Body.String(), tt.code, tt.body)
			}
		})
	}
}

func TestRouterReady(t *testing.T) {
	cfg := twoUpstreamConfig(t)
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ready() {
		t.Fatal("router with healthy upstreams should be ready")
	}

	ups := r.Upstreams()
	ups[0].SetHealthy(false)
	if !r.Ready() {
		t.Error("one healthy upstream is enough to be ready")
	}
	ups[1].SetHealthy(false)
	if r.Ready() {
		t.Error("router with no healthy upstream must not be ready")
	}
}

// A half-open upstream whose trial slots are all taken refuses traffic, so it
// must not make the router ready (B9).
func TestRouterNotReadyWhenHalfOpenTrialsAreTaken(t *testing.T) {
	r, err := proxy.NewRouter(twoUpstreamConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ups := r.Upstreams()
	ups[1].SetHealthy(false)

	now := time.Unix(1_700_000_000, 0)
	b, err := breaker.New(breaker.Config{FailureThreshold: 1, OpenTimeout: time.Minute, HalfOpenRequests: 1},
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ups[0].SetBreaker(b)
	ups[0].RecordFailure()
	now = now.Add(time.Minute)
	if !ups[0].Acquire() {
		t.Fatal("first half-open trial should be admitted")
	}
	if r.Ready() {
		t.Fatal("router must not be ready when its only healthy upstream has no free trial slot")
	}
	ups[0].Release()
	if !r.Ready() {
		t.Fatal("router should be ready once the trial slot is free again")
	}
}
