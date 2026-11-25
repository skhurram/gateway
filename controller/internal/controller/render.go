// Package controller renders Route resources into gateway configuration and reconciles them.
package controller

import (
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
	Routes []renderedRoute `yaml:"routes,omitempty"`
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

	out := renderedConfig{
		Listen: opts.Listen,
		Health: renderedHealth{
			Path: opts.HealthPath, Interval: opts.HealthInterval, Timeout: opts.HealthTimeout,
		},
		Routes: make([]renderedRoute, 0, len(winners)),
	}
	for _, route := range winners {
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
		out.Routes = append(out.Routes, rendered)
	}

	data, err := yaml.Marshal(out)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal gateway config: %w", err)
	}
	return data, conflicts, nil
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
