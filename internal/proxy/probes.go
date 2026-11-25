package proxy

import "net/http"

// Probe paths served by WithProbes. They take precedence over routes.
const (
	LivenessPath  = "/healthz"
	ReadinessPath = "/readyz"
)

// WithProbes serves the liveness and readiness endpoints ahead of next.
//
// /healthz always returns 200 while the process can serve requests.
// /readyz returns 200 when ready() is true and 503 otherwise.
func WithProbes(next http.Handler, ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LivenessPath:
			writeProbe(w, http.StatusOK, "ok")
		case ReadinessPath:
			if ready() {
				writeProbe(w, http.StatusOK, "ready")
				return
			}
			writeProbe(w, http.StatusServiceUnavailable, "not ready")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func writeProbe(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body + "\n"))
}
