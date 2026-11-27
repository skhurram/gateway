package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gateway/internal/proxy"
	"gateway/pkg/config"
)

func TestRuntimeManagerReloadKeepsOldRuntimeOnInvalidConfig(t *testing.T) {
	upstream := healthyUpstream(t)
	ctx, cancel := context.WithCancel(context.Background())
	manager := newRuntimeManager(ctx, proxy.NewTransport())
	cfg := parseRuntimeConfig(t, upstream.URL, "/old")
	if err := manager.initialize(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		manager.wait()
	})

	before := manager.router()
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte("routes:\n  - prefix: broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.reload(path); err == nil {
		t.Fatal("reload succeeded, want validation error")
	}
	if got := manager.router(); got != before {
		t.Fatal("invalid reload replaced the active router")
	}
}

func TestRuntimeManagerReloadServesNewRoutesWhileOldRequestFinishes(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	oldUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		close(entered)
		<-release
		_, _ = w.Write([]byte("old"))
	}))
	defer oldUpstream.Close()
	newUpstream := healthyUpstream(t)

	ctx, cancel := context.WithCancel(context.Background())
	manager := newRuntimeManager(ctx, proxy.NewTransport())
	if err := manager.initialize(parseRuntimeConfig(t, oldUpstream.URL, "/api")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		manager.wait()
	})

	oldDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		manager.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/work", nil))
		oldDone <- rec.Code
	}()
	<-entered

	path := filepath.Join(t.TempDir(), "gateway.yaml")
	writeRuntimeConfig(t, path, newUpstream.URL, "/v2", ":8080")
	if err := manager.reload(path); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	manager.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/item", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("new route status = %d, want 200", rec.Code)
	}
	close(release)
	if code := <-oldDone; code != http.StatusOK {
		t.Fatalf("in-flight old request status = %d, want 200", code)
	}
}

func TestRuntimeManagerRejectsListenAddressChange(t *testing.T) {
	upstream := healthyUpstream(t)
	ctx, cancel := context.WithCancel(context.Background())
	manager := newRuntimeManager(ctx, proxy.NewTransport())
	if err := manager.initialize(parseRuntimeConfig(t, upstream.URL, "/api")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		manager.wait()
	})

	path := filepath.Join(t.TempDir(), "gateway.yaml")
	writeRuntimeConfig(t, path, upstream.URL, "/api", ":9090")
	if err := manager.reload(path); err == nil {
		t.Fatal("reload succeeded, want listen-address error")
	}
}

func healthyUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func parseRuntimeConfig(t *testing.T, upstream, prefix string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(fmt.Sprintf(`
listen: ":8080"
health:
  path: /health
  interval: 1h
  timeout: 1s
routes:
  - prefix: %q
    upstreams: [%q]
`, prefix, upstream)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeRuntimeConfig(t *testing.T, path, upstream, prefix, listen string) {
	t.Helper()
	data := []byte(fmt.Sprintf(`
listen: %q
health:
  path: /health
  interval: 1h
  timeout: 1s
routes:
  - prefix: %q
    upstreams: [%q]
`, listen, prefix, upstream))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
