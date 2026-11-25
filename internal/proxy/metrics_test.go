package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"gateway/internal/config"
	"gateway/internal/proxy"
)

func twoUpstreamConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{Routes: []config.Route{{
		Prefix:    "/api",
		Upstreams: []string{namedUpstream(t, "a").URL, namedUpstream(t, "b").URL},
	}}}
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// counterValue sums the datapoints of a counter matching route and, when
// status is non-zero, the response status.
func counterValue(t *testing.T, m metricdata.Metrics, route string, status int64) int64 {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s is %T, want Sum[int64]", m.Name, m.Data)
	}
	var total int64
	for _, dp := range sum.DataPoints {
		if v, _ := dp.Attributes.Value("http.route"); v.AsString() != route {
			continue
		}
		if status != 0 {
			if v, _ := dp.Attributes.Value("http.response.status_code"); v.AsInt64() != status {
				continue
			}
		}
		total += dp.Value
	}
	return total
}

func TestRouterRecordsMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	cfg := &config.Config{Routes: []config.Route{
		{Prefix: "/api", Upstreams: []string{namedUpstream(t, "a").URL},
			RateLimit: &config.RateLimit{RequestsPerSecond: 0.001, Burst: 2}},
	}}
	r, err := proxy.NewRouter(cfg, nil, proxy.WithMeterProvider(mp))
	if err != nil {
		t.Fatal(err)
	}

	for range 4 { // 2 allowed, 2 limited
		get(t, r, "/api/x")
	}
	get(t, r, "/nowhere") // unmatched

	m := collect(t, reader)

	reqs := m["gateway.requests"]
	if got := counterValue(t, reqs, "/api", 200); got != 2 {
		t.Errorf("/api 200 requests = %d, want 2", got)
	}
	if got := counterValue(t, reqs, "/api", 429); got != 2 {
		t.Errorf("/api 429 requests = %d, want 2", got)
	}
	if got := counterValue(t, reqs, "unmatched", 404); got != 1 {
		t.Errorf("unmatched 404 requests = %d, want 1", got)
	}
	if got := counterValue(t, m["gateway.ratelimited"], "/api", 0); got != 2 {
		t.Errorf("rate limited = %d, want 2", got)
	}

	hist, ok := m["gateway.request.duration"].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("duration is %T, want Histogram[float64]", m["gateway.request.duration"].Data)
	}
	var observations uint64
	for _, dp := range hist.DataPoints {
		observations += dp.Count
	}
	if observations != 5 {
		t.Errorf("duration observations = %d, want 5", observations)
	}
}

func TestHealthyUpstreamGauge(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	r, err := proxy.NewRouter(twoUpstreamConfig(t), nil, proxy.WithMeterProvider(mp))
	if err != nil {
		t.Fatal(err)
	}

	healthy := func() int64 {
		g, ok := collect(t, reader)["gateway.upstreams.healthy"].Data.(metricdata.Gauge[int64])
		if !ok || len(g.DataPoints) != 1 {
			t.Fatalf("gauge = %+v", g)
		}
		return g.DataPoints[0].Value
	}

	if got := healthy(); got != 2 {
		t.Fatalf("healthy = %d, want 2", got)
	}
	r.Upstreams()[0].SetHealthy(false)
	if got := healthy(); got != 1 {
		t.Errorf("after one down, healthy = %d, want 1", got)
	}
	r.Upstreams()[1].SetHealthy(false)
	if got := healthy(); got != 0 {
		t.Errorf("after both down, healthy = %d, want 0", got)
	}
}

func TestStatusRecorderKeepsFlushing(t *testing.T) {
	// Streaming responses must still flush through the metrics wrapper.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: 1\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	r, err := proxy.NewRouter(&config.Config{Routes: []config.Route{
		{Prefix: "/", Upstreams: []string{upstream.URL}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !rec.Flushed || rec.Code != http.StatusOK {
		t.Errorf("flushed=%v code=%d, want flushed 200", rec.Flushed, rec.Code)
	}
}
