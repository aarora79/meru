// This file tests the facts merud hands about_meru, over a setup with two
// MCP servers, one reached with a secret header and one started with a
// secret and a plain value in its env, a local command, an indexed folder,
// a memory and a disabled skill. The tool's answer must name the models
// and the sources, and hold none of the secret or env values, nor the
// memory's text.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
)

// Details makes fakeEngine a modelDetailer, as OllamaEngine is, with fixed
// answers about any model.
func (f *fakeEngine) Details(ctx context.Context, model string) (engine.ModelDetails, error) {
	return engine.ModelDetails{
		Capabilities:  []string{"completion", "vision", "tools", "thinking"},
		ParameterSize: "36.0B", Quantization: "Q4_K_M", ContextLength: 262144,
	}, nil
}

func TestAboutMeruFacts(t *testing.T) {
	ctx := t.Context()
	dir := shortDir(t)
	notes := filepath.Join(dir, "notes")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "garden.md"), []byte("# Garden\n\nSow the beans in April.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The values that must never reach the model.
	const (
		headerToken = "Bearer about-token-5678"
		envKey      = "env-key-9876"
		plainEnv    = "plain-env-value-4321"
		memoryText  = "The user keeps bees behind the shed."
	)
	if err := os.WriteFile(filepath.Join(dir, "secrets.toml"),
		[]byte(fmt.Sprintf("tok = %q\nenvkey = %q\n", headerToken, envKey)), 0o600); err != nil {
		t.Fatal(err)
	}
	url, lastAuth := startMCPServerAuth(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`
[models]
main = "qwen3.6:35b"

[index]
folders = [%q]

[skills]
disabled = ["explainer"]

[[mcp.servers]]
name    = "files"
url     = %q
headers = { Authorization = "secret:tok" }
allow   = ["echo", "fail"]

[[mcp.servers]]
name    = "notes"
command = %q
args    = ["-test.run=^$"]
env     = { %s = "1", %s = %q, GORACE = "atexit_sleep_ms=0", NOTES_KEY = "secret:envkey", NOTES_MODE = %q }
allow   = ["echo"]

[[commands]]
name        = "disk-free"
description = "Free space on each mounted disk"
argv        = ["df", "-h"]
`, notes, url, exe, stdioServerEnv, stdioPIDEnv, filepath.Join(dir, "pid"), plainEnv)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	eng := &fakeEngine{version: "0.34.0"}
	log := obs.Discard()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "meru.db"), EmbedModel: cfg.Models.Embed, Dims: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mem, err := memory.Open(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Add("me", memoryText, "test"); err != nil {
		t.Fatal(err)
	}
	ix, err := index.New(cfg.Index, st, eng, log)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := newToolService(ctx, cfg, cfgPath, st, mem, ix, searchAdapter{st: st, eng: eng}, eng, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tools.Close)
	mems := memoryService{mem: mem, log: log}
	idx := newIndexService(ix, st, mems, cfg.Index, cfgPath, nil, log)
	if _, err := idx.scanAll(ctx, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sk, err := newSkillService(filepath.Join(dir, "skills"), cfg.Skills.Disabled, log)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	tools.bt.UseAbout(aboutService{
		cfg: cfg, main: func() string { return cfg.Models.Main }, eng: eng, st: st, folders: idx.currentFolders, servers: tools.dispatcher.Servers,
		skills: sk, mem: mem, machine: "The user's computer: macOS 26.0 (arm64), Apple M4 Max, 64 GB memory.",
		home: home, log: log,
	}.facts)

	// The call goes through dispatch, as a model's would.
	res, outcome := tools.dispatcher.Dispatch(ctx, dispatch.Call{ID: "call-1", Name: builtin.AboutMeru, Args: json.RawMessage(`{}`)})
	if outcome.Outcome != dispatch.OutcomeOK || res.IsError {
		t.Fatalf("about_meru = %+v, %v", res, outcome)
	}
	text := res.Text
	t.Logf("about_meru said:\n%s", text)
	for _, want := range []string{
		"Profile: lite.",
		"Ollama runs every model on this computer; no model runs in the cloud.\nOllama version: 0.34.0\n",
		"Answer model (main): qwen3.6:35b.",
		"36.0B parameters, Q4_K_M, a context of 262144 tokens. It can: vision, tools, thinking.",
		"Fast model: hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M.",
		"Embedding model: nomic-embed-text.",
		"Apple M4 Max",
		"The index holds 1 file in 1 chunk",
		"files (connected, 2 tools)",
		"notes (connected, 1 tool)",
		"A2A agents: none.",
		"Local commands: cmd.disk-free.",
		builtin.AboutMeru,
		"turned off: explainer",
		"Memories: me 1.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("about_meru lacks %q", want)
		}
	}
	// The header did reach the server, so the secret was live.
	if lastAuth() != headerToken {
		t.Errorf("the server saw Authorization %q, want the secret", lastAuth())
	}
	for _, secret := range []string{headerToken, "about-token", envKey, plainEnv, memoryText, "bees"} {
		if strings.Contains(text, secret) {
			t.Errorf("about_meru holds %q, which must stay out", secret)
		}
	}
	if n := utf8.RuneCountInString(text); n > 2000 {
		t.Errorf("about_meru is %d characters, over 2,000", n)
	}
}
