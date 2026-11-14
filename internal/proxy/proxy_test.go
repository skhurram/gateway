package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"gateway/internal/proxy"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestProxyForwardsRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			t.Errorf("upstream path = %q, want /hello", r.URL.Path)
		}
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, "world")
	}))
	defer upstream.Close()

	gw := httptest.NewServer(proxy.New(mustURL(t, upstream.URL), nil))
	defer gw.Close()

	resp, err := http.Get(gw.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || string(body) != "world" {
		t.Fatalf("got %d %q, want 200 world", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Error("upstream response header not forwarded")
	}
}

func TestProxySetsForwardedFor(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer upstream.Close()

	gw := httptest.NewServer(proxy.New(mustURL(t, upstream.URL), nil))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodGet, gw.URL, nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6") // spoofed, must be discarded
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if xff := got.Get("X-Forwarded-For"); xff != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want 127.0.0.1", xff)
	}
}

func TestProxyUpstreamDownReturns502(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	target := mustURL(t, upstream.URL)
	upstream.Close() // nothing listening any more

	gw := httptest.NewServer(proxy.New(target, nil))
	defer gw.Close()

	resp, err := http.Get(gw.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestGracefulShutdownDrainsInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	}))
	defer upstream.Close()

	gw := httptest.NewUnstartedServer(proxy.New(mustURL(t, upstream.URL), nil))
	gw.Start()
	defer gw.Close()

	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get(gw.URL)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		resCh <- result{body: string(b), err: err}
	}()

	<-started // request is now in flight

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- gw.Config.Shutdown(ctx)
	}()

	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while a request was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	r := <-resCh
	if r.err != nil || r.body != "done" {
		t.Fatalf("in-flight request got %q, %v; want done, nil", r.body, r.err)
	}
}
