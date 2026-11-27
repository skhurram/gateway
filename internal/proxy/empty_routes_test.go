package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gateway/internal/config"
	"gateway/internal/proxy"
)

// With no routes, every request is 404 and the gateway is not ready. This is
// what happens after the last Route is deleted.
func TestEmptyRouteTableServesNotFound(t *testing.T) {
	cfg, err := config.Parse([]byte("routes: []"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := proxy.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/hello", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if r.Ready() {
		t.Error("router with no upstreams reports ready")
	}
}
