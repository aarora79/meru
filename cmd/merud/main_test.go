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

	mu     sync.Mutex // guards models and embeds
	models []string
	embeds int
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
	return func(yield func(engine.Delta, error) bool) {
		if !yield(engine.Delta{Text: "pong"}, nil) {
			return
		}
		yield(engine.Delta{Done: true}, nil)
	}, nil
}

func (f *fakeEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embeds++
	return []engine.Vector{{0.1}}, nil
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

func TestRunServesAndStops(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "d.sock")
	cfgPath := filepath.Join(dir, "config.toml") // absent: defaults
	eng := &fakeEngine{version: "0.13.0"}
	build := func(config.Config) (engine.Engine, error) { return eng, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-config", cfgPath, "-socket", sock}, io.Discard, build) }()

	// Wait for merud to answer a ping.
	up := false
	for range 100 {
		for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpPing}) {
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
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpAsk, Text: "ping?"}) {
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

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v, want nil after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run didn't return after cancel")
	}

	if _, err := os.Stat(filepath.Join(dir, "merud.log")); err != nil {
		t.Errorf("no log file: %v", err)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Error("socket file left behind after shutdown")
	}
	sessions, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*", "*.jsonl"))
	if len(sessions) != 1 {
		t.Errorf("found %d session files, want 1", len(sessions))
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
	okEngine := func(config.Config) (engine.Engine, error) { return &fakeEngine{version: "0.13.0"}, nil }
	oldEngine := func(config.Config) (engine.Engine, error) { return &fakeEngine{version: "0.5.0"}, nil }

	tests := []struct {
		name  string
		args  []string
		build func(config.Config) (engine.Engine, error)
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
func failEngine(config.Config) (engine.Engine, error) {
	return nil, errors.New("boom")
}
