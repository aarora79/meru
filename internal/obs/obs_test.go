// This file tests the obs package: the loopback rule, the no-op mode before
// Setup, and that each Record function writes the metric names, units,
// attributes and values ARCHITECTURE.md promises. It reads metrics with the
// SDK's ManualReader, spans with the in-memory exporter from tracetest, and
// checks real OTLP export against a local httptest server.

package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/aarora79/meru/internal/config"
)

// useManualReader points the record functions at a fresh meter provider
// whose data the test pulls on demand, and puts the no-op state back when
// the test ends.
func useManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	inst, err := newInstruments(provider.Meter(scopeName))
	if err != nil {
		t.Fatalf("newInstruments: %v", err)
	}
	current.Store(&state{inst: inst})
	t.Cleanup(func() { current.Store(nil) })
	return reader
}

// collect reads every metric from reader, keyed by name.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// attrs turns key/value pairs into an attribute.Set for comparison.
func attrs(kv ...string) attribute.Set {
	var list []attribute.KeyValue
	for i := 0; i+1 < len(kv); i += 2 {
		list = append(list, attribute.String(kv[i], kv[i+1]))
	}
	return attribute.NewSet(list...)
}

// histPoint finds the histogram data point with the given attributes. It
// fails the test when the metric isn't a histogram of type N or has no such
// point. [N int64 | float64] makes it generic over both number types.
func histPoint[N int64 | float64](t *testing.T, m metricdata.Metrics, want attribute.Set) metricdata.HistogramDataPoint[N] {
	t.Helper()
	h, ok := m.Data.(metricdata.Histogram[N])
	if !ok {
		t.Fatalf("%s: data is %T, want a histogram", m.Name, m.Data)
	}
	for _, dp := range h.DataPoints {
		if dp.Attributes.Equals(&want) {
			return dp
		}
	}
	var got []string
	for _, dp := range h.DataPoints {
		got = append(got, dp.Attributes.Encoded(attribute.DefaultEncoder()))
	}
	t.Fatalf("%s: no point with %s; have %v", m.Name, want.Encoded(attribute.DefaultEncoder()), got)
	return metricdata.HistogramDataPoint[N]{}
}

// near reports whether a and b differ by less than a microsecond's worth.
func near(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

// TestLoopbackURL checks which endpoints pass the loopback rule.
func TestLoopbackURL(t *testing.T) {
	tests := []struct {
		endpoint string
		ok       bool
	}{
		{"http://127.0.0.1:4318", true},
		{"http://127.1.2.3:4318", true},
		{"http://localhost:4318", true},
		{"http://LOCALHOST:4318", true},
		{"http://[::1]:4318", true},
		{"https://127.0.0.1:4318/otlp", true},
		{"http://10.0.0.5:4318", false},
		{"http://192.168.1.10:4318", false},
		{"http://example.com:4318", false},
		{"http://0.0.0.0:4318", false},
		{"http://[::]:4318", false},
		{"http://127.0.0.1.example.com:4318", false},
		{"127.0.0.1:4318", false}, // no scheme
		{"grpc://127.0.0.1:4317", false},
		{"http://:4318", false},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			_, err := loopbackURL(context.Background(), tt.endpoint)
			if (err == nil) != tt.ok {
				t.Errorf("loopbackURL(%q) error = %v, want ok=%v", tt.endpoint, err, tt.ok)
			}
		})
	}
}

// TestSetupRefusesNonLoopback checks that Setup fails, and changes nothing,
// for an endpoint off this machine.
func TestSetupRefusesNonLoopback(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	for _, endpoint := range []string{"http://10.1.2.3:4318", "http://example.com:4318", "http://0.0.0.0:4318"} {
		t.Run(endpoint, func(t *testing.T) {
			shutdown, err := Setup(context.Background(), config.Observability{OTLPEndpoint: endpoint})
			if err == nil {
				_ = shutdown(context.Background())
				t.Fatalf("Setup(%q) succeeded, want an error", endpoint)
			}
			if current.Load() != nil {
				t.Errorf("Setup(%q) changed state after refusing", endpoint)
			}
		})
	}
}

// TestSetupRejectsBadInterval checks that a bad metrics_interval fails Setup.
func TestSetupRejectsBadInterval(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	for _, interval := range []string{"soon", "0s", "-5s"} {
		cfg := config.Observability{OTLPEndpoint: "http://127.0.0.1:4318", MetricsInterval: interval}
		if _, err := Setup(context.Background(), cfg); err == nil {
			t.Errorf("Setup with metrics_interval %q succeeded, want an error", interval)
		}
	}
}

// TestNoOpBeforeSetup checks that every function is safe to call before Setup.
func TestNoOpBeforeSetup(t *testing.T) {
	current.Store(nil)
	ctx := context.Background()
	// None of these may panic or allocate instruments before Setup.
	RecordModelCall(ctx, ModelCall{Tier: "main", Model: "m", Operation: "chat", Duration: time.Second})
	RecordTurn(ctx, Turn{Route: "direct", Source: "cli", Outcome: "ok"})
	RecordRoute(ctx, "direct", "ok")
	RecordContextTokens(ctx, "system", 10)
	RecordToolCall(ctx, ToolCallMetric{Kind: "mcp", Server: "s", Tool: "t", Outcome: "ok", Duration: time.Second})
	ActiveStreams(ctx, 1)
	if CaptureContent() {
		t.Error("CaptureContent() = true before Setup, want false")
	}
	_, span := Tracer().Start(ctx, "x")
	defer span.End()
	if span.IsRecording() {
		t.Error("span records before Setup, want a no-op span")
	}
}

// TestSetupWithoutEndpoint checks that an empty endpoint keeps export off but
// still honours capture_content.
func TestSetupWithoutEndpoint(t *testing.T) {
	t.Cleanup(func() {
		current.Store(nil)
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	})
	tests := []struct {
		capture bool
	}{{false}, {true}}
	for _, tt := range tests {
		shutdown, err := Setup(context.Background(), config.Observability{CaptureContent: tt.capture})
		if err != nil {
			t.Fatalf("Setup with no endpoint: %v", err)
		}
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("no-op shutdown: %v", err)
		}
		if got := CaptureContent(); got != tt.capture {
			t.Errorf("CaptureContent() = %v, want %v", got, tt.capture)
		}
		if load() != nil {
			t.Error("instruments exist with no endpoint, want none")
		}
		// Spans record nothing but still carry a trace ID for the log.
		_, span := Tracer().Start(context.Background(), "meru.turn")
		span.End()
		if span.IsRecording() {
			t.Error("span records with no endpoint, want a dropped span")
		}
		if !span.SpanContext().HasTraceID() {
			t.Error("span has no trace ID with no endpoint, want one for the log")
		}
	}
}

// TestRecordModelCall checks the five model-call metrics for a streamed chat.
func TestRecordModelCall(t *testing.T) {
	reader := useManualReader(t)
	RecordModelCall(context.Background(), ModelCall{
		Tier:             "main",
		Model:            "qwen3.8:27b",
		Operation:        "chat",
		Duration:         2 * time.Second,
		TimeToFirstToken: 400 * time.Millisecond,
		Usage: Usage{
			PromptTokens: 120,
			OutputTokens: 50,
			EvalDuration: time.Second,
			LoadDuration: 3 * time.Second,
		},
	})
	got := collect(t, reader)

	units := map[string]string{
		metricTokenUsage:        "{token}",
		metricOperationDuration: "s",
		metricTimeToFirstToken:  "s",
		metricTimePerOutputTok:  "s",
		metricLoadDuration:      "s",
	}
	for name, unit := range units {
		m, ok := got[name]
		if !ok {
			t.Errorf("metric %s missing", name)
			continue
		}
		if m.Unit != unit {
			t.Errorf("%s unit = %q, want %q", name, m.Unit, unit)
		}
	}

	in := histPoint[int64](t, got[metricTokenUsage], attrs(
		keyModel, "qwen3.8:27b", keyTier, "main", keyOperation, "chat", keyTokenType, "input"))
	if in.Sum != 120 || in.Count != 1 {
		t.Errorf("input tokens sum=%d count=%d, want 120 and 1", in.Sum, in.Count)
	}
	out := histPoint[int64](t, got[metricTokenUsage], attrs(
		keyModel, "qwen3.8:27b", keyTier, "main", keyOperation, "chat", keyTokenType, "output"))
	if out.Sum != 50 {
		t.Errorf("output tokens sum=%d, want 50", out.Sum)
	}

	dur := histPoint[float64](t, got[metricOperationDuration], attrs(
		keyModel, "qwen3.8:27b", keyTier, "main", keyOperation, "chat"))
	if !near(dur.Sum, 2) {
		t.Errorf("operation duration = %v, want 2", dur.Sum)
	}
	ttft := histPoint[float64](t, got[metricTimeToFirstToken], attrs(keyModel, "qwen3.8:27b", keyTier, "main"))
	if !near(ttft.Sum, 0.4) {
		t.Errorf("time to first token = %v, want 0.4", ttft.Sum)
	}
	perToken := histPoint[float64](t, got[metricTimePerOutputTok], attrs(keyModel, "qwen3.8:27b", keyTier, "main"))
	if !near(perToken.Sum, 0.02) { // 1 s over 50 tokens
		t.Errorf("time per output token = %v, want 0.02", perToken.Sum)
	}
	cold := histPoint[float64](t, got[metricLoadDuration], attrs(keyModel, "qwen3.8:27b"))
	if !near(cold.Sum, 3) {
		t.Errorf("load duration = %v, want 3", cold.Sum)
	}
	// The one-second target must be a bucket edge, so a panel can count
	// calls under it without guessing.
	if !slices.Contains(ttft.Bounds, 1) {
		t.Errorf("time-to-first-token bounds %v lack 1 s", ttft.Bounds)
	}
}

// TestRecordModelCallSkipsWhatItCannotKnow checks that a call without a first
// token or output tokens records neither.
func TestRecordModelCallSkipsWhatItCannotKnow(t *testing.T) {
	reader := useManualReader(t)
	// A non-streamed embedding call: no first token, no output tokens, and
	// an unknown tier.
	RecordModelCall(context.Background(), ModelCall{
		Tier:      "huge",
		Model:     "nomic-embed-text",
		Operation: "embed",
		Duration:  50 * time.Millisecond,
		Usage:     Usage{PromptTokens: 8},
	})
	got := collect(t, reader)
	for _, name := range []string{metricTimeToFirstToken, metricTimePerOutputTok} {
		if _, ok := got[name]; ok {
			t.Errorf("%s recorded for a call that can't have it", name)
		}
	}
	h := got[metricTokenUsage].Data.(metricdata.Histogram[int64])
	if len(h.DataPoints) != 1 {
		t.Fatalf("token usage has %d points, want only input", len(h.DataPoints))
	}
	histPoint[int64](t, got[metricTokenUsage], attrs(
		keyModel, "nomic-embed-text", keyTier, "other", keyOperation, "embeddings", keyTokenType, "input"))
	// Load duration is recorded on every call, zero included.
	cold := histPoint[float64](t, got[metricLoadDuration], attrs(keyModel, "nomic-embed-text"))
	if cold.Count != 1 || cold.Sum != 0 {
		t.Errorf("load duration count=%d sum=%v, want 1 and 0", cold.Count, cold.Sum)
	}
}

// TestRecordTurnAndRoute checks turn and route metrics, and that unknown
// values become "other".
func TestRecordTurnAndRoute(t *testing.T) {
	reader := useManualReader(t)
	ctx := context.Background()
	RecordTurn(ctx, Turn{Route: "search+tools", Source: "cli", Outcome: "ok", Duration: 3 * time.Second, Iterations: 2})
	RecordTurn(ctx, Turn{Route: "session-1234", Source: "web", Outcome: "boom", Duration: time.Second, Iterations: 1})
	RecordRoute(ctx, "direct", "ok")
	RecordRoute(ctx, "direct", "ok")
	RecordRoute(ctx, "fifth-route", "unsure")
	got := collect(t, reader)

	if u := got[metricTurnDuration].Unit; u != "s" {
		t.Errorf("turn duration unit = %q, want s", u)
	}
	turn := histPoint[float64](t, got[metricTurnDuration], attrs(
		keyRoute, "search+tools", keySource, "cli", keyOutcome, "ok"))
	if !near(turn.Sum, 3) {
		t.Errorf("turn duration = %v, want 3", turn.Sum)
	}
	// Unknown values collapse to "other" instead of making new series.
	histPoint[float64](t, got[metricTurnDuration], attrs(
		keyRoute, "other", keySource, "other", keyOutcome, "other"))
	iter := histPoint[int64](t, got[metricTurnIterations], attrs(keyRoute, "search+tools"))
	if iter.Sum != 2 {
		t.Errorf("iterations = %d, want 2", iter.Sum)
	}

	sum, ok := got[metricRouteDecisions].Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic {
		t.Fatalf("%s is %T, want a monotonic int64 sum (a counter)", metricRouteDecisions, got[metricRouteDecisions].Data)
	}
	counts := map[string]int64{}
	for _, dp := range sum.DataPoints {
		route, _ := dp.Attributes.Value(keyRoute)
		outcome, _ := dp.Attributes.Value(keyOutcome)
		counts[route.AsString()+"/"+outcome.AsString()] = dp.Value
	}
	if counts["direct/ok"] != 2 || counts["other/other"] != 1 || len(counts) != 2 {
		t.Errorf("route decisions = %v, want direct/ok=2 and other/other=1", counts)
	}
}

// TestRecordContextTokensAndStreams checks meru.context.tokens and the
// active-streams up-down counter.
func TestRecordContextTokensAndStreams(t *testing.T) {
	reader := useManualReader(t)
	ctx := context.Background()
	RecordContextTokens(ctx, "history", 900)
	RecordContextTokens(ctx, "/Users/me/notes.md", 5)
	ActiveStreams(ctx, 1)
	ActiveStreams(ctx, 1)
	ActiveStreams(ctx, -1)
	got := collect(t, reader)

	h := histPoint[int64](t, got[metricContextTokens], attrs(keySection, "history"))
	if h.Sum != 900 {
		t.Errorf("history tokens = %d, want 900", h.Sum)
	}
	histPoint[int64](t, got[metricContextTokens], attrs(keySection, "other"))

	streams, ok := got[metricActiveStreams].Data.(metricdata.Sum[int64])
	if !ok || streams.IsMonotonic || len(streams.DataPoints) != 1 {
		t.Fatalf("%s = %+v, want one non-monotonic sum point", metricActiveStreams, got[metricActiveStreams].Data)
	}
	if v := streams.DataPoints[0].Value; v != 1 {
		t.Errorf("active streams = %d, want 1", v)
	}
	if u := got[metricActiveStreams].Unit; u != "{stream}" {
		t.Errorf("active streams unit = %q, want {stream}", u)
	}
}

// TestRecordRetrieval checks each stage lands under its own attribute, in
// seconds, and that an unknown stage becomes "other".
func TestRecordRetrieval(t *testing.T) {
	reader := useManualReader(t)
	ctx := context.Background()
	RecordRetrieval(ctx, "vector", 3*time.Millisecond)
	RecordRetrieval(ctx, "fts", time.Millisecond)
	RecordRetrieval(ctx, "fts", time.Millisecond)
	RecordRetrieval(ctx, "fusion", 50*time.Microsecond)
	RecordRetrieval(ctx, "notes/launch.md", time.Millisecond)
	got := collect(t, reader)

	m := got[metricRetrievalDuration]
	if m.Unit != "s" {
		t.Errorf("unit = %q, want s", m.Unit)
	}
	if h := histPoint[float64](t, m, attrs(keyStage, "vector")); !near(h.Sum, 0.003) {
		t.Errorf("vector sum = %v, want 0.003", h.Sum)
	}
	if h := histPoint[float64](t, m, attrs(keyStage, "fts")); h.Count != 2 {
		t.Errorf("fts count = %d, want 2", h.Count)
	}
	histPoint[float64](t, m, attrs(keyStage, "fusion"))
	histPoint[float64](t, m, attrs(keyStage, "other"))
}

// TestRecordToolCall checks both tool metrics, that a call that never ran
// records no duration, and that a denied call's made-up names and unknown
// values become "other".
func TestRecordToolCall(t *testing.T) {
	reader := useManualReader(t)
	ctx := context.Background()
	RecordToolCall(ctx, ToolCallMetric{Kind: "mcp", Server: "web", Tool: "search", Outcome: "ok", Duration: 300 * time.Millisecond})
	RecordToolCall(ctx, ToolCallMetric{Kind: "mcp", Server: "web", Tool: "search", Outcome: "ok", Duration: 100 * time.Millisecond})
	RecordToolCall(ctx, ToolCallMetric{Kind: "builtin", Server: "meru", Tool: "write_file", Outcome: "declined"})
	RecordToolCall(ctx, ToolCallMetric{Kind: "mcp", Server: "evil", Tool: "rm_rf_12345", Outcome: "denied"})
	RecordToolCall(ctx, ToolCallMetric{Kind: "plugin", Server: "web", Tool: "search", Outcome: "exploded", Duration: time.Second})
	got := collect(t, reader)

	m := got[metricToolCalls]
	if m.Unit != "{call}" {
		t.Errorf("tool calls unit = %q, want {call}", m.Unit)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic {
		t.Fatalf("%s is %T, want a counter", metricToolCalls, m.Data)
	}
	counts := map[string]int64{}
	for _, dp := range sum.DataPoints {
		var parts []string
		for _, k := range []string{keyToolKind, keyToolServer, keyToolName, keyOutcome} {
			v, _ := dp.Attributes.Value(attribute.Key(k))
			parts = append(parts, v.AsString())
		}
		counts[strings.Join(parts, "/")] = dp.Value
	}
	want := map[string]int64{
		"mcp/web/search/ok":                2,
		"builtin/meru/write_file/declined": 1,
		"mcp/other/other/denied":           1,
		"other/web/search/other":           1,
	}
	if len(counts) != len(want) {
		t.Errorf("tool calls = %v, want %v", counts, want)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("tool calls[%s] = %d, want %d", k, counts[k], v)
		}
	}

	d := got[metricToolDuration]
	if d.Unit != "s" {
		t.Errorf("tool duration unit = %q, want s", d.Unit)
	}
	h := histPoint[float64](t, d, attrs(keyToolKind, "mcp", keyToolServer, "web", keyToolName, "search"))
	if h.Count != 2 || !near(h.Sum, 0.4) {
		t.Errorf("web.search duration count=%d sum=%v, want 2 and 0.4", h.Count, h.Sum)
	}
	if n := len(d.Data.(metricdata.Histogram[float64]).DataPoints); n != 2 {
		t.Errorf("tool duration has %d points, want 2 (calls that never ran record none)", n)
	}
}

// sumPoints returns a counter's values, keyed by the values of the given
// attributes joined with "/". It fails the test when m isn't a counter.
func sumPoints(t *testing.T, m metricdata.Metrics, keys ...string) map[string]int64 {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic {
		t.Fatalf("%s is %T, want a counter", m.Name, m.Data)
	}
	out := map[string]int64{}
	for _, dp := range sum.DataPoints {
		var parts []string
		for _, k := range keys {
			v, _ := dp.Attributes.Value(attribute.Key(k))
			parts = append(parts, v.AsString())
		}
		out[strings.Join(parts, "/")] = dp.Value
	}
	return out
}

// TestRecordUsage checks meru.sessions, meru.turn.tokens and
// meru.turn.docs, and that unknown values become "other".
func TestRecordUsage(t *testing.T) {
	reader := useManualReader(t)
	ctx := context.Background()
	RecordSession(ctx, "tui")
	RecordSession(ctx, "tui")
	RecordSession(ctx, "session-7f3a")
	RecordTurnUsage(ctx, TurnUsage{Route: "search", Source: "tui", TokensIn: 1200, TokensOut: 80, Docs: 3})
	RecordTurnUsage(ctx, TurnUsage{Route: "search", Source: "tui", TokensIn: 800, TokensOut: 20, Docs: 1})
	RecordTurnUsage(ctx, TurnUsage{Route: "direct", Source: "cli", TokensIn: 100, TokensOut: 10})
	RecordTurnUsage(ctx, TurnUsage{Route: "notes-a.md", Source: "web", TokensIn: 1, TokensOut: 1})
	got := collect(t, reader)

	if u := got[metricSessions].Unit; u != "{session}" {
		t.Errorf("sessions unit = %q, want {session}", u)
	}
	sessions := sumPoints(t, got[metricSessions], keySource)
	if sessions["tui"] != 2 || sessions["other"] != 1 || len(sessions) != 2 {
		t.Errorf("sessions = %v, want tui=2 and other=1", sessions)
	}

	tokens := sumPoints(t, got[metricTurnTokens], keyTokenType, keyRoute, keySource)
	want := map[string]int64{
		"input/search/tui":   2000,
		"output/search/tui":  100,
		"input/direct/cli":   100,
		"output/direct/cli":  10,
		"input/other/other":  1,
		"output/other/other": 1,
	}
	if len(tokens) != len(want) {
		t.Errorf("turn tokens = %v, want %v", tokens, want)
	}
	for k, v := range want {
		if tokens[k] != v {
			t.Errorf("turn tokens[%s] = %d, want %d", k, tokens[k], v)
		}
	}

	docs := histPoint[int64](t, got[metricTurnDocs], attrs(keyRoute, "search"))
	if docs.Count != 2 || docs.Sum != 4 {
		t.Errorf("search docs count=%d sum=%d, want 2 and 4", docs.Count, docs.Sum)
	}
	// A turn that used no file still counts, with 0.
	if d := histPoint[int64](t, got[metricTurnDocs], attrs(keyRoute, "direct")); d.Count != 1 || d.Sum != 0 {
		t.Errorf("direct docs count=%d sum=%d, want 1 and 0", d.Count, d.Sum)
	}
	histPoint[int64](t, got[metricTurnDocs], attrs(keyRoute, "other"))
}

// TestTracerUsesGlobalProvider checks that Tracer follows the installed
// provider and names Meru as the scope.
func TestTracerUsesGlobalProvider(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(tracenoop.NewTracerProvider()) })

	_, span := Tracer().Start(context.Background(), "meru.turn")
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "meru.turn" {
		t.Fatalf("spans = %v, want one meru.turn", spans)
	}
	if got := spans[0].InstrumentationScope.Name; got != scopeName {
		t.Errorf("scope = %q, want %q", got, scopeName)
	}
}

// TestSetupExportsToLoopback runs the real exporters against a local HTTP
// server and checks that shutdown flushes both signals to the OTLP paths.
func TestSetupExportsToLoopback(t *testing.T) {
	var mu sync.Mutex // guards paths; the exporters post from their own goroutines
	paths := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path] = r.Header.Get("Content-Type")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Cleanup(func() {
		current.Store(nil)
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
	})

	ctx := context.Background()
	shutdown, err := Setup(ctx, config.Observability{
		OTLPEndpoint:    server.URL, // httptest listens on 127.0.0.1
		MetricsInterval: "1h",       // only the shutdown flush should send
		Traces:          true,
		CaptureContent:  true,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !CaptureContent() {
		t.Error("CaptureContent() = false, want true from config")
	}
	RecordRoute(ctx, "direct", "ok")
	_, span := Tracer().Start(ctx, "meru.turn")
	span.End()

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"/v1/metrics", "/v1/traces"} {
		if ct, ok := paths[p]; !ok {
			t.Errorf("nothing posted to %s; got %v", p, paths)
		} else if ct != "application/x-protobuf" {
			t.Errorf("%s content type = %q, want application/x-protobuf", p, ct)
		}
	}
	if load() != nil {
		t.Error("instruments still live after shutdown")
	}
}

// TestBuildVersion checks that a version `make release` stamps in wins over
// the build information, and that a plain build still reports one. It sets
// the variable the linker sets, and puts it back when the test ends.
func TestBuildVersion(t *testing.T) {
	// t.Cleanup runs the function after the test, pass or fail.
	t.Cleanup(func() { releaseVersion = "" })
	releaseVersion = "v0.4.1"
	if got := BuildVersion(); got != "v0.4.1" {
		t.Errorf("BuildVersion = %q, want v0.4.1", got)
	}
	releaseVersion = ""
	if got := BuildVersion(); got == "v0.4.1" || got == "" {
		t.Errorf("BuildVersion without a stamp = %q, want the build's own version", got)
	}
}
