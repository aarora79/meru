// This file tests merud's startup checks and runs the whole daemon over a
// fake engine: start, ping, ask, shut down.

package main

import (
	"context"
	"encoding/json"
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

	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
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

	// pulled is what Pulled reports Ollama has on disk.
	pulled []string
	// slowModel and loaded, when set, make Generate for slowModel wait for
	// loaded to close (or for ctx to end), as Ollama does while it loads a
	// large model. Tests use them to ask a question during the load.
	slowModel string
	loaded    chan struct{}

	mu          sync.Mutex // guards the fields below, and version and infoErr once merud runs
	infoCalls   int        // Info calls, one per check of Ollama
	models      []string
	embeds      int
	system      string // the system prompt of the last Stream call
	route       string // the last message of the last router call
	streamModel string // the model of the last Stream call
	// events lists "generate <model>", "generated <model>" and "stream
	// <model>" in the order the calls began and ended.
	events []string
	// slowCall is the last Generate call for slowModel.
	slowCall struct {
		msgs []engine.Message
		opts engine.Options
	}
}

func (f *fakeEngine) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	f.mu.Lock()
	f.models = append(f.models, opts.Model)
	f.events = append(f.events, "generate "+opts.Model)
	// The router asks for one token with log probabilities; nothing else does.
	if opts.MaxTokens == 1 && opts.LogProbs && len(msgs) > 0 {
		f.route = msgs[len(msgs)-1].Content
	}
	slow := f.loaded != nil && opts.Model == f.slowModel
	if slow {
		f.slowCall.msgs, f.slowCall.opts = msgs, opts
	}
	f.mu.Unlock()
	if slow {
		select {
		case <-f.loaded:
		case <-ctx.Done():
			return engine.Completion{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "generated "+opts.Model)
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
	f.streamModel = opts.Model
	f.events = append(f.events, "stream "+opts.Model)
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infoCalls++
	return engine.ModelInfo{Runtime: "ollama", RuntimeVersion: f.version}, f.infoErr
}

// setOllama changes what Info reports, as when Ollama starts or is
// upgraded while merud waits.
func (f *fakeEngine) setOllama(version string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version, f.infoErr = version, err
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

// TestCheckOllama checks the sentence each Ollama gives: none for one
// new enough, and one each for a version too old, one Meru can't read,
// and no answer.
func TestCheckOllama(t *testing.T) {
	const url = "http://127.0.0.1:11434"
	tests := []struct {
		name    string
		eng     *fakeEngine
		version string
		problem string
	}{
		{"new enough", &fakeEngine{version: "0.13.0"}, "0.13.0", ""},
		{"too old", &fakeEngine{version: "0.11.0"}, "0.11.0", "Ollama 0.11.0 is too old; Meru needs 0.12.11 or later."},
		{"down", &fakeEngine{infoErr: errors.New("connection refused")}, "", "Ollama isn't running at " + url + "."},
		{"odd version", &fakeEngine{version: "dev"}, "dev", `Ollama answered with a version Meru can't read, "dev"; Meru needs 0.12.11 or later.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, problem := checkOllama(context.Background(), tt.eng, url)
			if version != tt.version || problem != tt.problem {
				t.Errorf("checkOllama = %q, %q; want %q, %q", version, problem, tt.version, tt.problem)
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
		{"lite loads the shared model", config.Models{Fast: "small", Main: "small", Embed: "emb"}, "", []string{"small"}, ""},
		// The answer model loads in the background later; see
		// TestStartupWarmsAnswerModel.
		{"full leaves the answer model", config.Models{Fast: "small", Main: "big", Embed: "emb"}, "", []string{"small"}, ""},
		{"missing model", config.Models{Fast: "small", Main: "big", Embed: "emb"}, "small", []string{"small"}, "ollama pull small"},
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

// TestStartupWarmsAnswerModel runs merud with a model set whose answer
// model loads slowly. merud answers a ping while the load runs, a
// question asked during the load waits for it rather than load the model
// again, and the load uses the set's think = false and a real system
// prompt.
func TestStartupWarmsAnswerModel(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "w.sock")
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := "[models]\nfast = \"small\"\nmain = \"big\"\n\n" +
		"[[models.sets]]\nname = \"big-set\"\nmain = \"big\"\nthink = false\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := make(chan struct{})
	eng := &fakeEngine{version: "0.13.0", slowModel: "big", loaded: loaded}
	build := func(config.Config, *slog.Logger) (engine.Engine, error) { return eng, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-config", cfgPath, "-socket", sock}, io.Discard, build) }()

	// merud answers a ping while the answer model still loads.
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
		t.Fatal("merud never answered a ping during the load")
	}

	// A question during the load waits for it.
	answered := make(chan string, 1)
	go func() {
		var answer strings.Builder
		for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpAsk, Text: "ping?"}, nil) {
			if err == nil && ev.Type == rpc.EventToken {
				answer.WriteString(ev.Text)
			}
		}
		answered <- answer.String()
	}()
	select {
	case got := <-answered:
		t.Fatalf("the question was answered (%q) before the answer model loaded", got)
	case <-time.After(300 * time.Millisecond):
	}
	close(loaded)
	select {
	case got := <-answered:
		if got != "pong" {
			t.Errorf("answer = %q, want pong", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question never got an answer after the load")
	}

	eng.mu.Lock()
	events := slices.Clone(eng.events)
	warmMsgs, warmOpts := eng.slowCall.msgs, eng.slowCall.opts
	eng.mu.Unlock()
	if n := slices.Index(events, "generated big"); n < 0 || slices.Index(events, "stream big") < n {
		t.Errorf("events = %v, want the load to end before the answer streams", events)
	}
	var loads int
	for _, ev := range events {
		if ev == "generate big" {
			loads++
		}
	}
	if loads != 1 {
		t.Errorf("the answer model loaded %d times, want 1: %v", loads, events)
	}
	if !warmOpts.NoThink || warmOpts.MaxTokens != 1 {
		t.Errorf("warm-up options = %+v, want one token with thinking off", warmOpts)
	}
	if len(warmMsgs) != 2 || warmMsgs[0].Role != engine.RoleSystem || !strings.HasPrefix(warmMsgs[0].Content, agent.DefaultSystemPrompt) {
		t.Errorf("warm-up messages = %+v, want the system prompt and one question", warmMsgs)
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
	raw, err := os.ReadFile(filepath.Join(dir, "merud.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `msg="answer model warm" model=big`) {
		t.Errorf("log lacks the answer model warm line:\n%s", raw)
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

	// The session ops read the question back from its transcript.
	var sessions []rpc.SessionInfo
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpSessions}, nil) {
		if err != nil {
			t.Fatalf("sessions: %v", err)
		}
		if ev.Type == rpc.EventSessions {
			sessions = ev.Sessions
		}
	}
	if len(sessions) != 1 || sessions[0].Title != "ping?" || sessions[0].Turns != 1 {
		t.Fatalf("sessions = %+v, want the one session with ping?", sessions)
	}
	var turns []rpc.TurnInfo
	for ev, err := range rpc.Do(ctx, sock, rpc.Request{Op: rpc.OpSessionTurns, Session: sessions[0].ID}, nil) {
		if err != nil {
			t.Fatalf("session turns: %v", err)
		}
		if ev.Type == rpc.EventTurns {
			turns = ev.Turns
		}
	}
	if len(turns) != 1 || turns[0].Question != "ping?" || turns[0].Answer != "pong" {
		t.Errorf("turns = %+v, want ping? answered pong", turns)
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

	tests := []struct {
		name  string
		args  []string
		build engineBuilder
		want  string
	}{
		{"bad config", []string{"-config", badCfg, "-socket", sock}, okEngine, "profile"},
		{"engine fails", []string{"-config", good, "-socket", sock}, failEngine, "engine: boom"},
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

// TestRouterNamesConnectedTools builds the router the way run does, over a
// fake Ollama, and checks that the prompt it sends names what config
// connects, on option C's line.
func TestRouterNamesConnectedTools(t *testing.T) {
	tests := []struct {
		name    string
		servers []config.MCPServer
		cmds    []config.Command
		searxng string // [web] searxng_url; "" turns web search off
		want    string // "" means the prompt has no connected list
	}{
		{"nothing connected", nil, nil, "", ""},
		{"the defaults: web search", nil, nil, "http://127.0.0.1:8888", "Connected: web search;"},
		{
			"a server and a command",
			[]config.MCPServer{{Name: "obsidian", Allow: []string{"obsidian_list_files_in_vault", "obsidian_simple_search"}}},
			[]config.Command{{Name: "git-log"}},
			"http://127.0.0.1:8888",
			"Connected: obsidian (vault), git-log, web search; questions about these, by name, are C",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.MCP.Servers, cfg.Commands, cfg.Web.SearXNGURL = tt.servers, tt.cmds, tt.searxng
			// With web search off, web_fetch would still name "web pages".
			if tt.searxng == "" {
				cfg.Builtin.Tools = []string{"datetime"}
			}
			srv := fakeollama.Start(t, fakeollama.Config{})
			eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			connected := agent.ConnectedTools(cfg)
			rt, err := newRouter(cfg, eng, func() []string { return connected }, func() []string { return cfg.Index.Folders }, nil)
			if err != nil {
				t.Fatal(err)
			}
			// The fake answers with no log probabilities, so the route is the
			// fallback; this test reads only the prompt.
			if _, err := rt.Decide(context.Background(), "list my obsidian vaults", nil); err != nil {
				t.Fatal(err)
			}
			reqs := srv.Requests("/api/chat")
			if len(reqs) != 1 {
				t.Fatalf("got %d chat requests, want 1", len(reqs))
			}
			// The struct names only the fields the test reads; json skips the rest.
			var body struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			prompt := body.Messages[len(body.Messages)-1].Content
			if tt.want == "" && strings.Contains(prompt, "Connected:") {
				t.Errorf("prompt names connected tools when none are:\n%s", prompt)
			}
			if !strings.Contains(prompt, tt.want) {
				t.Errorf("prompt lacks %q:\n%s", tt.want, prompt)
			}
		})
	}
}
