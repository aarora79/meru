// This file checks the fake's /v1/chat/completions against Meru's own
// OpenAIEngine, so a test that points an endpoint at the fake talks to a
// server the engine reads the way it reads a real one.

package fakeollama_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

func TestOpenAIRoute(t *testing.T) {
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue("m",
		fakeollama.Reply{Text: "one two three"},
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "datetime", Arguments: map[string]any{"zone": "UTC"}}}},
		fakeollama.Reply{Text: "a b c d e"},
	)
	e, err := engine.NewOpenAI(engine.EndpointConfig{Name: "fake", URL: srv.URL + "/v1", Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := []engine.Message{{Role: engine.RoleUser, Content: "q"}}

	read := func(opts engine.Options) (string, engine.Delta) {
		t.Helper()
		seq, err := e.Stream(ctx, q, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		var last engine.Delta
		for d, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			text.WriteString(d.Text)
			last = d
		}
		return text.String(), last
	}
	if text, last := read(engine.Options{}); text != "one two three" || last.DoneReason != "stop" || last.Usage.OutputTokens != 3 {
		t.Errorf("text %q, last %+v", text, last)
	}
	if _, last := read(engine.Options{}); len(last.ToolCalls) != 1 || string(last.ToolCalls[0].Arguments) != `{"zone":"UTC"}` {
		t.Errorf("tool calls = %+v", last.ToolCalls)
	}
	if text, last := read(engine.Options{MaxTokens: 2}); text != "a b " || last.DoneReason != "length" {
		t.Errorf("capped: text %q, reason %q", text, last.DoneReason)
	}
	if n := len(srv.Requests("/v1/chat/completions")); n != 3 {
		t.Errorf("recorded %d requests, want 3", n)
	}
}
