// This file tests the span each call records and the pieces Call uses to
// read an agent's reply.

package a2a

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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

	ta := startAgent(t, agentOptions{execute: completeWith("private answer")})
	c := newClient(t, AgentConfig{URL: ta.srv.URL, Allow: []string{"summarize"}})
	ctx := context.Background()
	if _, err := c.Call(ctx, "a2a.research.summarize", message("private words")); err != nil {
		t.Fatalf("Call: %v", err)
	}
	_, _ = c.Call(ctx, "a2a.research.delete_all", message("go"))

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(spans))
	}
	ok, denied := spans[0], spans[1]
	if ok.Name() != "invoke_agent research" {
		t.Errorf("span name = %q, want %q", ok.Name(), "invoke_agent research")
	}
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range ok.Attributes() {
		attrs[kv.Key] = kv.Value
	}
	want := map[attribute.Key]string{
		"gen_ai.operation.name": "invoke_agent",
		"gen_ai.agent.name":     "research",
		"meru.a2a.skill":        "summarize",
		"meru.tool.server":      "research",
		"meru.a2a.task.state":   "completed",
		"server.address":        "127.0.0.1",
	}
	for k, v := range want {
		if got := attrs[k].AsString(); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, kv := range ok.Attributes() {
		if s := kv.Value.String(); strings.Contains(s, "private") {
			t.Errorf("span attribute %s holds message text %q without capture_content", kv.Key, s)
		}
	}

	var errType string
	for _, kv := range denied.Attributes() {
		if kv.Key == "error.type" {
			errType = kv.Value.AsString()
		}
	}
	if errType != "denied" {
		t.Errorf("denied call error.type = %q, want denied", errType)
	}
}

func TestPartsText(t *testing.T) {
	raw := sdk.NewRawPart([]byte{1, 2, 3})
	raw.MediaType = "image/png"
	parts := sdk.ContentParts{
		sdk.NewTextPart("hello"),
		sdk.NewDataPart(map[string]any{"n": 1}),
		sdk.NewFileURLPart("https://files.example.com/a.pdf", "application/pdf"),
		raw,
		nil,
	}
	want := "hello\n{\"n\":1}\n[file https://files.example.com/a.pdf]\n[file image/png, 3 bytes]"
	if got := partsText(parts); got != want {
		t.Errorf("partsText() = %q, want %q", got, want)
	}
}

func TestAnswerWithoutTextOrReason(t *testing.T) {
	done := &answer{state: sdk.TaskStateCompleted}
	if res := done.result(); res.IsError || !strings.Contains(res.Text, "without returning any text") {
		t.Errorf("empty completed task: %+v", res)
	}
	failed := &answer{state: sdk.TaskStateFailed}
	if res := failed.result(); !res.IsError || !strings.Contains(res.Text, "gave no reason") {
		t.Errorf("failed task with no reason: %+v", res)
	}
	// A completed task whose only text sits in its status message returns
	// that text.
	status := &answer{state: sdk.TaskStateCompleted, status: "all done"}
	if res := status.result(); res.Text != "all done" {
		t.Errorf("status-only task: %+v", res)
	}
}
