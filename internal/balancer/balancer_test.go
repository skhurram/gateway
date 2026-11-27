package balancer_test

import (
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gateway/internal/balancer"
	"gateway/internal/breaker"
)

func pool(t *testing.T, n int) []*balancer.Upstream {
	t.Helper()
	ups := make([]*balancer.Upstream, n)
	for i := range ups {
		u, err := url.Parse("http://upstream-" + string(rune('a'+i)) + ":80")
		if err != nil {
			t.Fatal(err)
		}
		ups[i] = balancer.NewUpstream(u)
	}
	return ups
}

func TestRoundRobinOrder(t *testing.T) {
	ups := pool(t, 3)
	rr := balancer.NewRoundRobin(ups)
	for i := range 7 {
		got, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if want := ups[i%3]; got != want {
			t.Fatalf("call %d: got %v, want %v", i, got.URL, want.URL)
		}
	}
}

func TestRoundRobinConcurrentEvenSpread(t *testing.T) {
	const (
		upstreams  = 4
		goroutines = 50
		perG       = 20 // 1000 requests total
	)
	ups := pool(t, upstreams)
	rr := balancer.NewRoundRobin(ups)

	counts := make(map[*balancer.Upstream]*atomic.Int64, upstreams)
	for _, u := range ups {
		counts[u] = new(atomic.Int64)
	}

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perG {
				u, err := rr.Next()
				if err != nil {
					t.Error(err)
					return
				}
				counts[u].Add(1)
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * perG / upstreams)
	for u, c := range counts {
		if got := c.Load(); got != want {
			t.Errorf("%v served %d requests, want %d", u.URL, got, want)
		}
	}
}

func TestRoundRobinSkipsUnhealthy(t *testing.T) {
	ups := pool(t, 3)
	rr := balancer.NewRoundRobin(ups)
	ups[1].SetHealthy(false)

	for range 10 {
		got, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if got == ups[1] {
			t.Fatal("unhealthy upstream was selected")
		}
	}

	ups[1].SetHealthy(true)
	seen := map[*balancer.Upstream]bool{}
	for range 3 {
		u, _ := rr.Next()
		seen[u] = true
	}
	if !seen[ups[1]] {
		t.Error("recovered upstream did not return to rotation")
	}
}

func TestRoundRobinNoHealthy(t *testing.T) {
	ups := pool(t, 2)
	rr := balancer.NewRoundRobin(ups)
	for _, u := range ups {
		u.SetHealthy(false)
	}
	if _, err := rr.Next(); !errors.Is(err, balancer.ErrNoHealthyUpstream) {
		t.Errorf("err = %v, want ErrNoHealthyUpstream", err)
	}
	if _, err := balancer.NewRoundRobin(nil).Next(); !errors.Is(err, balancer.ErrNoHealthyUpstream) {
		t.Errorf("empty pool err = %v, want ErrNoHealthyUpstream", err)
	}
}

func TestRoundRobinSkipsOpenBreaker(t *testing.T) {
	ups := pool(t, 2)
	cb, err := breaker.New(breaker.Config{
		FailureThreshold: 1,
		OpenTimeout:      time.Minute,
		HalfOpenRequests: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ups[0].SetBreaker(cb)
	cb.Failure()

	rr := balancer.NewRoundRobin(ups)
	for range 5 {
		u, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if u != ups[1] {
			t.Fatalf("selected %v with an open circuit", u.URL)
		}
	}
}

func TestAvailableNeedsHealthAndAFreeBreakerSlot(t *testing.T) {
	u := balancer.NewUpstream(&url.URL{Scheme: "http", Host: "a"})
	if !u.Available() {
		t.Fatal("healthy upstream without a breaker should be available")
	}
	now := time.Unix(1_700_000_000, 0)
	b, err := breaker.New(breaker.Config{FailureThreshold: 1, OpenTimeout: time.Minute, HalfOpenRequests: 1},
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	u.SetBreaker(b)
	u.RecordFailure()
	now = now.Add(time.Minute)
	if !u.Acquire() { // half-open, only trial taken
		t.Fatal("first trial should be admitted")
	}
	if u.Available() {
		t.Fatal("half-open upstream with no free trial slot should not be available")
	}
	u.Release()
	if !u.Available() {
		t.Fatal("upstream should be available again once the trial is released")
	}
	u.SetHealthy(false)
	if u.Available() {
		t.Fatal("unhealthy upstream should not be available")
	}
}
