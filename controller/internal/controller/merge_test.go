package controller

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

const testBase = `listen: ":9090"
health:
  path: /healthz
  interval: 3s
  timeout: 1s
circuit_breaker:
  failure_threshold: 7
  open_timeout: 15s
  half_open_requests: 2
routes:
  - prefix: /
    upstreams: [http://chart-default:8080]
  - prefix: /static/
    upstreams: [http://chart-static:8080]
`

func TestMergeKeepsBaseSettingsAndRoutes(t *testing.T) {
	routes := []gatewayv1alpha1.Route{route("users", "/api/users", metav1.NewTime(time.Unix(1, 0)))}
	data, conflicts, err := Merge([]byte(testBase), routes, "default/gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("merged output is not YAML: %v\n%s", err, data)
	}
	if got["listen"] != ":9090" {
		t.Errorf("listen = %v, want the base value", got["listen"])
	}
	cb, _ := got["circuit_breaker"].(map[string]any)
	if cb["failure_threshold"] != 7 || cb["open_timeout"] != "15s" {
		t.Errorf("circuit_breaker = %v, want the base settings", got["circuit_breaker"])
	}
	if prefixes := routePrefixes(t, got); strings.Join(prefixes, ",") != "/,/static/,/api/users" {
		t.Errorf("route prefixes = %v, want base routes then Route routes", prefixes)
	}

	again, _, err := Merge([]byte(testBase), routes, "default/gateway")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Fatal("identical inputs produced different output")
	}
}

func TestMergeBaseRouteWinsPrefixConflict(t *testing.T) {
	routes := []gatewayv1alpha1.Route{
		route("root", "/", metav1.NewTime(time.Unix(1, 0))),
		// "/static" normalizes to the same prefix as the base's "/static/".
		route("static", "/static", metav1.NewTime(time.Unix(1, 0))),
		route("users", "/api/users", metav1.NewTime(time.Unix(1, 0))),
	}
	data, conflicts, err := Merge([]byte(testBase), routes, "default/gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %v, want 2", conflicts)
	}
	for _, c := range conflicts {
		if !strings.Contains(c.Winner, "default/gateway") {
			t.Errorf("conflict winner = %q, want the base ConfigMap", c.Winner)
		}
	}
	if strings.Contains(string(data), "upstream-root") || strings.Contains(string(data), "upstream-static") {
		t.Fatalf("conflicting Route upstreams leaked into the merged config:\n%s", data)
	}
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if prefixes := routePrefixes(t, got); strings.Join(prefixes, ",") != "/,/static/,/api/users" {
		t.Errorf("route prefixes = %v", prefixes)
	}
}

func TestMergeWithoutBaseRoutesEmitsRouteList(t *testing.T) {
	data, _, err := Merge([]byte("listen: \":8080\"\n"), nil, "default/gateway")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "routes: []") {
		t.Fatalf("merged config should state an explicit empty route table:\n%s", data)
	}
}

func TestMergeRejectsInvalidBase(t *testing.T) {
	for name, base := range map[string]string{
		"not yaml":          "listen: [",
		"not a mapping":     "- a\n- b\n",
		"routes not a list": "routes: {prefix: /}\n",
		"route not a map":   "routes: [/api]\n",
		"route no prefix":   "routes: [{upstreams: [http://x]}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Merge([]byte(base), nil, "default/gateway"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func routePrefixes(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	list, ok := cfg["routes"].([]any)
	if !ok {
		t.Fatalf("routes = %#v, want a list", cfg["routes"])
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.(map[string]any)["prefix"].(string))
	}
	return out
}
