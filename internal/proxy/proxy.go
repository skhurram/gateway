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

// attempt is the per-request record of one upstream selection. settled is set
// once the transport has recorded an outcome for it.
type attempt struct {
	u       *balancer.Upstream
	settled bool
}

type attemptKey struct{}

func attemptFrom(ctx context.Context) *attempt {
	return ctx.Value(attemptKey{}).(*attempt)
}

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
			r.SetURL(attemptFrom(r.In.Context()).u.URL)
			r.SetXForwarded()
		},
		Transport: breakerTransport{next: rt},
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
		a := &attempt{u: u}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), attemptKey{}, a)))
		// If the transport never reported an outcome, return any half-open
		// trial reserved by Next so the breaker cannot get stuck.
		if !a.settled {
			u.Release()
		}
	})
}

type breakerTransport struct {
	next http.RoundTripper
}

func (t breakerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	a := attemptFrom(r.Context())
	resp, err := t.next.RoundTrip(r)
	a.settled = true
	if err != nil {
		// A client that went away is not evidence the upstream is unhealthy.
		if r.Context().Err() != nil {
			a.u.Release()
		} else {
			a.u.RecordFailure()
		}
		return nil, err
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		a.u.RecordFailure()
	} else {
		a.u.RecordSuccess()
	}
	return resp, nil
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
