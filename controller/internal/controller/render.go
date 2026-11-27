// Package controller renders Route resources into gateway configuration and reconciles them.
package controller

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

const (
	defaultListen         = ":8080"
	defaultHealthPath     = "/health"
	defaultHealthInterval = "5s"
	defaultHealthTimeout  = "2s"
)

// RenderOptions controls fields shared by all rendered routes.
type RenderOptions struct {
	Listen         string
	HealthPath     string
	HealthInterval string
	HealthTimeout  string
}

// Conflict describes a Route excluded because another Route owns its prefix.
type Conflict struct {
	Route  string
	Prefix string
	Winner string
}

type renderedConfig struct {
	Listen string          `yaml:"listen"`
	Health renderedHealth  `yaml:"health"`
	Routes []renderedRoute `yaml:"routes"`
}

type renderedHealth struct {
	Path     string `yaml:"path"`
	Interval string `yaml:"interval"`
	Timeout  string `yaml:"timeout"`
}

type renderedRoute struct {
	Prefix    string             `yaml:"prefix"`
	Upstreams []string           `yaml:"upstreams"`
	RateLimit *renderedRateLimit `yaml:"rate_limit,omitempty"`
}

type renderedRateLimit struct {
	RequestsPerSecond int32 `yaml:"requests_per_second"`
	Burst             int32 `yaml:"burst"`
}

// Render produces deterministic gateway YAML and reports duplicate prefixes.
// The oldest Route owns a prefix; name breaks equal creation-time ties.
func Render(routes []gatewayv1alpha1.Route, opts RenderOptions) ([]byte, []Conflict, error) {
	opts.applyDefaults()
	winners, conflicts := selectRoutes(routes, nil)
	out := renderedConfig{
		Listen: opts.Listen,
		Health: renderedHealth{
			Path: opts.HealthPath, Interval: opts.HealthInterval, Timeout: opts.HealthTimeout,
		},
		Routes: make([]renderedRoute, 0, len(winners)),
	}
	for _, route := range winners {
		out.Routes = append(out.Routes, renderRoute(route))
	}

	data, err := yaml.Marshal(out)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal gateway config: %w", err)
	}
	return data, conflicts, nil
}

// Merge adds Route routes to a base gateway config that the controller does not
// own (the Helm chart's ConfigMap). Every base setting is kept as written, and
// base routes keep their prefixes: a Route with the same normalized prefix is
// reported as a conflict whose winner is baseOwner. The output always states
// the route table, even when it is empty.
func Merge(base []byte, routes []gatewayv1alpha1.Route, baseOwner string) ([]byte, []Conflict, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal(base, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse base config: %w", err)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}

	var baseRoutes []any
	if raw := cfg["routes"]; raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, nil, errors.New("parse base config: routes must be a list")
		}
		baseRoutes = list
	}
	reserved := make(map[string]string, len(baseRoutes))
	for i, item := range baseRoutes {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("parse base config: routes[%d] must be a mapping", i)
		}
		prefix, ok := entry["prefix"].(string)
		if !ok || prefix == "" {
			return nil, nil, fmt.Errorf("parse base config: routes[%d] needs a prefix", i)
		}
		reserved[normalizePrefix(prefix)] = "base config " + baseOwner
	}

	winners, conflicts := selectRoutes(routes, reserved)
	merged := make([]any, 0, len(baseRoutes)+len(winners))
	merged = append(merged, baseRoutes...)
	for _, route := range winners {
		merged = append(merged, renderRoute(route))
	}
	cfg["routes"] = merged

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal gateway config: %w", err)
	}
	return data, conflicts, nil
}

// selectRoutes picks one Route per normalized prefix, sorted by prefix. A prefix
// in reserved is already taken, and its value names the owner. Among Routes,
// the oldest owns a prefix; name breaks equal creation-time ties.
func selectRoutes(
	routes []gatewayv1alpha1.Route,
	reserved map[string]string,
) ([]gatewayv1alpha1.Route, []Conflict) {
	ordered := append([]gatewayv1alpha1.Route(nil), routes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		it, jt := ordered[i].CreationTimestamp.Time, ordered[j].CreationTimestamp.Time
		if !it.Equal(jt) {
			return it.Before(jt)
		}
		return routeKey(ordered[i]) < routeKey(ordered[j])
	})

	selected := make(map[string]gatewayv1alpha1.Route, len(ordered))
	var conflicts []Conflict
	for _, route := range ordered {
		prefix := normalizePrefix(route.Spec.Prefix)
		if owner, ok := reserved[prefix]; ok {
			conflicts = append(conflicts, Conflict{Route: routeKey(route), Prefix: prefix, Winner: owner})
			continue
		}
		if winner, ok := selected[prefix]; ok {
			conflicts = append(conflicts, Conflict{
				Route:  routeKey(route),
				Prefix: prefix,
				Winner: routeKey(winner),
			})
			continue
		}
		route.Spec.Prefix = prefix
		selected[prefix] = route
	}

	winners := make([]gatewayv1alpha1.Route, 0, len(selected))
	for _, route := range selected {
		winners = append(winners, route)
	}
	sort.SliceStable(winners, func(i, j int) bool {
		return winners[i].Spec.Prefix < winners[j].Spec.Prefix ||
			(winners[i].Spec.Prefix == winners[j].Spec.Prefix &&
				routeKey(winners[i]) < routeKey(winners[j]))
	})
	return winners, conflicts
}

func renderRoute(route gatewayv1alpha1.Route) renderedRoute {
	rendered := renderedRoute{
		Prefix:    route.Spec.Prefix,
		Upstreams: append([]string(nil), route.Spec.Upstreams...),
	}
	if limit := route.Spec.RateLimit; limit != nil {
		rendered.RateLimit = &renderedRateLimit{
			RequestsPerSecond: limit.RequestsPerSecond,
			Burst:             limit.Burst,
		}
	}
	return rendered
}

func (o *RenderOptions) applyDefaults() {
	if o.Listen == "" {
		o.Listen = defaultListen
	}
	if o.HealthPath == "" {
		o.HealthPath = defaultHealthPath
	}
	if o.HealthInterval == "" {
		o.HealthInterval = defaultHealthInterval
	}
	if o.HealthTimeout == "" {
		o.HealthTimeout = defaultHealthTimeout
	}
}

func routeKey(route gatewayv1alpha1.Route) string {
	return route.Namespace + "/" + route.Name
}

func normalizePrefix(prefix string) string {
	normalized := strings.TrimRight(prefix, "/")
	if normalized == "" {
		return "/"
	}
	return normalized
}
