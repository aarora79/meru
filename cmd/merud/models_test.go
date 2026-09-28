// This file tests the model ops. Most tests run the model service on the
// real OllamaEngine against the fake Ollama, which answers /api/tags,
// /api/show and /api/ps, and unloads a model when a request sets
// keep_alive to 0: which known models are installed, what the model sets
// show, the order of a switch (unload, wait, load), what a failed switch
// leaves alone, and what a save writes to config.toml. The last runs a
// whole merud and checks that the next question uses the new model, with
// no restart.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// The model names the tests use: the three known ones, and two more for
// the sets.
const (
	miniCPM  = "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M"
	qwen     = "qwen3.6:35b"
	gemma    = "gemma3:12b"
	qwenMoE  = "qwen3.6:35b-a3b-mxfp8"
	gemmaMoE = "gemma4:26b-mxfp8"
)

// Pulled makes fakeEngine a modelLister, as OllamaEngine is.
func (f *fakeEngine) Pulled(ctx context.Context) ([]engine.PulledModel, error) {
	var out []engine.PulledModel
	for _, name := range f.pulled {
		out = append(out, engine.PulledModel{Name: name})
	}
	return out, nil
}

// Unload makes fakeEngine a modelUnloader. Its Info lists no loaded
// models, so a switch finds the old model gone at once.
func (f *fakeEngine) Unload(ctx context.Context, model string) error { return nil }

// fixedAnswer stands in for the agent: it holds the answer model and
// whether its thinking is off.
type fixedAnswer struct {
	main    string
	noThink bool
}

// Main returns the answer model.
func (a *fixedAnswer) Main() string { return a.main }

// SetMain changes the answer model.
func (a *fixedAnswer) SetMain(model string, noThink bool) { a.main, a.noThink = model, noThink }

// WaitWarm returns at once: no startup load runs in these tests.
func (a *fixedAnswer) WaitWarm(context.Context) error { return nil }

// modelsConfig is a config.toml with a comment and an unrelated key, which
// every change must keep.
const modelsConfig = "# my models, keep me\n[models]\nmain = \"" + qwen + "\" # the big one\n\n[agent]\nhistory_turns = 3\n"

// testSets are the model sets the switch tests use. new(false) makes a
// *bool that points at false, which is how config holds think = false.
var testSets = []config.ModelSet{
	{Name: "qwen-moe", Main: qwenMoE, Think: new(false)},
	{Name: "gemma-moe", Main: gemmaMoE},
	{Name: "small-router", Main: qwenMoE, Fast: gemma},
	{Name: "new-embed", Main: qwenMoE, Embed: "qwen3-embedding:0.6b"},
	{Name: "missing", Main: "qwen3.8:27b-mlx"},
}

// testModelService returns a model service on the real OllamaEngine
// against a fake Ollama that has pulled the models in pulled, with the
// capabilities in caps, holds loaded in memory, and a config.toml holding
// modelsConfig. The fast model is MiniCPM5-2B, the answer model qwen, and
// the sets testSets.
func testModelService(t *testing.T, pulled, loaded []string, caps map[string][]string) (*modelService, *fixedAnswer, *fakeollama.Server) {
	t.Helper()
	srv := fakeollama.Start(t, fakeollama.Config{Version: "0.34.0", Models: pulled, Loaded: loaded, Capabilities: caps})
	eng, err := engine.NewOllama(srv.URL, "-1", "nomic-embed-text", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(modelsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	answer := &fixedAnswer{main: qwen}
	cfg := config.Config{Profile: "lite", Models: config.Models{Fast: miniCPM, Main: qwen, Embed: "nomic-embed-text", Sets: testSets}}
	m := newModelService(cfg, path, "", eng, answer, func(f func() error) error { return f() },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.wait, m.poll = 200*time.Millisecond, 5*time.Millisecond
	return m, answer, srv
}

// ollamaCaps is what Ollama 0.34 lists for the three models.
var ollamaCaps = map[string][]string{
	miniCPM: {"tools", "thinking", "completion"},
	qwen:    {"completion", "vision", "tools", "thinking"},
	gemma:   {"completion", "vision"},
}

// everyModel is a fake Ollama's disk with every model the tests name but
// the "missing" set's.
var everyModel = []string{miniCPM, qwen, gemma, qwenMoE, gemmaMoE, "nomic-embed-text:latest"}

func TestModelChoices(t *testing.T) {
	m, _, _ := testModelService(t, []string{miniCPM, qwen, "nomic-embed-text:latest"}, nil, ollamaCaps)
	info := m.info(t.Context(), "")
	if info.Main != qwen || info.RuntimeVersion != "0.34.0" || info.Err != "" {
		t.Errorf("info = %+v", info)
	}
	tests := []struct {
		name      string
		installed bool
		tiers     []string
		caps      []string
	}{
		{miniCPM, true, []string{"fast"}, ollamaCaps[miniCPM]},
		// Not pulled: the capabilities come from the known list.
		{gemma, false, []string{}, []string{"completion", "vision"}},
		{"gemma4:26b-a4b-it-qat", false, []string{}, ollamaCaps[qwen]},
		{qwen, true, []string{"main"}, ollamaCaps[qwen]},
		{gemmaMoE, false, []string{}, ollamaCaps[qwen]},
		{qwenMoE, false, []string{}, ollamaCaps[qwen]},
	}
	if len(info.Choices) != len(tests) {
		t.Fatalf("choices = %+v, want %d", info.Choices, len(tests))
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := info.Choices[i]
			if c.Name != tt.name || c.Installed != tt.installed || !slices.Equal(c.Tiers, tt.tiers) ||
				!slices.Equal(c.Capabilities, tt.caps) {
				t.Errorf("choice = %+v; want installed %v, tiers %v, capabilities %v", c, tt.installed, tt.tiers, tt.caps)
			}
			if c.Pull != "ollama pull "+tt.name || c.Run != "ollama run "+tt.name {
				t.Errorf("commands = %q, %q", c.Pull, c.Run)
			}
			if c.Label == "" || c.Size == "" || c.Good == "" || c.Bad == "" {
				t.Errorf("a field is empty: %+v", c)
			}
		})
	}
}

// TestModelSetRows checks the sets OpModels reports: in config's order,
// with size, whether Ollama has the main model and holds it now, the
// think setting and which one is in use.
func TestModelSetRows(t *testing.T) {
	m, _, _ := testModelService(t, everyModel, []string{miniCPM, qwenMoE}, ollamaCaps)
	info := m.info(t.Context(), "qwen-moe")
	if info.Active != "qwen-moe" || len(info.Sets) != len(testSets) {
		t.Fatalf("active %q, sets %+v", info.Active, info.Sets)
	}
	moe, gem, missing := info.Sets[0], info.Sets[1], info.Sets[4]
	// The fake gives every model a size of 1 GiB.
	if moe.Name != "qwen-moe" || !moe.Active || !moe.Loaded || !moe.Pulled || !moe.ThinkOff || moe.Bytes != 1<<30 {
		t.Errorf("qwen-moe row = %+v", moe)
	}
	if gem.Active || gem.Loaded || !gem.Pulled || gem.ThinkOff || len(gem.Capabilities) == 0 {
		t.Errorf("gemma-moe row = %+v", gem)
	}
	if missing.Pulled || missing.Bytes != 0 || missing.Loaded {
		t.Errorf("missing row = %+v, want not pulled", missing)
	}
}

// TestNewModelServiceFindsTheSet checks that merud starts with the first
// set whose models match config's, and passes on its think setting, and
// that with no set, [models] think decides, off by default.
func TestNewModelServiceFindsTheSet(t *testing.T) {
	answer := &fixedAnswer{main: qwenMoE}
	cfg := config.Config{Models: config.Models{Fast: miniCPM, Main: qwenMoE, Embed: "nomic-embed-text", Sets: testSets}}
	m := newModelService(cfg, "", "", &fakeEngine{}, answer, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if m.activeSet() != "qwen-moe" || !answer.noThink {
		t.Errorf("active %q, think off %v; want qwen-moe with thinking off", m.activeSet(), answer.noThink)
	}
	cfg.Models.Main = "some-other-model"
	other := &fixedAnswer{}
	m = newModelService(cfg, "", "", &fakeEngine{}, other, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if m.activeSet() != "" || !other.noThink {
		t.Errorf("active %q, think off %v; want no set, and thinking off by default", m.activeSet(), other.noThink)
	}
	// think = true under [models] turns thinking on when no set is in use.
	cfg.Models.Think = true
	thinking := &fixedAnswer{}
	newModelService(cfg, "", "", &fakeEngine{}, thinking, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if thinking.noThink {
		t.Error("think = true: thinking is off, want on")
	}
}

// use runs OpModelUse for name and returns the reply and the error.
func use(t *testing.T, m *modelService, name string, rebuild bool) (*rpc.ModelsInfo, error) {
	t.Helper()
	var got *rpc.ModelsInfo
	err := m.handleModelUse(t.Context(), rpc.Request{Op: rpc.OpModelUse, ID: name, Rebuild: rebuild}, func(ev rpc.Event) error {
		got = ev.Models
		return nil
	})
	return got, err
}

// TestModelUseEvictsThenLoads checks the order of a switch: unload the old
// answer model with keep_alive 0, poll /api/ps until it is gone, and load
// the new one, before the agent gets it. config.toml stays as it was:
// a switch lasts until merud stops.
func TestModelUseEvictsThenLoads(t *testing.T) {
	m, answer, srv := testModelService(t, everyModel, []string{miniCPM, qwen}, ollamaCaps)
	before := len(srv.Requests(""))

	got, err := use(t, m, "gemma-moe", false)
	if err != nil {
		t.Fatalf("use: %v", err)
	}
	if answer.main != gemmaMoE || answer.noThink || m.activeSet() != "gemma-moe" {
		t.Errorf("after the switch: main %q, think off %v, set %q", answer.main, answer.noThink, m.activeSet())
	}
	if got == nil || got.Active != "gemma-moe" || got.Main != gemmaMoE || !got.Sets[1].Active {
		t.Errorf("reply = %+v", got)
	}

	// The requests after the switch began, in order: find the unload, the
	// last /api/ps before the load, and the load.
	reqs := srv.Requests("")[before:]
	unload, load, lastPS := -1, -1, -1
	for i, r := range reqs {
		switch {
		case r.Path == "/api/generate" && r.Model == qwen:
			var body struct {
				KeepAlive json.RawMessage `json:"keep_alive"`
			}
			if err := json.Unmarshal(r.Body, &body); err != nil || string(body.KeepAlive) != "0" {
				t.Errorf("unload body = %s, want keep_alive 0", r.Body)
			}
			unload = i
		case r.Path == "/api/chat" && r.Model == gemmaMoE && load < 0:
			load = i
		case r.Path == "/api/ps" && load < 0:
			lastPS = i
		}
	}
	if unload < 0 || lastPS < unload || load < lastPS {
		t.Errorf("order: unload at %d, last ps at %d, load at %d; want unload < ps < load in %+v", unload, lastPS, load, reqs)
	}
	// Ollama now holds the router and the new model, not the old one.
	info := m.info(t.Context(), m.activeSet())
	if slices.Contains(info.Loaded, qwen) || !slices.Contains(info.Loaded, gemmaMoE) || !slices.Contains(info.Loaded, miniCPM) {
		t.Errorf("loaded = %v, want the router and gemma, not qwen", info.Loaded)
	}
	raw, _ := os.ReadFile(m.configPath)
	if string(raw) != modelsConfig {
		t.Errorf("a switch changed config.toml:\n%s", raw)
	}

	// Switching back and forth twice leaves only the router and the model
	// in use.
	for _, name := range []string{"qwen-moe", "gemma-moe", "qwen-moe"} {
		if _, err := use(t, m, name, false); err != nil {
			t.Fatalf("use %s: %v", name, err)
		}
	}
	info = m.info(t.Context(), m.activeSet())
	if want := []string{miniCPM, qwenMoE}; !sameSet(info.Loaded, want) {
		t.Errorf("loaded after four switches = %v, want %v", info.Loaded, want)
	}
	if !answer.noThink {
		t.Error("qwen-moe sets think = false, but the agent's thinking is on")
	}
}

// sameSet reports whether a and b hold the same names in any order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// TestModelUseKeepsTheRouter checks that a switch away from a main model
// that is also the fast model, as in the lite profile, doesn't unload it:
// the router still needs it.
func TestModelUseKeepsTheRouter(t *testing.T) {
	m, answer, srv := testModelService(t, everyModel, []string{miniCPM}, ollamaCaps)
	answer.main = miniCPM
	if _, err := use(t, m, "gemma-moe", false); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Requests("/api/generate")); n != 0 {
		t.Errorf("%d unload calls, want none", n)
	}
	if !slices.Contains(m.info(t.Context(), "").Loaded, miniCPM) {
		t.Error("the router's model left memory")
	}
}

// TestModelUseFailures checks each way a switch can fail: it names the
// step, and the answer model and the set in use stay as they were.
func TestModelUseFailures(t *testing.T) {
	tests := []struct {
		name    string
		set     string
		rebuild bool
		setup   func(srv *fakeollama.Server)
		wantErr string
		unloads int // unload calls the switch should have made
	}{
		{"no such set", "nope", false, nil, `no model set is called "nope"; config.toml has qwen-moe, gemma-moe`, 0},
		{"main model not pulled", "missing", false, nil, "ollama pull qwen3.8:27b-mlx", 0},
		{"a new embed model without --rebuild", "new-embed", false, nil, "--rebuild", 0},
		{"Ollama refuses the unload", "gemma-moe", false, func(srv *fakeollama.Server) {
			srv.FailNext("/api/generate", 500, "boom")
		}, "step 1 of 3, unload " + qwen, 1},
		{"Ollama can't say what it holds", "gemma-moe", false, func(srv *fakeollama.Server) {
			srv.FailNext("/api/ps", 500, "boom")
		}, "step 2 of 3", 1},
		{"the new model won't load", "gemma-moe", false, func(srv *fakeollama.Server) {
			srv.FailNext("/api/chat", 500, "out of memory")
		}, "step 3 of 3, load " + gemmaMoE, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, answer, srv := testModelService(t, everyModel, []string{miniCPM, qwen}, ollamaCaps)
			m.setActive("qwen-moe")
			if tt.setup != nil {
				tt.setup(srv)
			}
			_, err := use(t, m, tt.set, tt.rebuild)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one that says %q", err, tt.wantErr)
			}
			if answer.main != qwen || m.activeSet() != "qwen-moe" {
				t.Errorf("after a failed switch: main %q, set %q; want qwen and qwen-moe", answer.main, m.activeSet())
			}
			if n := len(srv.Requests("/api/generate")); n != tt.unloads {
				t.Errorf("%d unload calls, want %d", n, tt.unloads)
			}
		})
	}
}

// stickyEngine is an OllamaEngine whose Unload does nothing, so the old
// model never leaves /api/ps, as when another program keeps using it.
type stickyEngine struct{ *engine.OllamaEngine }

// Unload does nothing.
func (stickyEngine) Unload(ctx context.Context, model string) error { return nil }

// TestModelUseTimesOut checks that a switch whose old model stays in
// memory gives up after the wait, loads nothing, and says so.
func TestModelUseTimesOut(t *testing.T) {
	m, answer, srv := testModelService(t, everyModel, []string{miniCPM, qwen}, ollamaCaps)
	m.eng = stickyEngine{m.eng.(*engine.OllamaEngine)}
	_, err := use(t, m, "gemma-moe", false)
	if err == nil || !strings.Contains(err.Error(), "step 2 of 3: ollama still holds "+qwen) {
		t.Fatalf("err = %v, want step 2's timeout", err)
	}
	if answer.main != qwen {
		t.Errorf("main = %q, want qwen", answer.main)
	}
	for _, r := range srv.Requests("/api/chat") {
		if r.Model == gemmaMoE {
			t.Error("the switch loaded gemma after the old model failed to leave")
		}
	}
}

// TestModelUseWarnings checks the warnings a switch sends: a set that
// changes the fast model, and one that changes the embed model with
// --rebuild, each say the tier waits for a save and a restart.
func TestModelUseWarnings(t *testing.T) {
	tests := []struct {
		set, want string
	}{
		{"small-router", "merud moves to the fast model gemma3:12b only after you save the set and restart merud."},
		{"new-embed", "merud moves to the embed model qwen3-embedding:0.6b only after you save the set and restart merud."},
		{"gemma-moe", ""},
	}
	for _, tt := range tests {
		t.Run(tt.set, func(t *testing.T) {
			m, _, _ := testModelService(t, everyModel, []string{miniCPM, qwen}, ollamaCaps)
			got, err := use(t, m, tt.set, true)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" && got.Warning != "" {
				t.Errorf("warning = %q, want none", got.Warning)
			}
			if !strings.Contains(got.Warning, tt.want) {
				t.Errorf("warning = %q, want it to say %q", got.Warning, tt.want)
			}
			if fast := strings.Contains(got.Warning, "log probabilities"); fast != (tt.set == "small-router") {
				t.Errorf("warning = %q; the router warning belongs to small-router alone", got.Warning)
			}
		})
	}
}

// TestModelSave checks that a switch leaves config.toml alone and a save
// writes the models in use, keeping every other line, with a set's fast
// model too.
func TestModelSave(t *testing.T) {
	m, _, _ := testModelService(t, everyModel, []string{miniCPM, qwen}, ollamaCaps)
	if _, err := use(t, m, "small-router", false); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(m.configPath); string(raw) != modelsConfig {
		t.Fatalf("the switch wrote config.toml:\n%s", raw)
	}
	var got *rpc.ModelsInfo
	if err := m.handleModelSave(t.Context(), func(ev rpc.Event) error { got = ev.Models; return nil }); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Main != qwenMoE || cfg.Models.Fast != gemma || cfg.Agent.HistoryTurns != 3 {
		t.Errorf("saved models = %+v, history %d", cfg.Models, cfg.Agent.HistoryTurns)
	}
	raw, _ := os.ReadFile(m.configPath)
	if !strings.Contains(string(raw), "# my models, keep me") || !strings.Contains(string(raw), "# the big one") {
		t.Errorf("the save lost a comment:\n%s", raw)
	}
	if got == nil || !strings.Contains(got.Warning, "merud moves to the fast model gemma3:12b when it restarts") {
		t.Errorf("reply = %+v, want the restart warning", got)
	}
}

func TestModelSet(t *testing.T) {
	tests := []struct {
		name    string
		pulled  []string
		model   string
		wantErr string // part of the error, or "" for none
		warning string
	}{
		{"back to MiniCPM", []string{miniCPM, qwen}, miniCPM, "", ""},
		{"gemma pulled", []string{miniCPM, qwen, gemma}, gemma, "", noToolsWarning},
		{"gemma not pulled", []string{miniCPM, qwen}, gemma, "ollama pull gemma3:12b", ""},
		{"a model we never tried", []string{miniCPM, qwen, "llama3:8b"}, "llama3:8b", "isn't one of the models", ""},
		{"no name", []string{miniCPM, qwen}, "", "isn't one of the models", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, answer, _ := testModelService(t, tt.pulled, []string{miniCPM, qwen}, ollamaCaps)
			var got *rpc.ModelsInfo
			err := m.handleModelSet(t.Context(), rpc.Request{Op: rpc.OpModelSet, ID: tt.model}, func(ev rpc.Event) error {
				got = ev.Models
				return nil
			})
			raw, rerr := os.ReadFile(m.configPath)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one that says %q", err, tt.wantErr)
				}
				if string(raw) != modelsConfig || answer.main != qwen {
					t.Errorf("a refused switch changed something: main %q, config\n%s", answer.main, raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("handleModelSet: %v", err)
			}
			// Only the main line changed, and its comment stayed.
			want := strings.Replace(modelsConfig, `main = "`+qwen+`"`, `main = "`+tt.model+`"`, 1)
			if string(raw) != want {
				t.Errorf("config =\n%s\nwant\n%s", raw, want)
			}
			if answer.main != tt.model {
				t.Errorf("the agent's model = %q, want %q", answer.main, tt.model)
			}
			if got == nil || got.Main != tt.model || got.Warning != tt.warning {
				t.Fatalf("reply = %+v; want main %q, warning %q", got, tt.model, tt.warning)
			}
			for _, c := range got.Choices {
				if (c.Name == tt.model) != slices.Contains(c.Tiers, "main") {
					t.Errorf("%s has tiers %v after the switch to %s", c.Name, c.Tiers, tt.model)
				}
			}
			// The pick went through the same switch: qwen left memory.
			if slices.Contains(got.Loaded, qwen) {
				t.Errorf("loaded = %v; the old answer model stayed", got.Loaded)
			}
		})
	}
}

// TestModelSetOp switches the answer model on a running merud and checks
// that the next question goes to the new model, that the models op names
// it, and that config.toml kept the rest.
func TestModelSetOp(t *testing.T) {
	dir, _ := meruHome(t)
	eng := &fakeEngine{version: "0.34.0", pulled: []string{miniCPM, qwen, "nomic-embed-text:latest"}}
	d := startDaemon(t, dir, settingsHeader, eng)

	m := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpModelSet, ID: qwen}), rpc.EventModels).Models
	if m == nil || m.Main != qwen || m.Warning != "" {
		t.Fatalf("models after the switch = %+v", m)
	}
	if cfg := loadConfig(t, dir); cfg.Models.Main != qwen {
		t.Errorf("config main = %q, want %q", cfg.Models.Main, qwen)
	}

	mustCall(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "hello there", Scope: rpc.ScopeTalk})
	eng.mu.Lock()
	used := eng.streamModel
	eng.mu.Unlock()
	if used != qwen {
		t.Errorf("the next question went to %q, want %q", used, qwen)
	}
	if m := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpModels}), rpc.EventModels).Models; m.Main != qwen {
		t.Errorf("the models op names %q, want %q", m.Main, qwen)
	}

	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpModelSet, ID: gemma}, rpc.ChoiceOnce); !strings.Contains(msg, "ollama pull gemma3:12b") {
		t.Errorf("a model Ollama lacks: %q", msg)
	}
}

// TestModelUseOp switches to a set on a running merud and saves it: the
// switch leaves config.toml alone, the next question uses the set's
// model, and the save writes it.
func TestModelUseOp(t *testing.T) {
	dir, _ := meruHome(t)
	eng := &fakeEngine{version: "0.34.0", pulled: []string{miniCPM, qwenMoE, "nomic-embed-text:latest"}}
	body := settingsHeader + "\n[[models.sets]]\nname = \"qwen-moe\"\nmain = \"" + qwenMoE + "\"\nthink = false\n"
	d := startDaemon(t, dir, body, eng)

	m := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpModelUse, ID: "qwen-moe"}), rpc.EventModels).Models
	if m == nil || m.Active != "qwen-moe" || m.Main != qwenMoE || len(m.Sets) != 1 || !m.Sets[0].ThinkOff {
		t.Fatalf("models after the switch = %+v", m)
	}
	if cfg := loadConfig(t, dir); cfg.Models.Main != miniCPM {
		t.Errorf("config main = %q after a switch; want it unchanged", cfg.Models.Main)
	}
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "hello there", Scope: rpc.ScopeTalk})
	eng.mu.Lock()
	used := eng.streamModel
	eng.mu.Unlock()
	if used != qwenMoE {
		t.Errorf("the next question went to %q, want %q", used, qwenMoE)
	}
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpModelSave})
	if cfg := loadConfig(t, dir); cfg.Models.Main != qwenMoE {
		t.Errorf("config main = %q after a save, want %q", cfg.Models.Main, qwenMoE)
	}
}
