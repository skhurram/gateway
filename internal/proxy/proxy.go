// Package proxy implements the gateway's reverse proxy.
package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"gateway/internal/balancer"
)

// Client-side (gateway -> upstream) timeouts.
const (
	dialTimeout           = 5 * time.Second
	tlsHandshakeTimeout   = 5 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second
)

// Server-side (client -> gateway) timeouts.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
)

// NewTransport returns an upstream transport with explicit timeouts and
// connection pooling limits.
func NewTransport() *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       idleConnTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
	}
}

type upstreamKey struct{}

// New returns a handler that forwards requests to a single target. It sets
// X-Forwarded-For/Host/Proto from the inbound connection, discarding any
// values supplied by the client.
func New(target *url.URL, rt http.RoundTripper) http.Handler {
	return NewBalanced(balancer.NewRoundRobin([]*balancer.Upstream{balancer.NewUpstream(target)}), rt)
}

// NewBalanced returns a handler that forwards each request to the upstream
// chosen by b. It responds 503 when b has no healthy upstream and 502 when the
// chosen upstream cannot be reached.
func NewBalanced(b balancer.Balancer, rt http.RoundTripper) http.Handler {
	if rt == nil {
		rt = NewTransport()
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(r.In.Context().Value(upstreamKey{}).(*url.URL))
			r.SetXForwarded()
		},
		Transport: rt,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := b.Next()
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), upstreamKey{}, u.URL)))
	})
}

// NewServer returns an http.Server with timeouts set.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}
