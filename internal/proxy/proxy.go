// Package proxy implements the gateway's reverse proxy.
package proxy

import (
	"context"
	"mime"
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
	rc      *http.ResponseController
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
		Transport:      breakerTransport{next: rt},
		ModifyResponse: liftStreamDeadlines,
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
		a := &attempt{u: u, rc: http.NewResponseController(w)}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), attemptKey{}, a)))
		// If the transport never reported an outcome, return any half-open
		// trial reserved by Next so the breaker cannot get stuck.
		if !a.settled {
			u.Release()
		}
	})
}

// liftStreamDeadlines removes the server's read and write deadlines for a
// server-sent event stream, which is meant to stay open far longer than the
// server timeouts allow. The response header has already arrived, so the
// upstream is answering; the stream then ends when either side closes it.
// Upgraded connections (WebSocket) need no help: the server clears deadlines
// when the reverse proxy hijacks the connection.
func liftStreamDeadlines(resp *http.Response) error {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return nil
	}
	a := attemptFrom(resp.Request.Context())
	// ErrNotSupported only means a wrapper hides the connection; the stream
	// then keeps the normal timeouts.
	_ = a.rc.SetWriteDeadline(time.Time{})
	_ = a.rc.SetReadDeadline(time.Time{})
	return nil
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

// serverTimeouts are the client -> gateway timeouts; tests shorten them.
type serverTimeouts struct {
	readHeader, read, write, idle time.Duration
}

// NewServer returns an http.Server with timeouts set.
func NewServer(addr string, h http.Handler) *http.Server {
	return newServer(addr, h, serverTimeouts{
		readHeader: readHeaderTimeout,
		read:       readTimeout,
		write:      writeTimeout,
		idle:       idleTimeout,
	})
}

func newServer(addr string, h http.Handler, t serverTimeouts) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: t.readHeader,
		ReadTimeout:       t.read,
		WriteTimeout:      t.write,
		IdleTimeout:       t.idle,
	}
}
