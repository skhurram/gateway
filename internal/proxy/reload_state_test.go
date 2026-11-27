package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gateway/internal/breaker"
	"gateway/internal/proxy"
	"gateway/pkg/config"
)

func breakerConfig(upstreams ...string) *config.Config {
	return &config.Config{
		CircuitBreaker: config.CircuitBreaker{FailureThreshold: 1, OpenTimeout: time.Hour, HalfOpenRequests: 1},
		Routes:         []config.Route{{Prefix: "/api", Upstreams: upstreams}},
	}
}

// Each upstream keeps its own breaker across a reload, also when a route
// lists the same URL twice, and a removed upstream that comes back starts
// fresh.
func TestWithPreviousMatchesUpstreamsOneToOne(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	first, err := proxy.NewRouter(breakerConfig(failing.URL, failing.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	get(t, first, "/api") // round-robin: opens the first copy's breaker only
	open := func(r *proxy.Router) []bool {
		var out []bool
		for _, u := range r.Upstreams() {
			out = append(out, u.BreakerState() == breaker.Open)
		}
		return out
	}
	if got := open(first); !got[0] || got[1] {
		t.Fatalf("before reload: open = %v, want [true false]", got)
	}

	second, err := proxy.NewRouter(breakerConfig(failing.URL, failing.URL), nil, proxy.WithPrevious(first))
	if err != nil {
		t.Fatal(err)
	}
	if got := open(second); !got[0] || got[1] {
		t.Fatalf("after reload: open = %v, want [true false]; breakers must not be shared", got)
	}

	removed, err := proxy.NewRouter(breakerConfig("http://elsewhere:8080"), nil, proxy.WithPrevious(second))
	if err != nil {
		t.Fatal(err)
	}
	back, err := proxy.NewRouter(breakerConfig(failing.URL), nil, proxy.WithPrevious(removed))
	if err != nil {
		t.Fatal(err)
	}
	if got := open(back); got[0] {
		t.Fatal("an upstream removed and added back should start with a fresh breaker")
	}
}
