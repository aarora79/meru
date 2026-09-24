// This file checks the span each call records: its name and attributes
// follow the OpenTelemetry MCP conventions, and it carries no argument or
// result text while capture_content is off.

package mcp

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCallSpans(t *testing.T) {
	// A SpanRecorder keeps every finished span in memory. Installing it as
	// the global provider makes obs.Tracer hand out recording tracers for
	// this test; Cleanup puts the old provider back.
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	var calls atomic.Int32
	p := openPool(t, ServerConfig{Name: "t", Command: "unused", Allow: allTestTools()}, nil, memoryDial(t, &calls))
	ctx := context.Background()
	_, _ = p.Call(ctx, "t.echo", json.RawMessage(`{"text":"private words"}`))
	_, _ = p.Call(ctx, "t.fail", nil)
	_, _ = p.Call(ctx, "t.secret", nil)

	tests := []struct {
		spanName    string
		wantAllowed bool
		wantErrType string // "" means no error.type attribute
	}{
		{"tools/call echo", true, ""},
		{"tools/call fail", true, "tool_error"},
		{"tools/call secret", false, "denied"},
	}
	spans := rec.Ended()
	if len(spans) != len(tests) {
		t.Fatalf("recorded %d spans, want %d", len(spans), len(tests))
	}
	for i, tt := range tests {
		s := spans[i]
		if s.Name() != tt.spanName {
			t.Errorf("span %d name = %q, want %q", i, s.Name(), tt.spanName)
		}
		attrs := map[attribute.Key]attribute.Value{}
		for _, kv := range s.Attributes() {
			attrs[kv.Key] = kv.Value
		}
		if attrs["mcp.method.name"].AsString() != "tools/call" ||
			attrs["gen_ai.operation.name"].AsString() != "execute_tool" ||
			attrs["meru.tool.server"].AsString() != "t" {
			t.Errorf("%s: attributes %v miss the MCP convention names", tt.spanName, attrs)
		}
		if attrs["meru.tool.allowed"].AsBool() != tt.wantAllowed {
			t.Errorf("%s: meru.tool.allowed = %v, want %v", tt.spanName, attrs["meru.tool.allowed"].AsBool(), tt.wantAllowed)
		}
		if got := attrs["error.type"].AsString(); got != tt.wantErrType {
			t.Errorf("%s: error.type = %q, want %q", tt.spanName, got, tt.wantErrType)
		}
		if tt.wantErrType != "" && s.Status().Code != codes.Error {
			t.Errorf("%s: status = %v, want Error", tt.spanName, s.Status().Code)
		}
		for _, k := range []attribute.Key{"gen_ai.tool.call.arguments", "gen_ai.tool.call.result"} {
			if _, ok := attrs[k]; ok {
				t.Errorf("%s: carries %s with capture_content off", tt.spanName, k)
			}
		}
	}
}
