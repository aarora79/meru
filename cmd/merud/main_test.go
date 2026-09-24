// This file tests merud's startup checks and runs the whole daemon over a
// fake engine: start, ping, ask, shut down.

package main

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// fakeEngine answers every call with canned results and records which models
// it was asked for.
type fakeEngine struct {
	version  string
	infoErr  error
	failWarm string // Generate fails for this model
	// dims is the size of each vector Embed returns; 0 means 1.
	dims int
	// hold, when not nil, makes Embed wait for it to close (or for ctx to
	// end) before embedding any text that contains holdMarker. Tests use
	// it to keep the startup scan busy.
	hold chan struct{}

	mu     sync.Mutex // guards models, embeds and system
	models []string
	embeds int
	system string // the system prompt of the last Stream call
}

func (f *fakeEngine) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = append(f.models, opts.Model)
	if opts.Model == f.failWarm {
		return engine.Completion{}, errors.New("model not found")
	}
	return engine.Completion{Text: "h"}, nil
}

func (f *fakeEngine) Stream(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (iter.Seq2[engine.Delta, error], error) {
	f.mu.Lock()
	if len(msgs) > 0 {
		f.system = msgs[0].Content
	}
	f.mu.Unlock()
	return func(yield func(engine.Delta, error) bool) {
		if !yield(engine.Delta{Text: "pong"}, nil) {
			return
		}
		yield(engine.Delta{Done: true}, nil)
	}, nil
}

// holdMarker is the text that makes Embed wait for fakeEngine.hold.
const holdMarker = "HOLD-THE-SCAN"

// Embed returns one vector per text, each the length of its text in every
// place, so the vectors differ between texts.
func (f *fakeEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	f.mu.Lock()
	f.embeds++
	f.mu.Unlock()
	dims := max(f.dims, 1)
	out := make([]engine.Vector, len(texts))
	for i, t := range texts {
		if f.hold != nil && strings.Contains(t, holdMarker) {
			select {
			case <-f.hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		v := make(engine.Vector, dims)
		for j := range v {
			v[j] = float32(len(t) + j)
		}
		out[i] = v
	}
	return out, nil
}

func (f *fakeEngine) Info(ctx context.Context) (engine.ModelInfo, error) {
	return engine.ModelInfo{Runtime: "ollama", RuntimeVersion: f.version}, f.infoErr
}

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		have string
		want bool
		bad  bool
	}{
		{"0.12.11", true, false},
		{"0.12.12", true, false},
		{"0.13", true, false},
		{"1.0.0", true, false},
		{"v0.12.11", true, false},
		{"0.12.11-rc1", true, false},
		{"0.12.10", false, false},
		{"0.9.99", false, false},
		{"0.12", false, false},
		{"", false, true},
		{"banana", false, true},
		{"0.12.11.1", false, true},
		{"0.-1.0", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.have, func(t *testing.T) {
			got, err := versionAtLeast(tt.have, minOllama)
			if (err != nil) != tt.bad {
				t.Fatalf("versionAtLeast(%q) error = %v, want error %v", tt.have, err, tt.bad)
			}
			if got != tt.want {
				t.Errorf("versionAtLeast(%q) = %v, want %v", tt.have, got, tt.want)
			}
		})
	}
}

func TestCheckRuntime(t *testing.T) {
	tests := []struct {
		name string
		eng  *fakeEngine
		want string // piece of the error; empty means success
	}{
		{"new enough", &fakeEngine{version: "0.13.0"}, ""},
		{"too old", &fakeEngine{version: "0.11.0"}, "found Ollama 0.11.0, which is too old; Meru needs 0.12.11"},
		{"down", &fakeEngine{infoErr: errors.New("connection refused")}, "is it running?"},
		{"odd version", &fakeEngine{version: "dev"}, "read Ollama version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := checkRuntime(context.Background(), tt.eng)
			if tt.want == "" {
				if err != nil {
					t.Errorf("checkRuntime = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("checkRuntime = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestWarm(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name       string
		models     config.Models
		failWarm   string
		wantModels []string
		wantErr    string
	}{
		{"lite loads the shared model once", config.Models{Fast: "small", Main: "small", Embed: "emb"}, "", []string{"small"}, ""},
		{"full loads both", config.Models{Fast: "small", Main: "big", Embed: "emb"}, "", []string{"small", "big"}, ""},
		{"missing model", config.Models{Fast: "small", Main: "big", Embed: "emb"}, "big", []string{"small", "big"}, "ollama pull big"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{failWarm: tt.failWarm}
			err := warm(context.Background(), eng, tt.models, quiet)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("warm = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("warm = %v, want an error containing %q", err, tt.wantErr)
			}
			if !slices.Equal(eng.models, tt.wantModels) {
				t.Errorf("warmed %v, want %v", eng.models, tt.wantModels)
			}
			if tt.wantErr == "" && eng.embeds != 1 {
				t.Errorf("embed warmed %d times, want 1", eng.embeds)
			}
		})
	}
}

// shortDir makes a temporary directory with a short path, because macOS caps
// Unix socket paths at 104 bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "merud")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// serveOneQuestion runs merud with extra flags over a fake engine: it waits
// for a ping, asks one question, checks the answer, and shuts merud down. It
// returns merud's home directory.
func serveOneQuestion(t *testing.T, extra ...string) string {
	t.Helper()
	dir := shortDir(t)
	sock := filepath.Join(dir, "d.sock")
	cfgPath := filepath.Join(dir, "config.toml") // absent: defaults
	eng := &fakeEngine{version: "0.13.0"}
	build := func(config.Config, *slog.Logger) (engine.Engine, error) { return eng, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	args := append([]string{"-config", cfgPath, "-socket", sock}, extra...)
	go func() { done <- run(ctx, args, io.Discard, build) }()

	// Wait for merud to answer a ping.
	up := false
	for range 100 {
		for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpPing}, nil) {
			up = err == nil && ev.Type == rpc.EventDone
		}
		if up {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		t.Fatal("merud never answered a ping")
	}

	var answer strings.Builder
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpAsk, Text: "ping?"}, nil) {
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		if ev.Type == rpc.EventToken {
			answer.WriteString(ev.Text)
		}
	}
	if answer.String() != "pong" {
		t.Errorf("answer = %q, want pong", answer.String())
	}

	// The answered question shows in every usage window, and the status
	// op reports the database's size.
	var usage []rpc.UsageWindow
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpUsage}, nil) {
		if err != nil {
			t.Fatalf("usage: %v", err)
		}
		if ev.Type == rpc.EventUsage {
			usage = ev.Usage
		}
	}
	if len(usage) != 6 {
		t.Fatalf("usage has %d windows, want 6", len(usage))
	}
	for _, w := range usage {
		if w.Turns != 1 || w.Sessions != 1 {
			t.Errorf("usage window %s = %+v, want 1 turn in 1 session", w.Name, w)
		}
	}
	var size int64
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpIndexStatus}, nil) {
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if ev.Type == rpc.EventStatus && ev.Status != nil {
			size = ev.Status.DBBytes
		}
	}
	if size <= 0 {
		t.Errorf("status DBBytes = %d, want the size of meru.db", size)
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

	if _, err := os.Stat(sock); err == nil {
		t.Error("socket file left behind after shutdown")
	}
	return dir
}

func TestRunServesAndStops(t *testing.T) {
	dir := serveOneQuestion(t)
	if _, err := os.Stat(filepath.Join(dir, "merud.log")); err != nil {
		t.Errorf("no log file: %v", err)
	}
	sessions, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*", "*.jsonl"))
	if len(sessions) != 1 {
		t.Errorf("found %d session files, want 1", len(sessions))
	}
}

// TestRunLogLevel checks what merud.log holds at the default info level and
// with -v: info has the startup, turn and shutdown lines only; -v adds a
// debug line for each stage, each tagged with the turn's trace ID, and still
// no question text.
func TestRunLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantDebug bool
	}{
		{"info by default", nil, false},
		{"-v forces debug", []string{"-v"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := serveOneQuestion(t, tt.args...)
			raw, err := os.ReadFile(filepath.Join(dir, "merud.log"))
			if err != nil {
				t.Fatal(err)
			}
			log := string(raw)
			if got := strings.Contains(log, "level=DEBUG"); got != tt.wantDebug {
				t.Errorf("log has debug lines = %v, want %v:\n%s", got, tt.wantDebug, log)
			}
			if strings.Contains(log, "ping?") {
				t.Errorf("log holds the question text:\n%s", log)
			}
			for _, want := range []string{"msg=\"merud starting\"", "msg=turn ", "msg=\"merud stopped\""} {
				if !strings.Contains(log, want) {
					t.Errorf("log lacks %s:\n%s", want, log)
				}
			}
			for line := range strings.Lines(log) {
				if strings.Contains(line, "msg=turn ") && !strings.Contains(line, "trace_id=") {
					t.Errorf("turn line has no trace_id: %s", line)
				}
				if strings.Contains(line, "err=<nil>") {
					t.Errorf("line logs a nil error: %s", line)
				}
			}
			if tt.wantDebug {
				for _, want := range []string{"msg=\"rpc request\"", "msg=\"prompt built\"", "msg=route "} {
					if !strings.Contains(log, want) {
						t.Errorf("debug log lacks %s:\n%s", want, log)
					}
				}
			}
		})
	}
}

func TestRunStartupErrors(t *testing.T) {
	dir := shortDir(t)
	badCfg := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(badCfg, []byte("profile = \"huge\""), 0o600); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "config.toml")
	sock := filepath.Join(dir, "e.sock")
	okEngine := func(config.Config, *slog.Logger) (engine.Engine, error) { return &fakeEngine{version: "0.13.0"}, nil }
	oldEngine := func(config.Config, *slog.Logger) (engine.Engine, error) { return &fakeEngine{version: "0.5.0"}, nil }

	tests := []struct {
		name  string
		args  []string
		build engineBuilder
		want  string
	}{
		{"bad config", []string{"-config", badCfg, "-socket", sock}, okEngine, "profile"},
		{"engine fails", []string{"-config", good, "-socket", sock}, failEngine, "engine: boom"},
		{"old ollama", []string{"-config", good, "-socket", sock}, oldEngine, "too old"},
		{"stray argument", []string{"-config", good, "extra"}, okEngine, "unexpected arguments"},
		{"unknown flag", []string{"-nope"}, okEngine, "not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), tt.args, io.Discard, tt.build)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("run = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// failEngine stands in for an engine that can't be built, so run must report
// the failure instead of starting.
func failEngine(config.Config, *slog.Logger) (engine.Engine, error) {
	return nil, errors.New("boom")
}
