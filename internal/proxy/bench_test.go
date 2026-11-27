package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gateway/internal/proxy"
	"gateway/pkg/config"
)

// BenchmarkProxy measures the full gateway path (route match, balancer,
// reverse proxy, loopback round trip to a trivial upstream) without the
// client-to-gateway network hop.
func BenchmarkProxy(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	r, err := proxy.NewRouter(&config.Config{Routes: []config.Route{
		{Prefix: "/", Upstreams: []string{upstream.URL}},
	}}, nil)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://gw/api/ping", nil))
			if rec.Code != http.StatusOK {
				b.Errorf("status = %d", rec.Code)
				return
			}
		}
	})
}
