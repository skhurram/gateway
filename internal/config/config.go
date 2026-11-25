// Package config loads and validates the gateway's YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultListen                  = ":8080"
	defaultHealthPath              = "/health"
	defaultHealthInterval          = 5 * time.Second
	defaultHealthTimeout           = 2 * time.Second
	defaultBreakerFailureThreshold = 5
	defaultBreakerOpenTimeout      = 30 * time.Second
	defaultBreakerHalfOpenRequests = 1
)

// Config is the top-level gateway configuration.
type Config struct {
	Listen         string         `yaml:"listen"`
	Health         Health         `yaml:"health"`
	CircuitBreaker CircuitBreaker `yaml:"circuit_breaker"`
	Routes         []Route        `yaml:"routes"`
}

// Health configures active upstream health checks.
type Health struct {
	Path     string        `yaml:"path"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// CircuitBreaker configures the passive breaker applied to every upstream.
type CircuitBreaker struct {
	FailureThreshold int           `yaml:"failure_threshold"`
	OpenTimeout      time.Duration `yaml:"open_timeout"`
	HalfOpenRequests int           `yaml:"half_open_requests"`
}

// Route maps a path prefix to a pool of upstreams.
type Route struct {
	Prefix    string     `yaml:"prefix"`
	Upstreams []string   `yaml:"upstreams"`
	RateLimit *RateLimit `yaml:"rate_limit"`
}

// RateLimit is a per-client-IP token bucket.
type RateLimit struct {
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

// Load reads and validates the YAML file at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // path is an operator-supplied flag
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes and validates YAML config. Unknown fields are rejected so
// typos fail loudly instead of being silently ignored.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Single returns a validated config with one catch-all route to upstream. It
// backs the -listen/-upstream command-line shortcut.
func Single(listen, upstream string) (*Config, error) {
	c := &Config{
		Listen: listen,
		Routes: []Route{{Prefix: "/", Upstreams: []string{upstream}}},
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = defaultListen
	}
	if c.Health.Path == "" {
		c.Health.Path = defaultHealthPath
	}
	if c.Health.Interval == 0 {
		c.Health.Interval = defaultHealthInterval
	}
	if c.Health.Timeout == 0 {
		c.Health.Timeout = defaultHealthTimeout
	}
	c.CircuitBreaker = c.EffectiveCircuitBreaker()
	for i := range c.Routes {
		c.Routes[i].Prefix = NormalizePrefix(c.Routes[i].Prefix)
	}
}

// EffectiveCircuitBreaker returns the circuit breaker settings with defaults
// applied. This keeps programmatically constructed configs consistent with
// parsed YAML configs.
func (c *Config) EffectiveCircuitBreaker() CircuitBreaker {
	cb := c.CircuitBreaker
	if cb.FailureThreshold == 0 {
		cb.FailureThreshold = defaultBreakerFailureThreshold
	}
	if cb.OpenTimeout == 0 {
		cb.OpenTimeout = defaultBreakerOpenTimeout
	}
	if cb.HalfOpenRequests == 0 {
		cb.HalfOpenRequests = defaultBreakerHalfOpenRequests
	}
	return cb
}

// NormalizePrefix strips trailing slashes, keeping "/" for the root prefix.
func NormalizePrefix(p string) string {
	if p == "" {
		return p
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

func (c *Config) validate() error {
	if c.Health.Interval < 0 || c.Health.Timeout < 0 {
		return errors.New("health: interval and timeout must be positive")
	}
	if !strings.HasPrefix(c.Health.Path, "/") {
		return fmt.Errorf("health: path %q must start with /", c.Health.Path)
	}
	if c.CircuitBreaker.FailureThreshold < 1 ||
		c.CircuitBreaker.OpenTimeout <= 0 ||
		c.CircuitBreaker.HalfOpenRequests < 1 {
		return errors.New("circuit_breaker: failure_threshold, open_timeout, and half_open_requests must be positive")
	}
	if len(c.Routes) == 0 {
		return errors.New("at least one route is required")
	}
	seen := make(map[string]bool, len(c.Routes))
	for i, r := range c.Routes {
		if !strings.HasPrefix(r.Prefix, "/") {
			return fmt.Errorf("route %d: prefix %q must start with /", i, r.Prefix)
		}
		if seen[r.Prefix] {
			return fmt.Errorf("route %d: duplicate prefix %q", i, r.Prefix)
		}
		seen[r.Prefix] = true
		if len(r.Upstreams) == 0 {
			return fmt.Errorf("route %q: at least one upstream is required", r.Prefix)
		}
		for _, raw := range r.Upstreams {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("route %q: invalid upstream %q (want http(s)://host[:port])", r.Prefix, raw)
			}
		}
		if rl := r.RateLimit; rl != nil && (rl.RequestsPerSecond <= 0 || rl.Burst <= 0) {
			return fmt.Errorf("route %q: rate_limit requires positive requests_per_second and burst", r.Prefix)
		}
	}
	return nil
}
