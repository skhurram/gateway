package controller

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gatewayv1alpha1 "gateway/controller/api/v1alpha1"
)

func TestRender(t *testing.T) {
	old := metav1.NewTime(time.Unix(1, 0))
	recent := metav1.NewTime(time.Unix(2, 0))
	tests := []struct {
		name      string
		routes    []gatewayv1alpha1.Route
		wantRoute []string
		conflicts int
	}{
		{
			name: "sorts by normalized prefix and name",
			routes: []gatewayv1alpha1.Route{
				route("z", "/z", old),
				route("api", "/api/", old),
			},
			wantRoute: []string{"    - prefix: /api", "    - prefix: /z"},
		},
		{
			name: "oldest duplicate wins",
			routes: []gatewayv1alpha1.Route{
				route("new", "/api", recent),
				route("old", "/api", old),
			},
			wantRoute: []string{"    - prefix: /api"},
			conflicts: 1,
		},
		{
			name: "empty route list is deterministic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, conflicts, err := Render(tt.routes, RenderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			again, againConflicts, err := Render(tt.routes, RenderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != string(again) {
				t.Fatal("identical inputs produced different output")
			}
			if len(conflicts) != tt.conflicts || len(againConflicts) != tt.conflicts {
				t.Fatalf("conflicts = %d, want %d", len(conflicts), tt.conflicts)
			}
			offset := 0
			for _, line := range tt.wantRoute {
				index := indexFrom(string(data), line, offset)
				if index < 0 {
					t.Fatalf("missing ordered line %q in:\n%s", line, data)
				}
				offset = index + len(line)
			}
		})
	}
}

func TestRenderGolden(t *testing.T) {
	stamp := metav1.NewTime(time.Unix(1, 0))
	routes := []gatewayv1alpha1.Route{
		route("orders", "/api/orders", stamp),
		route("users", "/api/users", stamp),
	}
	routes[0].Spec.RateLimit = &gatewayv1alpha1.RateLimit{
		RequestsPerSecond: 25,
		Burst:             50,
	}
	data, conflicts, err := Render(routes, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v", conflicts)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(golden) {
		t.Fatalf("rendered config differs from golden:\n--- got ---\n%s--- want ---\n%s", data, golden)
	}
}

func route(name, prefix string, created metav1.Time) gatewayv1alpha1.Route {
	return gatewayv1alpha1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: created,
		},
		Spec: gatewayv1alpha1.RouteSpec{
			Prefix:    prefix,
			Upstreams: []string{"http://upstream-" + name + ":8080"},
		},
	}
}

func indexFrom(value, substring string, offset int) int {
	for i := offset; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return i
		}
	}
	return -1
}
