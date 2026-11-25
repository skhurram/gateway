package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gateway/internal/ratelimit"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func TestBurstThenLimitedThenRefilled(t *testing.T) {
	clk := newClock()
	l := ratelimit.New(2, 3, clk.Now) // 2 tokens/s, burst of 3

	for i := range 3 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d in burst was limited", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok {
		t.Fatal("request after burst was allowed")
	}
	if retry != 500*time.Millisecond {
		t.Errorf("retryAfter = %v, want 500ms", retry)
	}

	clk.Advance(500 * time.Millisecond) // exactly one token
	if ok, _ := l.Allow("a"); !ok {
		t.Error("request after refill was limited")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Error("second request after a one-token refill was allowed")
	}

	clk.Advance(time.Hour) // refill is capped at burst
	for i := range 3 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d after long idle was limited", i)
		}
	}
	if ok, _ := l.Allow("a"); ok {
		t.Error("burst cap was exceeded after long idle")
	}
}

func TestClientsHaveSeparateBuckets(t *testing.T) {
	l := ratelimit.New(1, 1, newClock().Now)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a first request limited")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("a second request allowed")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("b was limited by a's usage")
	}
}

func TestIdleBucketsAreSweptWithoutChangingBehaviour(t *testing.T) {
	clk := newClock()
	l := ratelimit.New(1, 1, clk.Now)
	l.Allow("a")
	clk.Advance(2 * time.Minute) // past the sweep interval and full again
	l.Allow("b")                 // triggers a sweep that drops a
	if ok, _ := l.Allow("a"); !ok {
		t.Error("a should have a full bucket after idling")
	}
}

func TestMiddleware(t *testing.T) {
	clk := newClock()
	l := ratelimit.New(0.5, 1, clk.Now) // one token every 2s
	served := 0
	h := ratelimit.Middleware(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	}))

	do := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-For", "9.9.9.9") // must not be used as the key
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("10.0.0.1:1111"); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}
	rec := do("10.0.0.1:2222") // same IP, different port
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	if rec := do("10.0.0.2:1111"); rec.Code != http.StatusOK {
		t.Errorf("different IP = %d, want 200", rec.Code)
	}
	if served != 2 {
		t.Errorf("handler served %d requests, want 2", served)
	}

	clk.Advance(2 * time.Second)
	if rec := do("10.0.0.1:3333"); rec.Code != http.StatusOK {
		t.Errorf("after refill = %d, want 200", rec.Code)
	}
}

func TestRetryAfterIsAtLeastOneSecond(t *testing.T) {
	l := ratelimit.New(100, 1, newClock().Now) // next token in 10ms
	h := ratelimit.Middleware(l)(http.NotFoundHandler())
	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("got %d Retry-After=%q, want 429 and 1", rec.Code, rec.Header().Get("Retry-After"))
	}
}
