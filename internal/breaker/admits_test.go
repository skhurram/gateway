package breaker_test

import (
	"testing"
	"time"
)

// Admits reports whether Allow would admit a request now, without reserving
// a trial. Readiness uses it, so a half-open breaker with every trial slot
// taken must not count as available.
func TestAdmitsMatchesAllowWithoutReserving(t *testing.T) {
	clk := newClock()
	b := newBreaker(t, clk, 1, 1)
	if !b.Admits() {
		t.Fatal("closed breaker should admit")
	}

	b.Failure() // opens
	if b.Admits() {
		t.Fatal("open breaker inside its timeout should not admit")
	}

	clk.Advance(time.Minute)
	if !b.Admits() {
		t.Fatal("open breaker past its timeout admits a trial, so it should report Admits")
	}
	if !b.Admits() {
		t.Fatal("Admits must not reserve the trial")
	}

	if !b.Allow() { // half-open; takes the only trial
		t.Fatal("Allow should admit the first trial")
	}
	if b.Admits() {
		t.Fatal("half-open breaker with no free trial slot should not admit")
	}

	b.Release()
	if !b.Admits() {
		t.Fatal("released trial slot should make the breaker admit again")
	}
}
