// Package ratelimit implements a per-client token-bucket rate limiter.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const sweepInterval = time.Minute

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a set of token buckets keyed by client identity. Each bucket
// holds up to burst tokens and refills at rate tokens per second.
type Limiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// New returns a Limiter. now supplies the current time; pass nil for
// time.Now. Tests inject a fake clock so they never have to sleep.
func New(requestsPerSecond float64, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		rate:      requestsPerSecond,
		burst:     float64(burst),
		now:       now,
		buckets:   make(map[string]*bucket),
		lastSweep: now(),
	}
}

// Allow takes one token from key's bucket. When none is available it returns
// false and how long the caller should wait before a token will be.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	t := l.now()
	l.sweep(t)

	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: l.burst, last: t}
		l.buckets[key] = b
	} else {
		b.tokens = l.refilled(b, t)
		b.last = t
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := (1 - b.tokens) / l.rate
	return false, time.Duration(math.Ceil(wait * float64(time.Second)))
}

func (l *Limiter) refilled(b *bucket, t time.Time) float64 {
	elapsed := t.Sub(b.last).Seconds()
	if elapsed < 0 { // clock went backwards
		elapsed = 0
	}
	return math.Min(l.burst, b.tokens+elapsed*l.rate)
}

// sweep drops buckets that have been idle long enough to be full again, since
// a fresh bucket is equivalent. It bounds memory by the number of recently
// active clients. Callers must hold l.mu.
func (l *Limiter) sweep(t time.Time) {
	if t.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = t
	for k, b := range l.buckets {
		if l.refilled(b, t) >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// Option configures Middleware.
type Option func(*middlewareConfig)

type middlewareConfig struct {
	onLimited func(*http.Request)
}

// WithOnLimited registers a callback invoked for every rejected request, for
// metrics.
func WithOnLimited(f func(*http.Request)) Option {
	return func(c *middlewareConfig) { c.onLimited = f }
}

// Middleware rejects requests over the limit with 429 and a Retry-After
// header. Clients are identified by the connection's remote IP; forwarded
// headers are ignored because clients can set them freely.
func Middleware(l *Limiter, opts ...Option) func(http.Handler) http.Handler {
	var cfg middlewareConfig
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retry := l.Allow(clientIP(r))
			if !ok {
				if cfg.onLimited != nil {
					cfg.onLimited(r)
				}
				secs := max(int(math.Ceil(retry.Seconds())), 1)
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
