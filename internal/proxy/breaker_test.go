package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gateway/internal/balancer"
	"gateway/internal/breaker"
	"gateway/internal/proxy"
)

// A client that disconnects mid-request must not count against the upstream.
func TestClientCancelDoesNotOpenBreaker(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	cb, err := breaker.New(breaker.Config{
		FailureThreshold: 1,
		OpenTimeout:      time.Minute,
		HalfOpenRequests: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	up := balancer.NewUpstream(mustURL(t, upstream.URL))
	up.SetBreaker(cb)
	h := proxy.NewBalanced(balancer.NewRoundRobin([]*balancer.Upstream{up}), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client is already gone
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := cb.State(); got != breaker.Closed {
		t.Fatalf("breaker state = %s after client cancel, want closed", got)
	}
}
