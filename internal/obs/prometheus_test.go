// This file tests the /metrics page: the text format writeText produces
// for each kind of metric, that it leaves out a kind it can't write, the
// Host, method and path checks in MetricsHandler, Setup with /metrics on
// and no OTLP endpoint, and the hourly limit on export-error lines.

package obs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/aarora79/meru/internal/config"
)

// quiet is a logger that drops every line.
var quiet = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

// text runs writeText over rm and returns what it wrote.
func text(res *resource.Resource, rm metricdata.ResourceMetrics) string {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	writeText(w, res, rm, quiet)
	_ = w.Flush()
	return buf.String()
}

// TestPrometheusText records a counter, an up-down counter, a gauge and a
// histogram through a real meter provider, and checks every line of the
// text format: names with _total and _seconds, labels sorted and escaped,
// series sorted, and a histogram's buckets counted up to +Inf.
func TestPrometheusText(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter("test")
	ctx := context.Background()

	calls, _ := meter.Int64Counter("meru.tool.calls", metric.WithDescription("Tool calls."))
	calls.Add(ctx, 2, metric.WithAttributes(attribute.String("meru.outcome", "ok"), attribute.String("gen_ai.tool.name", "web_fetch")))
	calls.Add(ctx, 1, metric.WithAttributes(attribute.String("meru.outcome", "error"), attribute.String("gen_ai.tool.name", `say "hi"`)))

	streams, _ := meter.Int64UpDownCounter("meru.streams.active", metric.WithUnit("{stream}"))
	streams.Add(ctx, 3)

	gauge, _ := meter.Float64Gauge("meru.index.ratio", metric.WithUnit("1"))
	gauge.Record(ctx, 0.5)

	turns, _ := meter.Float64Histogram("meru.turn.duration", metric.WithUnit("s"),
		metric.WithDescription("Turn time.\nTwo lines."), metric.WithExplicitBucketBoundaries(1, 5))
	route := metric.WithAttributes(attribute.String("meru.route", "direct"))
	turns.Record(ctx, 0.5, route)
	turns.Record(ctx, 2, route)
	turns.Record(ctx, 9, route)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	res := resource.NewSchemaless(attribute.String("service.name", "merud"))
	got := text(res, rm)

	want := `# HELP target_info The program that reports these metrics.
# TYPE target_info gauge
target_info{service_name="merud"} 1
# TYPE meru_index_ratio gauge
meru_index_ratio 0.5
# TYPE meru_streams_active gauge
meru_streams_active 3
# HELP meru_tool_calls_total Tool calls.
# TYPE meru_tool_calls_total counter
meru_tool_calls_total{gen_ai_tool_name="say \"hi\"",meru_outcome="error"} 1
meru_tool_calls_total{gen_ai_tool_name="web_fetch",meru_outcome="ok"} 2
# HELP meru_turn_duration_seconds Turn time.\nTwo lines.
# TYPE meru_turn_duration_seconds histogram
meru_turn_duration_seconds_bucket{meru_route="direct",le="1"} 1
meru_turn_duration_seconds_bucket{meru_route="direct",le="5"} 2
meru_turn_duration_seconds_bucket{meru_route="direct",le="+Inf"} 3
meru_turn_duration_seconds_sum{meru_route="direct"} 11.5
meru_turn_duration_seconds_count{meru_route="direct"} 3
`
	if got != want {
		t.Errorf("text format:\n%s\nwant:\n%s", got, want)
	}
}

// TestPrometheusSkipsUnknown checks that an exponential histogram, which
// the text format here doesn't write, is left out, and the metrics around
// it still come out.
func TestPrometheusSkipsUnknown(t *testing.T) {
	rm := metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Metrics: []metricdata.Metrics{
		{Name: "a.count", Data: metricdata.Sum[int64]{IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 4}}}},
		{Name: "b.expo", Data: metricdata.ExponentialHistogram[float64]{DataPoints: []metricdata.ExponentialHistogramDataPoint[float64]{{Count: 1}}}},
		{Name: "c.level", Data: metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 7}}}},
	}}}}
	got := text(nil, rm)
	want := "# TYPE a_count_total counter\na_count_total 4\n# TYPE c_level gauge\nc_level 7\n"
	if got != want {
		t.Errorf("text format:\n%s\nwant:\n%s", got, want)
	}
}

func TestPromName(t *testing.T) {
	tests := []struct {
		name, unit, kind, want string
	}{
		{"meru.tool.calls", "{call}", "counter", "meru_tool_calls_total"},
		{"meru.turn.duration", "s", "histogram", "meru_turn_duration_seconds"},
		{"gen_ai.server.time_to_first_token", "s", "histogram", "gen_ai_server_time_to_first_token_seconds"},
		{"meru.index.bytes", "By", "gauge", "meru_index_bytes"},
		{"meru.size", "By", "gauge", "meru_size_bytes"},
		{"9lives", "", "gauge", "_lives"},
		{"meru.errors_total", "", "counter", "meru_errors_total"},
	}
	for _, tt := range tests {
		if got := promName(tt.name, tt.unit, tt.kind); got != tt.want {
			t.Errorf("promName(%q, %q, %q) = %q, want %q", tt.name, tt.unit, tt.kind, got, tt.want)
		}
	}
}

// TestMetricsHandlerHost checks the checks MetricsHandler makes before it
// collects: a Host naming this machine gets the page, any other Host gets
// 403, a method other than GET or HEAD gets 405, and any other path 404.
func TestMetricsHandlerHost(t *testing.T) {
	useManualReader(t)
	s := current.Load()
	s.manual = sdkmetric.NewManualReader()
	sdkmetric.NewMeterProvider(sdkmetric.WithReader(s.manual))
	h := MetricsHandler(quiet)

	tests := []struct {
		name, method, host, path string
		want                     int
	}{
		{"ipv4", http.MethodGet, "127.0.0.1:9464", "/metrics", http.StatusOK},
		{"localhost", http.MethodGet, "localhost:9464", "/metrics", http.StatusOK},
		{"ipv6", http.MethodGet, "[::1]:9464", "/metrics", http.StatusOK},
		{"head", http.MethodHead, "127.0.0.1:9464", "/metrics", http.StatusOK},
		{"another name", http.MethodGet, "evil.example:9464", "/metrics", http.StatusForbidden},
		{"no host", http.MethodGet, "", "/metrics", http.StatusForbidden},
		{"lan address", http.MethodGet, "192.168.1.5:9464", "/metrics", http.StatusForbidden},
		{"post", http.MethodPost, "127.0.0.1:9464", "/metrics", http.StatusMethodNotAllowed},
		{"other path", http.MethodGet, "127.0.0.1:9464", "/", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://127.0.0.1:9464"+tt.path, nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusOK && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
				t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestSetupPrometheusOnly checks Setup with /metrics on and no OTLP
// endpoint: the instruments record, and the page shows what they recorded.
func TestSetupPrometheusOnly(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	shutdown, err := Setup(context.Background(), config.Observability{Prometheus: "127.0.0.1:9464"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	if load() == nil {
		t.Fatal("no instruments with /metrics on")
	}
	RecordTurn(context.Background(), Turn{Route: "direct", Source: "cli", Outcome: "ok", Duration: 2 * time.Second, Iterations: 1})

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9464/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler(quiet).ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`target_info{service_name="merud"`, "meru_turn_duration_seconds_count{", "} 1\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
}

// TestMetricsHandlerOff checks that the page is 404 when Setup made no
// reader for it.
func TestMetricsHandlerOff(t *testing.T) {
	useManualReader(t)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9464/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler(quiet).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestExportErrorsOncePerHour checks that failed OTLP pushes write one log
// line an hour, not one per push.
func TestExportErrorsOncePerHour(t *testing.T) {
	var buf bytes.Buffer
	e := newExportErrors("http://127.0.0.1:4318", slog.New(slog.NewTextHandler(&buf, nil)))
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }

	push := errors.New("connection refused")
	e.Handle(push)
	now = now.Add(10 * time.Second)
	e.Handle(push)
	now = now.Add(59 * time.Minute)
	e.Handle(push)
	if n := strings.Count(buf.String(), "OTLP push failed"); n != 1 {
		t.Errorf("%d lines within the hour, want 1:\n%s", n, buf.String())
	}
	now = now.Add(time.Minute)
	e.Handle(push)
	if n := strings.Count(buf.String(), "OTLP push failed"); n != 2 {
		t.Errorf("%d lines after the hour, want 2:\n%s", n, buf.String())
	}
}
