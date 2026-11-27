// Package health actively probes upstreams and updates their rotation status.
package health

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"gateway/internal/balancer"
)

// maxDrainBytes bounds how much of a probe response is read so the connection
// can be reused. Health responses are normally far smaller.
const maxDrainBytes = 64 << 10

// Checker polls each upstream's health endpoint. A 2xx response marks the
// upstream healthy; anything else (including timeouts and connection errors)
// marks it unhealthy. Status is re-evaluated on every probe, so an upstream
// returns to rotation as soon as it passes one.
type Checker struct {
	Upstreams []*balancer.Upstream
	Path      string
	Interval  time.Duration
	Timeout   time.Duration
	Client    *http.Client // optional; defaults to a client using Timeout
}

// Run probes all upstreams until ctx is cancelled, then waits for every
// probe goroutine to exit before returning.
func (c *Checker) Run(ctx context.Context) {
	client := c.httpClient()

	var wg sync.WaitGroup
	for _, u := range c.Upstreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.loop(ctx, client, u)
		}()
	}
	wg.Wait()
}

// CheckOnce probes every upstream concurrently and returns when all probes
// finish. Call it before serving so readiness reflects real upstream state
// instead of the optimistic initial value.
func (c *Checker) CheckOnce(ctx context.Context) {
	client := c.httpClient()

	var wg sync.WaitGroup
	for _, u := range c.Upstreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.probe(ctx, client, u)
		}()
	}
	wg.Wait()
}

func (c *Checker) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: c.Timeout}
}

func (c *Checker) loop(ctx context.Context, client *http.Client, u *balancer.Upstream) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		c.probe(ctx, client, u)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Checker) probe(ctx context.Context, client *http.Client, u *balancer.Upstream) {
	target := u.URL.JoinPath(c.Path).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		u.SetHealthy(false)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down; don't flip status on a cancelled probe
		}
		u.SetHealthy(false)
		return
	}
	// Read the body before closing so the transport can reuse the connection.
	// The read is bounded so a large or endless body can't stall the probe.
	_, _ = io.CopyN(io.Discard, resp.Body, maxDrainBytes)
	_ = resp.Body.Close()
	u.SetHealthy(resp.StatusCode >= 200 && resp.StatusCode < 300)
}
