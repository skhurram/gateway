package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gateway/internal/config"
)

func TestParseValid(t *testing.T) {
	c, err := config.Parse([]byte(`
listen: ":9090"
circuit_breaker:
  failure_threshold: 3
  open_timeout: 20s
  half_open_requests: 2
routes:
  - prefix: /api/users/
    upstreams: [http://users-1:9000, http://users-2:9000]
    rate_limit:
      requests_per_second: 50
      burst: 100
  - prefix: /
    upstreams: [http://web:8000]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9090" {
		t.Errorf("listen = %q", c.Listen)
	}
	if got := c.Routes[0].Prefix; got != "/api/users" {
		t.Errorf("prefix not normalized: %q", got)
	}
	if rl := c.Routes[0].RateLimit; rl == nil || rl.RequestsPerSecond != 50 || rl.Burst != 100 {
		t.Errorf("rate limit = %+v", rl)
	}
	if c.Routes[1].RateLimit != nil {
		t.Error("route without rate_limit should have nil RateLimit")
	}
	if got := c.CircuitBreaker; got.FailureThreshold != 3 || got.OpenTimeout != 20*time.Second || got.HalfOpenRequests != 2 {
		t.Errorf("circuit breaker = %+v", got)
	}
	if c.Health.Path != "/health" || c.Health.Interval != 5*time.Second || c.Health.Timeout != 2*time.Second {
		t.Errorf("health defaults = %+v", c.Health)
	}
}

func TestParseDefaultsAndHealthOverride(t *testing.T) {
	c, err := config.Parse([]byte(`
health: {path: /up, interval: 1s, timeout: 500ms}
routes:
  - prefix: /
    upstreams: [http://a:1]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" {
		t.Errorf("default listen = %q", c.Listen)
	}
	if c.Health.Path != "/up" || c.Health.Interval != time.Second || c.Health.Timeout != 500*time.Millisecond {
		t.Errorf("health = %+v", c.Health)
	}
	if got := c.CircuitBreaker; got.FailureThreshold != 5 || got.OpenTimeout != 30*time.Second || got.HalfOpenRequests != 1 {
		t.Errorf("circuit breaker defaults = %+v", got)
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"empty", ``, "empty"},
		{"no routes", `listen: ":1"`, "at least one route"},
		{"bad prefix", "routes:\n  - prefix: api\n    upstreams: [http://a:1]", "must start with /"},
		{"duplicate prefix", "routes:\n  - {prefix: /a, upstreams: [http://a:1]}\n  - {prefix: /a/, upstreams: [http://b:1]}", "duplicate"},
		{"no upstreams", "routes:\n  - prefix: /a", "at least one upstream"},
		{"bad upstream scheme", "routes:\n  - {prefix: /a, upstreams: [ftp://a:1]}", "invalid upstream"},
		{"upstream no host", "routes:\n  - {prefix: /a, upstreams: ['http://']}", "invalid upstream"},
		{"bad rate limit", "routes:\n  - {prefix: /a, upstreams: [http://a:1], rate_limit: {requests_per_second: 0, burst: 1}}", "rate_limit"},
		{"unknown field", "routes:\n  - {prefix: /a, upstream: [http://a:1]}", "upstream"},
		{"bad health path", "health: {path: up}\nroutes:\n  - {prefix: /a, upstreams: [http://a:1]}", "health"},
		{"bad breaker", "circuit_breaker: {failure_threshold: -1}\nroutes:\n  - {prefix: /a, upstreams: [http://a:1]}", "circuit_breaker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSingle(t *testing.T) {
	c, err := config.Single(":7000", "http://localhost:9000")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7000" || len(c.Routes) != 1 || c.Routes[0].Prefix != "/" {
		t.Errorf("config = %+v", c)
	}
	if _, err := config.Single(":7000", "not-a-url"); err == nil {
		t.Error("expected error for invalid upstream")
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gw.yaml")
	if err := os.WriteFile(path, []byte("routes:\n  - {prefix: /, upstreams: [http://a:1]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("expected error for missing file")
	}
}
