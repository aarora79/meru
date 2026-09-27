// This file tests the two model ops. The first tests run the model
// service on the real OllamaEngine against the fake Ollama, which answers
// /api/tags and /api/show: which known models are installed, what each
// can do and which tier uses it, and what switching the answer model
// writes to config.toml. The last runs a whole merud and checks that the
// next question uses the new model, with no restart.

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// The model names the tests use: the three known ones.
const (
	miniCPM = "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M"
	qwen    = "qwen3.6:35b"
	gemma   = "gemma3:12b"
)

// Pulled makes fakeEngine a modelLister, as OllamaEngine is.
func (f *fakeEngine) Pulled(ctx context.Context) ([]string, error) {
	return f.pulled, nil
}

// fixedAnswer stands in for the agent: it holds the answer model.
type fixedAnswer struct{ main string }

// Main returns the answer model.
func (a *fixedAnswer) Main() string { return a.main }

// SetMain changes the answer model.
func (a *fixedAnswer) SetMain(model string) { a.main = model }

// modelsConfig is a config.toml with a comment and an unrelated key, which
// every change must keep.
const modelsConfig = "# my models, keep me\n[models]\nmain = \"" + qwen + "\" # the big one\n\n[agent]\nhistory_turns = 3\n"

// newModelService returns a model service on the real OllamaEngine
// against a fake Ollama that has pulled the models in pulled, with the
// capabilities in caps, and a config.toml holding modelsConfig. The fast
// model is MiniCPM5-2B and the answer model qwen3.6:35b.
func newModelService(t *testing.T, pulled []string, caps map[string][]string) (modelService, *fixedAnswer) {
	t.Helper()
	srv := fakeollama.Start(t, fakeollama.Config{Version: "0.34.0", Models: pulled, Capabilities: caps})
	eng, err := engine.NewOllama(srv.URL, "", "nomic-embed-text", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(modelsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	answer := &fixedAnswer{main: qwen}
	return modelService{
		models: config.Models{Fast: miniCPM, Main: qwen, Embed: "nomic-embed-text"}, profile: "lite",
		configPath: path, eng: eng, answer: answer,
		edit: func(f func() error) error { return f() },
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, answer
}

// ollamaCaps is what Ollama 0.34 lists for the three models.
var ollamaCaps = map[string][]string{
	miniCPM: {"tools", "thinking", "completion"},
	qwen:    {"completion", "vision", "tools", "thinking"},
	gemma:   {"completion", "vision"},
}

func TestModelChoices(t *testing.T) {
	m, _ := newModelService(t, []string{miniCPM, qwen, "nomic-embed-text:latest"}, ollamaCaps)
	info := m.info(t.Context())
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
		{qwen, true, []string{"main"}, ollamaCaps[qwen]},
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
			m, answer := newModelService(t, tt.pulled, ollamaCaps)
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
