// This file tests what OllamaEngine tells the log and the tracer: one client
// span per HTTP call with its status, debug lines with the call's settings
// and Ollama's counters, and never the text of a message.

package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/aarora79/meru/internal/obs"
)

// lockedBuffer is a bytes.Buffer that many goroutines may write to at once.
// Embedding bytes.Buffer gives lockedBuffer all its methods; Write and
// String below replace two of them with locked versions.
type lockedBuffer struct {
	mu sync.Mutex // guards the buffer
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

// debugLog returns a debug-level logger that writes to the returned buffer,
// with trace IDs added the way merud adds them.
func debugLog() (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(obs.LogHandler(h)), buf
}

// recordSpans installs a tracer provider that keeps every finished span in
// memory, and puts the no-op provider back when the test ends.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(tracenoop.NewTracerProvider()) })
	return rec
}

// attrOf returns the value of key on span, and false when it is missing.
func attrOf(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestCallsLogAndTrace(t *testing.T) {
	stream := `{"message":{"role":"assistant","content":"","thinking":"secret thought"},"done":false}` + "\n" +
		`{"message":{"role":"assistant","content":"secret answer"},"done":false}` + "\n" +
		`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop",` +
		`"load_duration":1000000,"prompt_eval_count":12,"prompt_eval_duration":5000000,"eval_count":4,"eval_duration":200000000}` + "\n"
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, stream}})
	log, buf := debugLog()
	rec := recordSpans(t)
	e, err := NewOllama(f.srv.URL, "-1", "", f.srv.Client(), log)
	if err != nil {
		t.Fatal(err)
	}

	seq, err := e.Stream(context.Background(), []Message{{Role: RoleUser, Content: "secret question"}}, nil,
		Options{Model: "m", MaxTokens: 50})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("stream item: %v", err)
		}
	}

	out := buf.String()
	for _, want := range []string{
		`msg="ollama http" method=POST path=/api/chat status=200`,
		"model=m stream=true num_predict=50 logprobs=false",
		`msg="ollama first token" model=m`,
		"thinking_chunks=1",
		`msg="ollama done" model=m`,
		"prompt_tokens=12 output_tokens=4 load_ms=1 prompt_eval_ms=5 eval_ms=200 tokens_per_s=20 done_reason=stop thinking_chunks=1",
		"trace_id=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Errorf("log holds message text:\n%s", out)
	}

	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "POST /api/chat" {
		t.Fatalf("spans = %d, want one POST /api/chat", len(spans))
	}
	if v, _ := attrOf(spans[0], "http.response.status_code"); v.AsInt64() != 200 {
		t.Errorf("status attribute = %v, want 200", v.AsInt64())
	}
	for _, kv := range spans[0].Attributes() {
		if strings.Contains(kv.Value.String(), "secret") {
			t.Errorf("span attribute %s holds message text", kv.Key)
		}
	}
}

func TestFailedCallLogsOllamaError(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/chat": {404, `{"error":"model 'nope' not found"}`}})
	log, buf := debugLog()
	rec := recordSpans(t)
	e, err := NewOllama(f.srv.URL, "", "", f.srv.Client(), log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Generate(context.Background(), nil, nil, Options{Model: "nope"}); err == nil {
		t.Fatal("Generate succeeded, want a 404 error")
	}
	out := buf.String()
	if !strings.Contains(out, `msg="ollama error" path=/api/chat`) || !strings.Contains(out, "model 'nope' not found") {
		t.Errorf("log lacks the error line with Ollama's message:\n%s", out)
	}
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("span status = %v, want Error", spans[0].Status())
	}
	if v, _ := attrOf(spans[0], "http.response.status_code"); v.AsInt64() != 404 {
		t.Errorf("status attribute = %v, want 404", v.AsInt64())
	}
}
