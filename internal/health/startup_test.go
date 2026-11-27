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

func countingUpstream(t *testing.T, probes *atomic.Int32) *balancer.Upstream {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return balancer.NewUpstream(u)
}

func runFor(c *health.Checker, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	c.Run(ctx)
}

// The runtime calls CheckOnce before serving and then starts Run. Run must not
// probe again right away, or every upstream is probed twice at startup.
func TestRunAfterCheckOnceWaitsForFirstInterval(t *testing.T) {
	var probes atomic.Int32
	c := &health.Checker{
		Upstreams: []*balancer.Upstream{countingUpstream(t, &probes)},
		Path:      "/health",
		Interval:  time.Hour,
		Timeout:   time.Second,
	}
	c.CheckOnce(context.Background())
	runFor(c, 200*time.Millisecond)
	if got := probes.Load(); got != 1 {
		t.Fatalf("CheckOnce then Run probed %d times before the first interval, want 1", got)
	}
}

// Without CheckOnce, Run still probes immediately.
func TestRunWithoutCheckOnceProbesImmediately(t *testing.T) {
	var probes atomic.Int32
	c := &health.Checker{
		Upstreams: []*balancer.Upstream{countingUpstream(t, &probes)},
		Path:      "/health",
		Interval:  time.Hour,
		Timeout:   time.Second,
	}
	runFor(c, 200*time.Millisecond)
	if got := probes.Load(); got != 1 {
		t.Fatalf("Run probed %d times before the first interval, want 1", got)
	}
}
