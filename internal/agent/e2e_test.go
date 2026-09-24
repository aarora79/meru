// This file runs whole questions in one process: the rpc server and the
// agent on a real Unix socket, and the rpc client asking. The first test
// uses the fake engine; the second runs the real OllamaEngine against the
// fake Ollama for a turn with a tool round. They check what merud and meru
// would see, without Ollama.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// serveOnSocket serves a on a fresh Unix socket until the test ends and
// returns the socket's path.
func serveOnSocket(t *testing.T, a *Agent) string {
	t.Helper()
	// A short socket path: macOS caps them at 104 bytes.
	sockDir, err := os.MkdirTemp("", "meru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "merud.sock")

	ctx, cancel := context.WithCancel(context.Background())
	ln, err := rpc.Listen(ctx, sock)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(ctx, ln, a.Handle, quietLog()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return sock
}

func TestEndToEnd(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{pieces: []string{"The answer ", "is 42."}, usage: engine.Usage{PromptTokens: 20, OutputTokens: 4}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, nil, nil, quietLog())
	sock := serveOnSocket(t, a)

	// Ping first, as meru ping would.
	for ev, err := range rpc.Do(context.Background(), sock, rpc.Request{Op: rpc.OpPing}, nil) {
		if err != nil || ev.Type != rpc.EventDone {
			t.Fatalf("ping: event %+v, err %v", ev, err)
		}
	}

	var answer strings.Builder
	var session, route string
	var last rpc.EventType
	for ev, err := range rpc.Do(context.Background(), sock, rpc.Request{Op: rpc.OpAsk, Text: "what is the answer?", Source: rpc.SourceCLI}, nil) {
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		switch ev.Type {
		case rpc.EventSession:
			session = ev.Session
		case rpc.EventRoute:
			route = ev.Route
		case rpc.EventToken:
			answer.WriteString(ev.Text)
		case rpc.EventError:
			t.Fatalf("error event: %s", ev.Error)
		}
		last = ev.Type
	}
	if last != rpc.EventDone {
		t.Errorf("last event = %s, want done", last)
	}
	if got := answer.String(); got != "The answer is 42." {
		t.Errorf("answer = %q", got)
	}
	if route != "direct" {
		t.Errorf("route = %q, want direct", route)
	}

	lines := readLines(t, cfg, session)
	if len(lines) != 2 {
		t.Fatalf("transcript has %d lines, want 2", len(lines))
	}
	if lines[0].Text != "what is the answer?" || lines[1].Text != "The answer is 42." || lines[1].TokensOut != 4 {
		t.Errorf("transcript = %+v", lines)
	}
}

// TestEndToEndToolRound asks a question whose answer takes one tool call.
// The fake Ollama answers the first chat request with the call and the
// second with text, so the test also checks what Meru sends Ollama in the
// second request: the call and its result.
func TestEndToEndToolRound(t *testing.T) {
	cfg := testConfig(t)
	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "weather.now", Arguments: map[string]any{"city": "Paris"}}}},
		fakeollama.Reply{Text: "It is sunny in Paris."},
	)
	eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := &fakeTools{
		specs:   []engine.ToolSpec{spec("weather.now")},
		results: map[string]fakeResult{"weather.now": {text: "sunny, 21C"}},
	}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())
	sock := serveOnSocket(t, a)

	var got []rpc.EventType
	var answer strings.Builder
	var call, result *rpc.ToolEvent
	for ev, err := range rpc.Do(context.Background(), sock, rpc.Request{Op: rpc.OpAsk, Text: "weather in Paris?"}, nil) {
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		got = append(got, ev.Type)
		switch ev.Type {
		case rpc.EventToken:
			answer.WriteString(ev.Text)
		case rpc.EventToolCall:
			call = ev.Tool
		case rpc.EventToolResult:
			result = ev.Tool
		case rpc.EventError:
			t.Fatalf("error event: %s", ev.Error)
		}
	}
	// Collapse the token run: the fake streams one word at a time.
	got = slices.Compact(got)
	want := []rpc.EventType{rpc.EventSession, rpc.EventRoute, rpc.EventToolCall, rpc.EventToolResult, rpc.EventToken, rpc.EventDone}
	if !slices.Equal(got, want) {
		t.Errorf("events = %v\nwant %v", got, want)
	}
	if call == nil || call.Name != "weather.now" || call.Kind != "mcp" || string(call.Args) != `{"city":"Paris"}` {
		t.Errorf("tool_call = %+v", call)
	}
	if result == nil || result.Outcome != "ok" {
		t.Errorf("tool_result = %+v", result)
	}
	if answer.String() != "It is sunny in Paris." {
		t.Errorf("answer = %q", answer.String())
	}

	chats := srv.Requests("/api/chat")
	if len(chats) != 2 {
		t.Fatalf("fake Ollama got %d chat requests, want 2", len(chats))
	}
	// The second request carries the assistant's call and the tool's
	// result, and still offers the tool.
	var body struct {
		Messages []struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolName  string            `json:"tool_name"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(chats[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	n := len(body.Messages)
	if n < 2 {
		t.Fatalf("second request has %d messages", n)
	}
	if m := body.Messages[n-2]; m.Role != "assistant" || len(m.ToolCalls) != 1 {
		t.Errorf("second last message = %+v, want the assistant's tool call", m)
	}
	if m := body.Messages[n-1]; m.Role != "tool" || m.ToolName != "weather.now" || m.Content != "sunny, 21C" {
		t.Errorf("last message = %+v, want the tool result", m)
	}
	if len(body.Tools) != 1 {
		t.Errorf("second request offers %d tools, want 1", len(body.Tools))
	}
}
