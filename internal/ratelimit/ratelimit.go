// Package ratelimit implements a per-client token-bucket rate limiter.
package ratelimit

import (
	"hash/maphash"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	sweepInterval = time.Minute
	// shardCount splits the buckets across independently locked shards, so
	// unrelated clients rarely contend, and a sweep pauses only the clients
	// in one shard while it walks 1/shardCount of the buckets.
	shardCount = 64
)

type bucket struct {
	tokens float64
	last   time.Time
}

type shard struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// Limiter is a set of token buckets keyed by client identity. Each bucket
// holds up to burst tokens and refills at rate tokens per second.
type Limiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	seed   maphash.Seed
	shards [shardCount]shard
}

// New returns a Limiter. now supplies the current time; pass nil for
// time.Now. Tests inject a fake clock so they never have to sleep.
func New(requestsPerSecond float64, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	l := &Limiter{
		rate:  requestsPerSecond,
		burst: float64(burst),
		now:   now,
		seed:  maphash.MakeSeed(),
	}
	start := now()
	for i := range l.shards {
		l.shards[i].buckets = make(map[string]*bucket)
		l.shards[i].lastSweep = start
	}
	return l
}

// Allow takes one token from key's bucket. When none is available it returns
// false and how long the caller should wait before a token will be.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	s := &l.shards[maphash.String(l.seed, key)%shardCount]
	s.mu.Lock()
	defer s.mu.Unlock()

	t := l.now()
	l.sweep(s, t)

	b, found := s.buckets[key]
	if !found {
		b = &bucket{tokens: l.burst, last: t}
		s.buckets[key] = b
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

// sweep drops a shard's buckets that have been idle long enough to be full
// again, since a fresh bucket is equivalent. Each shard sweeps itself, at
// most once per sweepInterval, on its next Allow. This bounds memory by the
// number of recently active clients. Callers must hold s.mu.
func (l *Limiter) sweep(s *shard, t time.Time) {
	if t.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = t
	for k, b := range s.buckets {
		if l.refilled(b, t) >= l.burst {
			delete(s.buckets, k)
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
