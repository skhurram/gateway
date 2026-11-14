// Package proxy implements the gateway's reverse proxy.
package proxy

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
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

// New returns a handler that forwards requests to target. It sets
// X-Forwarded-For/Host/Proto from the inbound connection, discarding any
// values supplied by the client.
func New(target *url.URL, rt http.RoundTripper) http.Handler {
	if rt == nil {
		rt = NewTransport()
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
		},
		Transport: rt,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		},
	}
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
