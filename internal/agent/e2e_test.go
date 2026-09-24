// This file runs a whole question in one process: the rpc server and the
// agent over a fake engine on a real Unix socket, and the rpc client asking.
// It checks what merud and meru would see, without Ollama.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

func TestEndToEnd(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{pieces: []string{"The answer ", "is 42."}, usage: engine.Usage{PromptTokens: 20, OutputTokens: 4}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, quietLog())

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

	// Ping first, as meru ping would.
	for ev, err := range rpc.Do(context.Background(), sock, rpc.Request{Op: rpc.OpPing}) {
		if err != nil || ev.Type != rpc.EventDone {
			t.Fatalf("ping: event %+v, err %v", ev, err)
		}
	}

	var answer strings.Builder
	var session, route string
	var last rpc.EventType
	for ev, err := range rpc.Do(context.Background(), sock, rpc.Request{Op: rpc.OpAsk, Text: "what is the answer?", Source: rpc.SourceCLI}) {
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
