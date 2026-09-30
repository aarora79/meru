// This file tests merud's watch on Ollama: merud keeps its socket while
// Ollama is down or too old, reports why in the connectors op, turns a
// question away with a clear error, and takes questions once Ollama
// answers; after that the watch keeps the status true.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// ollamaRow asks merud at sock for its connectors and returns Ollama's
// row, and false when merud doesn't answer or has no such row.
func ollamaRow(ctx context.Context, sock string) (rpc.ConnectorStatus, bool) {
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpConnectors}, nil) {
		if err != nil {
			return rpc.ConnectorStatus{}, false
		}
		for _, c := range ev.Connectors {
			if c.ID == "ollama" {
				return c, true
			}
		}
	}
	return rpc.ConnectorStatus{}, false
}

// waitOllama asks for the connectors until Ollama's sentence is want,
// for up to 5 seconds. Each ask also makes merud check Ollama at once.
func waitOllama(t *testing.T, ctx context.Context, sock, want string) rpc.ConnectorStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, ok := ollamaRow(ctx, sock)
		if ok && row.Sentence == want {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("ollama = %+v (answered %v), want %q", row, ok, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// askOnce asks merud one question and returns the answer's text and the
// error event's text, if one came.
func askOnce(ctx context.Context, sock string) (answer, errText string) {
	var b strings.Builder
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpAsk, Text: "ping?"}, nil) {
		switch {
		case err != nil:
			return b.String(), err.Error()
		case ev.Type == rpc.EventToken:
			b.WriteString(ev.Text)
		case ev.Type == rpc.EventError:
			errText = ev.Error
		}
	}
	return b.String(), errText
}

// TestServeWaitsForOllama starts merud while Ollama is down: merud keeps
// its socket, reports Ollama failed, and turns a question away. Then
// Ollama comes back too old, and then new enough, and merud answers.
func TestServeWaitsForOllama(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "o.sock")
	cfgPath := filepath.Join(dir, "config.toml")
	// No SearXNG, so the test checks nothing on this machine's port 8888.
	if err := os.WriteFile(cfgPath, []byte("[web]\nsearxng_url = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{infoErr: errors.New("connection refused")}
	build := func(config.Config, *slog.Logger) (engine.Engine, error) { return eng, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-config", cfgPath, "-socket", sock}, io.Discard, build) }()

	row := waitOllama(t, ctx, sock, "Ollama isn't running at http://127.0.0.1:11434.")
	if row.State != rpc.ConnectorFailed || !row.Required || row.Kind != "dependency" {
		t.Errorf("ollama = %+v, want a failed, required dependency", row)
	}
	if _, errText := askOnce(ctx, sock); errText != "Ollama isn't running, so Meru can't answer yet." {
		t.Errorf("a question while Ollama is down got %q", errText)
	}
	var servers []rpc.MCPStatus
	gotStatus := false
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpMCPStatus}, nil) {
		if err == nil && ev.Type == rpc.EventMCPStatus {
			servers, gotStatus = ev.MCP, true
		}
	}
	if !gotStatus || len(servers) != 0 {
		t.Errorf("mcp_status while waiting = %v (answered %v), want no servers", servers, gotStatus)
	}

	eng.setOllama("0.11.0", nil)
	waitOllama(t, ctx, sock, "Ollama 0.11.0 is too old; Meru needs 0.12.11 or later.")
	if _, errText := askOnce(ctx, sock); errText != "Meru can't answer yet: Ollama 0.11.0 is too old; Meru needs 0.12.11 or later." {
		t.Errorf("a question with an old Ollama got %q", errText)
	}

	eng.setOllama("0.13.0", nil)
	waitOllama(t, ctx, sock, "Ollama 0.13.0 is running at http://127.0.0.1:11434.")
	answer, errText := askOnce(ctx, sock)
	if answer != "pong" || errText != "" {
		t.Errorf("answer once Ollama runs = %q, error %q; want pong", answer, errText)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v, want nil after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run didn't return after cancel")
	}
}

// TestServeStopsWhileWaiting stops merud before Ollama ever answers:
// run returns nil and removes the socket.
func TestServeStopsWhileWaiting(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "s.sock")
	eng := &fakeEngine{infoErr: errors.New("connection refused")}
	build := func(config.Config, *slog.Logger) (engine.Engine, error) { return eng, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-config", filepath.Join(dir, "config.toml"), "-socket", sock}, io.Discard, build)
	}()
	waitOllama(t, ctx, sock, "Ollama isn't running at http://127.0.0.1:11434.")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run didn't return after cancel")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Error("socket file left behind")
	}
}

// TestOllamaWatchAfterReady checks the watch that runs once merud is
// ready: Ollama stopping shows as failed, and coming back as ok.
func TestOllamaWatchAfterReady(t *testing.T) {
	eng := &fakeEngine{version: "0.13.0"}
	cfg := config.Config{Ollama: config.Ollama{BaseURL: "http://127.0.0.1:11434"}, Models: config.Models{Fast: "small", Embed: "embed"}}
	o, err := newOllamaWatch(cfg, eng, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	o.every = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	if !o.waitReady(ctx) {
		t.Fatal("waitReady = false with Ollama up")
	}
	stopped := make(chan struct{})
	go func() { o.watch(ctx); close(stopped) }()
	// t.Cleanup runs when the test ends: it stops the watch and waits.
	t.Cleanup(func() { cancel(); <-stopped })

	wait := func(state, sentence string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st := o.status()
			if st.State == state && st.Sentence == sentence {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("ollama = %s %q, want %s %q", st.State, st.Sentence, state, sentence)
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(connectors.StateOK, "Ollama 0.13.0 is running at http://127.0.0.1:11434.")
	eng.setOllama("", errors.New("connection refused"))
	wait(connectors.StateFailed, "Ollama isn't running at http://127.0.0.1:11434.")
	eng.setOllama("0.13.1", nil)
	wait(connectors.StateOK, "Ollama 0.13.1 is running at http://127.0.0.1:11434.")
}
