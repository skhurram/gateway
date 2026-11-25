package breaker_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gateway/internal/breaker"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func newBreaker(t *testing.T, clk *fakeClock, failures, trials int) *breaker.Breaker {
	t.Helper()
	b, err := breaker.New(breaker.Config{
		FailureThreshold: failures,
		OpenTimeout:      time.Minute,
		HalfOpenRequests: trials,
	}, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  breaker.Config
	}{
		{"failure threshold", breaker.Config{OpenTimeout: time.Second, HalfOpenRequests: 1}},
		{"open timeout", breaker.Config{FailureThreshold: 1, HalfOpenRequests: 1}},
		{"half-open requests", breaker.Config{FailureThreshold: 1, OpenTimeout: time.Second}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := breaker.New(tt.cfg, nil); err == nil {
				t.Fatal("New succeeded with invalid config")
			}
		})
	}
}

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *breaker.Breaker, *fakeClock)
		state breaker.State
		allow bool
	}{
		{
			name:  "starts closed",
			setup: func(*testing.T, *breaker.Breaker, *fakeClock) {},
			state: breaker.Closed,
			allow: true,
		},
		{
			name: "opens at failure threshold",
			setup: func(_ *testing.T, b *breaker.Breaker, _ *fakeClock) {
				b.Failure()
				b.Failure()
			},
			state: breaker.Open,
			allow: false,
		},
		{
			name: "success resets consecutive failures",
			setup: func(_ *testing.T, b *breaker.Breaker, _ *fakeClock) {
				b.Failure()
				b.Success()
				b.Failure()
			},
			state: breaker.Closed,
			allow: true,
		},
		{
			name: "timeout admits first half-open trial",
			setup: func(_ *testing.T, b *breaker.Breaker, clk *fakeClock) {
				b.Failure()
				b.Failure()
				clk.Advance(time.Minute)
			},
			state: breaker.HalfOpen,
			allow: true,
		},
		{
			name: "half-open closes after all trials succeed",
			setup: func(t *testing.T, b *breaker.Breaker, clk *fakeClock) {
				t.Helper()
				b.Failure()
				b.Failure()
				clk.Advance(time.Minute)
				for range 2 {
					if !b.Allow() {
						t.Fatal("half-open trial was rejected")
					}
					b.Success()
				}
			},
			state: breaker.Closed,
			allow: true,
		},
		{
			name: "half-open reopens on failure",
			setup: func(t *testing.T, b *breaker.Breaker, clk *fakeClock) {
				t.Helper()
				b.Failure()
				b.Failure()
				clk.Advance(time.Minute)
				if !b.Allow() {
					t.Fatal("half-open trial was rejected")
				}
				b.Failure()
			},
			state: breaker.Open,
			allow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newClock()
			b := newBreaker(t, clk, 2, 2)
			tt.setup(t, b, clk)
			if got := b.Allow(); got != tt.allow {
				t.Fatalf("Allow() = %v, want %v", got, tt.allow)
			}
			if got := b.State(); got != tt.state {
				t.Errorf("State() = %q, want %q", got, tt.state)
			}
		})
	}
}

func TestOpenRejectsUntilTimeout(t *testing.T) {
	clk := newClock()
	b := newBreaker(t, clk, 1, 1)
	b.Failure()

	clk.Advance(time.Minute - time.Nanosecond)
	if b.Allow() {
		t.Fatal("request was admitted before open timeout")
	}
	clk.Advance(time.Nanosecond)
	if !b.Allow() {
		t.Fatal("request was rejected at open timeout")
	}
	if got := b.State(); got != breaker.HalfOpen {
		t.Errorf("State() = %q, want half_open", got)
	}
}

func TestHalfOpenLimitsTrials(t *testing.T) {
	clk := newClock()
	b := newBreaker(t, clk, 1, 2)
	b.Failure()
	clk.Advance(time.Minute)

	var admitted int
	for range 10 {
		if b.Allow() {
			admitted++
		}
	}
	if admitted != 2 {
		t.Errorf("admitted %d half-open requests, want 2", admitted)
	}
}

func TestConcurrentUse(t *testing.T) {
	clk := newClock()
	b := newBreaker(t, clk, 1, 100)
	b.Failure()
	clk.Advance(time.Minute)

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Allow() {
				b.Success()
				return
			}
			errs <- errors.New("half-open request rejected")
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
