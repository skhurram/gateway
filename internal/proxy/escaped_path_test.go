package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gateway/internal/proxy"
	"gateway/pkg/config"
)

// The gateway routes on the cleaned, decoded path but must forward what the
// client sent when cleaning changes nothing, so an encoded slash (%2F) stays
// encoded for upstreams that treat it as data (B10).
func TestRouterKeepsEncodedSlashes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.EscapedPath())
	}))
	t.Cleanup(upstream.Close)
	router, err := proxy.NewRouter(&config.Config{Routes: []config.Route{{
		Prefix: "/api", Upstreams: []string{upstream.URL},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)

	tests := []struct{ sent, want string }{
		{"/api/files/a%2Fb", "/api/files/a%2Fb"},
		{"/api/files/a%2Fb/", "/api/files/a%2Fb/"},
		// Cleaning changes the path: the escaping is kept where it is safe.
		{"/api/x/../files/a%2Fb", "/api/files/a%2Fb"},
		{"/api//files/a%2Fb", "/api/files/a%2Fb"},
		// Encoded dot segments decode to "..": route and forward the cleaned
		// path, never the escaped original.
		{"/api/files/%2E%2E/a", "/api/a"},
		{"/api/files/a%2F..%2Fb", "/api/files/b"},
	}
	for _, tt := range tests {
		t.Run(tt.sent, func(t *testing.T) {
			resp, err := http.Get(gateway.URL + tt.sent)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || string(body) != tt.want {
				t.Fatalf("upstream saw %q (status %d), want %q", body, resp.StatusCode, tt.want)
			}
		})
	}
}
