// Package breaker provides a concurrency-safe circuit breaker state machine.
package breaker

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is the current circuit breaker state.
type State string

const (
	// Closed allows all requests and records consecutive failures.
	Closed State = "closed"
	// Open rejects requests until the configured timeout has elapsed.
	Open State = "open"
	// HalfOpen admits a limited number of trial requests.
	HalfOpen State = "half_open"
)

// Config controls circuit breaker transitions.
type Config struct {
	FailureThreshold int
	OpenTimeout      time.Duration
	HalfOpenRequests int
}

// Breaker tracks failures and limits requests while an upstream recovers.
type Breaker struct {
	mu sync.Mutex

	failureThreshold int
	openTimeout      time.Duration
	halfOpenRequests int
	now              func() time.Time

	state          State
	failures       int
	openedAt       time.Time
	halfOpenTrials int
	halfOpenPasses int
}

// New creates a breaker in the closed state. now defaults to time.Now when nil.
func New(cfg Config, now func() time.Time) (*Breaker, error) {
	var errs []error
	if cfg.FailureThreshold < 1 {
		errs = append(errs, errors.New("failure threshold must be at least 1"))
	}
	if cfg.OpenTimeout <= 0 {
		errs = append(errs, errors.New("open timeout must be positive"))
	}
	if cfg.HalfOpenRequests < 1 {
		errs = append(errs, errors.New("half-open requests must be at least 1"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("invalid circuit breaker config: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	return &Breaker{
		failureThreshold: cfg.FailureThreshold,
		openTimeout:      cfg.OpenTimeout,
		halfOpenRequests: cfg.HalfOpenRequests,
		now:              now,
		state:            Closed,
	}, nil
}

// Allow reports whether a request may proceed. It moves an expired open
// breaker to half-open and reserves one of its trial requests.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case Closed:
		return true
	case Open:
		if b.now().Sub(b.openedAt) < b.openTimeout {
			return false
		}
		b.state = HalfOpen
		b.halfOpenTrials = 0
		b.halfOpenPasses = 0
		fallthrough
	case HalfOpen:
		if b.halfOpenTrials >= b.halfOpenRequests {
			return false
		}
		b.halfOpenTrials++
		return true
	default:
		return false
	}
}

// Success records a successful request.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case Closed:
		b.failures = 0
	case HalfOpen:
		b.halfOpenPasses++
		if b.halfOpenPasses == b.halfOpenRequests {
			b.state = Closed
			b.failures = 0
			b.halfOpenTrials = 0
			b.halfOpenPasses = 0
		}
	}
}

// Failure records a failed request. In closed state, the configured number of
// consecutive failures opens the breaker; any half-open failure reopens it.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case Closed:
		b.failures++
		if b.failures >= b.failureThreshold {
			b.open()
		}
	case HalfOpen:
		b.open()
	}
}

// State returns the current breaker state. An open breaker becomes half-open
// only when Allow admits the first trial request after its timeout.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) open() {
	b.state = Open
	b.openedAt = b.now()
	b.failures = 0
	b.halfOpenTrials = 0
	b.halfOpenPasses = 0
}
