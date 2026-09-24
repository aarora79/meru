// This file tests the log and span helpers: the trace_id the log handler
// adds, Preview's cut, and how EndSpanErr and ChatResult mark a span.

package obs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TestLogHandlerAddsTraceID checks that a line logged with a span in its
// context gains that span's trace ID, and that one without a span, or logged
// without a context, passes through unchanged.
func TestLogHandlerAddsTraceID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(LogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	// With keeps working through the wrapper.
	log = log.With("part", "test")

	provider := sdktrace.NewTracerProvider()
	ctx, span := provider.Tracer("t").Start(context.Background(), "x")
	defer span.End()
	id := span.SpanContext().TraceID().String()

	log.DebugContext(ctx, "with span")
	log.Debug("without context")
	log.InfoContext(context.Background(), "no span")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "part=test trace_id="+id) {
		t.Errorf("line with span = %q, want part=test and trace_id=%s", lines[0], id)
	}
	for _, l := range lines[1:] {
		if strings.Contains(l, "trace_id") {
			t.Errorf("line without span has a trace_id: %q", l)
		}
	}
}

// TestLogHandlerRespectsLevel checks that the wrapper keeps the inner
// handler's level, so debug lines cost nothing at info level.
func TestLogHandlerRespectsLevel(t *testing.T) {
	h := LogHandler(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug enabled at info level")
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info disabled at info level")
	}
}

func TestPreview(t *testing.T) {
	long := strings.Repeat("é", 250) // two bytes each, so a byte cut would split one
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"short", "short"},
		{strings.Repeat("a", 200), strings.Repeat("a", 200)},
		{strings.Repeat("a", 201), strings.Repeat("a", 200) + "…"},
		{long, strings.Repeat("é", 200) + "…"},
	}
	for _, tt := range tests {
		if got := Preview(tt.in); got != tt.want {
			t.Errorf("Preview(%d chars) = %d chars, want %d", len([]rune(tt.in)), len([]rune(got)), len([]rune(tt.want)))
		}
	}
}

// TestEndSpanErr checks the three endings: nil leaves the span alone, a
// cancel adds a "cancelled" event without an error status, and any other
// error records an exception and sets the status.
func TestEndSpanErr(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		ctx        context.Context
		err        error
		wantEvent  string
		wantStatus codes.Code
	}{
		{"nil", context.Background(), nil, "", codes.Unset},
		{"ctx cancelled", cancelled, errors.New("read: closed"), "cancelled", codes.Unset},
		{"wraps Canceled", context.Background(), fmt.Errorf("stream: %w", context.Canceled), "cancelled", codes.Unset},
		{"failure", context.Background(), errors.New("boom"), "exception", codes.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			_, span := provider.Tracer("t").Start(context.Background(), "x")
			EndSpanErr(tt.ctx, span, tt.err)
			span.End()
			got := rec.Ended()[0]
			var event string
			if evs := got.Events(); len(evs) > 0 {
				event = evs[0].Name
			}
			if event != tt.wantEvent || got.Status().Code != tt.wantStatus {
				t.Errorf("event %q status %v, want %q %v", event, got.Status().Code, tt.wantEvent, tt.wantStatus)
			}
		})
	}
}

// useProvider installs provider as the global tracer provider for one test,
// and puts the no-op provider back when the test ends.
func useProvider(t *testing.T, provider *sdktrace.TracerProvider) {
	t.Helper()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(tracenoop.NewTracerProvider()) })
}

// TestChatSpan checks the attributes StartChat and ChatResult set.
func TestChatSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	useProvider(t, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))

	_, span := StartChat(context.Background(), Chat{Tier: "fast", Model: "m", MaxTokens: 1})
	ChatResult(span, Usage{
		PromptTokens: 90, OutputTokens: 1, LoadDuration: 3 * time.Millisecond,
		PromptEvalDuration: 40 * time.Millisecond, EvalDuration: 2 * time.Millisecond,
	}, "length")
	span.End()

	got := rec.Ended()[0]
	if got.Name() != "gen_ai.chat" {
		t.Errorf("name = %q, want gen_ai.chat", got.Name())
	}
	want := map[attribute.Key]attribute.Value{
		"gen_ai.operation.name":          attribute.StringValue("chat"),
		"gen_ai.request.model":           attribute.StringValue("m"),
		"gen_ai.request.max_tokens":      attribute.IntValue(1),
		"meru.tier":                      attribute.StringValue("fast"),
		"gen_ai.usage.input_tokens":      attribute.IntValue(90),
		"gen_ai.usage.output_tokens":     attribute.IntValue(1),
		"meru.ollama.load_ms":            attribute.Int64Value(3),
		"meru.ollama.prompt_eval_ms":     attribute.Int64Value(40),
		"meru.ollama.eval_ms":            attribute.Int64Value(2),
		"gen_ai.response.finish_reasons": attribute.StringSliceValue([]string{"length"}),
	}
	have := map[attribute.Key]attribute.Value{}
	for _, kv := range got.Attributes() {
		have[kv.Key] = kv.Value
	}
	for k, v := range want {
		if have[k] != v {
			t.Errorf("%s = %v, want %v", k, have[k].String(), v.String())
		}
	}
}
