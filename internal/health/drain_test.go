package health_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gateway/internal/balancer"
	"gateway/internal/health"
)

// A probe must read the response body before closing it; otherwise the
// transport cannot reuse the connection and probes dial new ones. Go copes with
// small unread bodies, so the test uses a 60 KB body, where it does not.
func TestProbesReuseConnection(t *testing.T) {
	var conns atomic.Int32
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("ok\n", 20000)))
	}))
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	s.Start()
	t.Cleanup(s.Close)

	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	c := &health.Checker{
		Upstreams: []*balancer.Upstream{balancer.NewUpstream(u)},
		Path:      "/health",
		Timeout:   time.Second,
		Client:    &http.Client{Timeout: time.Second, Transport: transport},
	}
	for range 5 {
		c.CheckOnce(context.Background())
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("5 probes opened %d connections, want 1", got)
	}
}
