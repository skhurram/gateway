package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"gateway/internal/balancer"
	"gateway/internal/health"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func startChecker(t *testing.T, ups []*balancer.Upstream) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c := &health.Checker{Upstreams: ups, Path: "/health", Interval: 10 * time.Millisecond, Timeout: time.Second}
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after context cancel")
		}
	}
}

func fakeUpstream(t *testing.T, healthy *atomic.Bool) *balancer.Upstream {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("probe path = %q, want /health", r.URL.Path)
		}
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return balancer.NewUpstream(u)
}

func TestUpstreamLeavesRotationAndReturns(t *testing.T) {
	var aUp, bUp atomic.Bool
	aUp.Store(true)
	bUp.Store(true)
	a, b := fakeUpstream(t, &aUp), fakeUpstream(t, &bUp)
	rr := balancer.NewRoundRobin([]*balancer.Upstream{a, b})

	stop := startChecker(t, rr.Upstreams())
	defer stop()

	// Take b down: traffic must avoid it.
	bUp.Store(false)
	eventually(t, "b marked unhealthy", func() bool { return !b.Healthy() })
	for range 20 {
		u, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if u == b {
			t.Fatal("traffic was routed to the unhealthy upstream")
		}
	}

	// Bring b back: traffic must return to it.
	bUp.Store(true)
	eventually(t, "b marked healthy again", b.Healthy)
	seen := map[*balancer.Upstream]bool{}
	for range 4 {
		u, _ := rr.Next()
		seen[u] = true
	}
	if !seen[a] || !seen[b] {
		t.Errorf("after recovery, rotation = %v; want both upstreams", seen)
	}
}

func TestUnreachableUpstreamIsUnhealthy(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(s.URL)
	s.Close() // nothing listening
	up := balancer.NewUpstream(u)

	stop := startChecker(t, []*balancer.Upstream{up})
	defer stop()
	eventually(t, "unreachable upstream marked unhealthy", func() bool { return !up.Healthy() })
}

func TestRunStopsOnContextCancel(t *testing.T) {
	var ok atomic.Bool
	ok.Store(true)
	up := fakeUpstream(t, &ok)
	stop := startChecker(t, []*balancer.Upstream{up})
	stop() // fails the test if Run does not return
}
