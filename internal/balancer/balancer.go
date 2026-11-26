// Package balancer selects an upstream for each request.
package balancer

import (
	"errors"
	"net/url"
	"sync/atomic"

	"gateway/internal/breaker"
)

// ErrNoHealthyUpstream is returned when every upstream is marked unhealthy.
var ErrNoHealthyUpstream = errors.New("no healthy upstream")

// Upstream is a backend target whose health can change concurrently.
type Upstream struct {
	URL     *url.URL
	healthy atomic.Bool
	breaker *breaker.Breaker
}

// NewUpstream returns an upstream that starts out healthy.
func NewUpstream(u *url.URL) *Upstream {
	up := &Upstream{URL: u}
	up.healthy.Store(true)
	return up
}

// Healthy reports whether the upstream is in rotation.
func (u *Upstream) Healthy() bool { return u.healthy.Load() }

// SetHealthy moves the upstream in or out of rotation.
func (u *Upstream) SetHealthy(h bool) { u.healthy.Store(h) }

// SetBreaker installs the passive circuit breaker for this upstream.
func (u *Upstream) SetBreaker(b *breaker.Breaker) { u.breaker = b }

// Acquire reports whether a request may use the upstream, reserving a
// half-open trial when one is needed.
func (u *Upstream) Acquire() bool {
	if !u.Healthy() {
		return false
	}
	return u.breaker == nil || u.breaker.Allow()
}

// RecordSuccess records a successful upstream request.
func (u *Upstream) RecordSuccess() {
	if u.breaker != nil {
		u.breaker.Success()
	}
}

// RecordFailure records a failed upstream request.
func (u *Upstream) RecordFailure() {
	if u.breaker != nil {
		u.breaker.Failure()
	}
}

// Release gives back a reservation made by Acquire when the request ended
// without a verdict on the upstream (for example, the client went away first).
func (u *Upstream) Release() {
	if u.breaker != nil {
		u.breaker.Release()
	}
}

// BreakerState returns the upstream's passive circuit state.
func (u *Upstream) BreakerState() breaker.State {
	if u.breaker == nil {
		return breaker.Closed
	}
	return u.breaker.State()
}

// Balancer picks the upstream that should serve the next request.
// Implementations must be safe for concurrent use.
type Balancer interface {
	Next() (*Upstream, error)
}

// RoundRobin cycles through upstreams in order, skipping unhealthy ones.
type RoundRobin struct {
	upstreams []*Upstream
	counter   atomic.Uint64
}

// NewRoundRobin returns a round-robin balancer over ups.
func NewRoundRobin(ups []*Upstream) *RoundRobin {
	return &RoundRobin{upstreams: ups}
}

// Next returns the next healthy upstream, or ErrNoHealthyUpstream.
func (r *RoundRobin) Next() (*Upstream, error) {
	n := uint64(len(r.upstreams))
	if n == 0 {
		return nil, ErrNoHealthyUpstream
	}
	// Each call claims a unique counter value, so traffic is spread evenly
	// even under heavy concurrency.
	start := r.counter.Add(1) - 1
	for i := range n {
		if u := r.upstreams[(start+i)%n]; u.Acquire() {
			return u, nil
		}
	}
	return nil, ErrNoHealthyUpstream
}

// Upstreams returns the pool, for health checking.
func (r *RoundRobin) Upstreams() []*Upstream { return r.upstreams }
