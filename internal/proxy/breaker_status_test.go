package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"gateway/internal/proxy"
	"gateway/pkg/config"
)

// Only statuses that mean the upstream failed or is unavailable open the
// breaker (decision 005). Statuses that describe the request, such as 501
// and 505, mean the upstream answered and must not take it out of rotation.
func TestBreakerFailureStatuses(t *testing.T) {
	tests := []struct {
		status       int
		opensBreaker bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusNotImplemented, false},
		{http.StatusHTTPVersionNotSupported, false},
		{http.StatusVariantAlsoNegotiates, false},
		{http.StatusInsufficientStorage, false},
		{http.StatusNetworkAuthenticationRequired, false},
		{599, false},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer upstream.Close()
			r, err := proxy.NewRouter(&config.Config{
				CircuitBreaker: config.CircuitBreaker{
					FailureThreshold: 1, OpenTimeout: time.Minute, HalfOpenRequests: 1,
				},
				Routes: []config.Route{{Prefix: "/", Upstreams: []string{upstream.URL}}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if code, _ := get(t, r, "/"); code != tt.status {
				t.Fatalf("first request: status %d, want %d passed through", code, tt.status)
			}
			// The only upstream is healthy, so the router is not ready exactly
			// when its breaker has opened.
			if opened := !r.Ready(); opened != tt.opensBreaker {
				t.Fatalf("status %d: breaker opened = %v, want %v", tt.status, opened, tt.opensBreaker)
			}
		})
	}
}
