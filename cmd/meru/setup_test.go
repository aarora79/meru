// This file tests `meru setup`, and `meru mcp add` with merud down, with
// scripted input: each test feeds the answers a person would type and
// checks what landed in config.toml and secrets.toml, and what the user
// saw. mcp_test.go tests the probe step against a fake merud.

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// fakeKey stands in for an API key. It is long enough for Redact and
// obviously not a real one.
const fakeKey = "fake-obsidian-key-0123456789"

// scripted returns a console that reads input as typed answers, runs no
// programs (it records them in ran), and finds Ollama up.
func scripted(input string) (c *console, out *bytes.Buffer, ran *[]string) {
	out = &bytes.Buffer{}
	ran = &[]string{}
	c = &console{
		in:  bufio.NewReader(strings.NewReader(input)),
		out: out,
		run: func(_ context.Context, name string, args ...string) error {
			*ran = append(*ran, name+" "+strings.Join(args, " "))
			return nil
		},
		ollamaVersion: func(context.Context, string) (string, error) { return "0.12.11", nil },
		// Nothing answers at a server's URL unless a test says so.
		answers: func(context.Context, string) bool { return false },
		// SearXNG answers unless a test says otherwise.
		searxng: func(context.Context, string) error { return nil },
	}
	c.readSecret = c.line
	return c, out, ran
}

// loadServers loads the config.toml in dir and returns its MCP servers.
func loadServers(t *testing.T, dir string) []config.MCPServer {
	t.Helper()
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.MCP.Servers
}

func TestMCPAddDoIt(t *testing.T) {
	dir := t.TempDir()
	c, out, _ := scripted("d\n" + fakeKey + "\ny\n")
	if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "obsidian"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	servers := loadServers(t, dir)
	if len(servers) != 1 || servers[0].Name != "obsidian" || servers[0].Env["OBSIDIAN_API_KEY"] != "secret:obsidian_api_key" {
		t.Errorf("servers = %+v", servers)
	}
	s, err := secrets.Load(secrets.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve("secret:obsidian_api_key"); got != fakeKey {
		t.Errorf("saved key = %q", got)
	}
	if strings.Contains(out.String(), fakeKey) {
		t.Error("the key shows in the output")
	}
	for _, want := range []string{"[[mcp.servers]]", "merud isn't running", "merud &"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestMCPAddReusesSavedKey(t *testing.T) {
	dir := t.TempDir()
	if err := secrets.Set(secrets.Path(dir), "obsidian_api_key", fakeKey); err != nil {
		t.Fatal(err)
	}
	c, out, _ := scripted("d\ny\n")
	if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "obsidian"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Using obsidian_api_key") {
		t.Errorf("output doesn't say it reused the key:\n%s", out)
	}
	if len(loadServers(t, dir)) != 1 {
		t.Error("the server wasn't written")
	}
}

// TestMCPAddWritesNothing covers the paths that must leave both files
// alone: show me how, skip, a no at the end, and an empty key.
func TestMCPAddWritesNothing(t *testing.T) {
	tests := []struct {
		name, input, wantOut string
	}{
		{"show me how", "s\n", "obsidian_api_key = \"<paste it here>\""},
		{"skip", "x\nk\n", "[d/s/k]"},
		{"say no", "d\n" + fakeKey + "\nn\n", "Nothing was written"},
		{"empty key", "d\n\n", "No key given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			c, out, _ := scripted(tt.input)
			if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "obsidian"}, c); err != nil {
				t.Fatalf("mcp add: %v", err)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output lacks %q:\n%s", tt.wantOut, out)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("wrote %d files, want none", len(entries))
			}
		})
	}
}

func TestMCPListCatalog(t *testing.T) {
	for _, args := range [][]string{{"list-catalog"}, {"add"}} {
		c, out, _ := scripted("")
		if err := mcpCmd(context.Background(), "merud.sock", args, c); err != nil {
			t.Fatal(err)
		}
		for _, name := range catalog.Names() {
			if !strings.Contains(out.String(), name) {
				t.Errorf("%v: listing lacks %s", args, name)
			}
		}
	}
}

func TestMCPAddAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("[[mcp.servers]]\nname = \"obsidian\"\ncommand = \"uvx\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, out, _ := scripted("")
	if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "obsidian"}, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already has") {
		t.Errorf("output = %s", out)
	}
}

// TestSetupWithMain checks `meru setup --main <model>`, the way
// scripts/install.sh runs it: no profile question, lite's router and
// embedding model plus the given answer model, and a new config.toml that
// names it with thinking off.
func TestSetupWithMain(t *testing.T) {
	dir := t.TempDir()
	input := "\n" + // download: yes, the default
		"\n" + // no folders
		strings.Repeat("k\n", len(catalog.Entries())) // skip each catalog server
	c, out, ran := scripted(input)
	c.ollamaVersion = func(context.Context, string) (string, error) { return "0.34.0", nil }

	if err := setupCmd(context.Background(), filepath.Join(dir, "merud.sock"), c, "qwen3.6:35b"); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	lite, _ := config.ProfileModels("lite")
	wantRan := []string{"ollama pull " + lite.Fast, "ollama pull qwen3.6:35b", "ollama pull " + lite.Embed}
	if !slices.Equal(*ran, wantRan) {
		t.Errorf("ran %q, want %q", *ran, wantRan)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != "lite" || cfg.Models.Main != "qwen3.6:35b" || cfg.Models.Fast != lite.Fast ||
		!cfg.Models.ThinkOff() || cfg.Ollama.ContextLength != 32768 {
		t.Errorf("config = profile %q, models %+v, context %d; want lite with qwen3.6:35b, thinking off, 32768",
			cfg.Profile, cfg.Models, cfg.Ollama.ContextLength)
	}
	if strings.Contains(out.String(), "Profile: lite") {
		t.Errorf("setup asked for a profile:\n%s", out)
	}
}

// TestSetupFirstRun walks setup with no config and no merud: Ollama is down
// at first, the user picks full, downloads, gives one bad folder then good
// ones, and skips every server.
func TestSetupFirstRun(t *testing.T) {
	dir := t.TempDir()
	input := "\n" + // Ollama is down: press Enter to check again
		"full\n" + // profile
		"\n" + // download: yes, the default
		"notes\n" + // not absolute: asked again
		"~/notes, /srv/papers\n" +
		strings.Repeat("k\n", len(catalog.Entries())) // skip each catalog server
	c, out, ran := scripted(input)
	checks := 0
	c.ollamaVersion = func(context.Context, string) (string, error) {
		checks++
		if checks == 1 {
			return "", errors.New("connection refused")
		}
		return "0.12.11", nil
	}

	if err := setupCmd(context.Background(), filepath.Join(dir, "merud.sock"), c, ""); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}

	full, _ := config.ProfileModels("full")
	wantRan := []string{"ollama pull " + full.Fast, "ollama pull " + full.Main, "ollama pull " + full.Embed}
	if !slices.Equal(*ran, wantRan) {
		t.Errorf("ran %q, want %q", *ran, wantRan)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != "full" || !slices.Equal(cfg.Index.Folders, []string{"~/notes", "/srv/papers"}) {
		t.Errorf("config = profile %q, folders %q", cfg.Profile, cfg.Index.Folders)
	}
	// The empty [models] lines in the template leave the tiers to the
	// profile, so picking full gives full's models.
	if cfg.Models.Fast != full.Fast || cfg.Models.Main != full.Main || cfg.Models.Embed != full.Embed {
		t.Errorf("models = %+v, want the full profile's %+v", cfg.Models, full)
	}
	written, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if !strings.Contains(string(written), "[[mcp.servers]]") || !strings.Contains(string(written), "\nfolders = [\"~/notes\", \"/srv/papers\"]\n") {
		t.Errorf("setup didn't write the filled-in template:\n%s", written)
	}
	for _, want := range []string{"Ollama isn't answering", "must be an absolute path", "meru setup user", "merud isn't running"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestSetupExistingConfig runs setup against a running merud with a config
// already in place: setup leaves config.toml alone, skips the download,
// and prints merud's answer to the test question.
func TestSetupExistingConfig(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		if req.Op == rpc.OpAsk {
			return emit(rpc.Event{Type: rpc.EventToken, Text: "I answer questions."})
		}
		return nil
	})
	configPath := filepath.Join(filepath.Dir(sock), "config.toml")
	orig := "# mine\nprofile = \"lite\"\n"
	if err := os.WriteFile(configPath, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	// No download, skip each server, and no to setup user.
	c, out, ran := scripted("n\n" + strings.Repeat("k\n", len(catalog.Entries())) + "n\n")
	if err := setupCmd(context.Background(), sock, c, ""); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %q after a no", *ran)
	}
	if got, _ := os.ReadFile(configPath); string(got) != orig {
		t.Errorf("config.toml changed to:\n%s", got)
	}
	for _, want := range []string{"Setup leaves", "I answer questions."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestSetupWebSearch walks the web search step: SearXNG down, then
// answering HTML, then JSON; an empty URL; and a skip.
func TestSetupWebSearch(t *testing.T) {
	down := fmt.Errorf("%w on http://127.0.0.1:8888", catalog.ErrSearXNGDown)
	html := fmt.Errorf("%w on http://127.0.0.1:8888", catalog.ErrSearXNGNoJSON)
	tests := []struct {
		name    string
		url     string
		answers []error // what each check returns, in order
		input   string
		want    []string
	}{
		{"answers", "http://127.0.0.1:8888", []error{nil}, "", []string{"SearXNG answers JSON at http://127.0.0.1:8888"}},
		{
			"down, then html, then json", "http://127.0.0.1:8888", []error{down, html, nil}, "\n\n",
			[]string{"SearXNG isn't answering on http://127.0.0.1:8888", "[connectors.searxng]\n  enabled = true",
				"restart merud", "JSON is off", "settings.yml", "SearXNG answers JSON"},
		},
		{"skip", "http://127.0.0.1:8888", []error{down}, "s\n", []string{"[connectors.searxng]", "Skipped."}},
		{
			"down at another address", "http://127.0.0.1:9999", []error{fmt.Errorf("%w on http://127.0.0.1:9999", catalog.ErrSearXNGDown)}, "s\n",
			[]string{"SearXNG isn't answering on http://127.0.0.1:9999. Start the SearXNG you run there", "Skipped."},
		},
		{"off", "", nil, "", []string{"Web search is off"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, out, _ := scripted(tt.input)
			checks := 0
			c.searxng = func(_ context.Context, u string) error {
				if u != tt.url {
					t.Errorf("checked %q, want %q", u, tt.url)
				}
				checks++
				return tt.answers[checks-1]
			}
			if err := c.checkWebSearch(context.Background(), tt.url); err != nil {
				t.Fatalf("checkWebSearch: %v\n%s", err, out)
			}
			if checks != len(tt.answers) {
				t.Errorf("checked %d times, want %d", checks, len(tt.answers))
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestSetupWebSearchReal runs the step's real check against httptest
// servers: one answers JSON, one answers HTML then gets fixed, and one
// port is closed.
func TestSetupWebSearchReal(t *testing.T) {
	jsonOn := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !jsonOn {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<!doctype html><title>403 Forbidden</title>")
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": "No query"}`)
	}))
	defer srv.Close()

	c, out, _ := scripted("\n")
	c.searxng = func(ctx context.Context, u string) error {
		err := catalog.CheckSearXNG(ctx, u)
		jsonOn = true // the user fixes settings.yml before pressing Enter
		return err
	}
	if err := c.checkWebSearch(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "JSON is off") || !strings.Contains(out.String(), "SearXNG answers JSON") {
		t.Errorf("output:\n%s", out)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.URL
	closed.Close()
	c, out, _ = scripted("s\n")
	c.searxng = catalog.CheckSearXNG
	if err := c.checkWebSearch(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SearXNG isn't answering on "+addr) {
		t.Errorf("output:\n%s", out)
	}
}

func TestSetupStopsWithoutOllama(t *testing.T) {
	c, _, _ := scripted("q\n")
	c.ollamaVersion = func(context.Context, string) (string, error) { return "", errors.New("down") }
	err := setupCmd(context.Background(), filepath.Join(t.TempDir(), "merud.sock"), c, "")
	if err == nil || !strings.Contains(err.Error(), "Ollama isn't running") {
		t.Errorf("error = %v", err)
	}
}

func TestOllamaVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"version":"0.12.11"}`))
	}))
	defer srv.Close()

	v, err := ollamaVersion(context.Background(), srv.URL+"/")
	if err != nil || v != "0.12.11" {
		t.Errorf("ollamaVersion = %q, %v", v, err)
	}
	if _, err := ollamaVersion(context.Background(), srv.URL+"/missing"); err == nil {
		t.Error("a 404 counted as Ollama running")
	}
}

func TestOllamaInstallHint(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if !strings.Contains(ollamaInstallHint(goos), "ollama") {
			t.Errorf("%s: hint = %q", goos, ollamaInstallHint(goos))
		}
	}
}

// TestFirstConfig checks the template setup fills in: with the defaults
// it is the template itself, and otherwise only the lines it fills in
// change, and the result loads with the answer model it names.
func TestFirstConfig(t *testing.T) {
	same, err := firstConfig("lite", "", nil)
	if err != nil || same != config.Template() {
		t.Fatalf("firstConfig(lite, none) changed the template: %v", err)
	}
	got, err := firstConfig("lite", "qwen3.6:35b", []string{"~/notes", `C:\Users\me "quoted"`})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		`main  = "qwen3.6:35b"   # picked for this Mac's memory; writes the answer`: true,
		`folders = ["~/notes", "C:\\Users\\me \"quoted\""]`:                         true,
	}
	// The Windows path above isn't valid on every system, so load a copy
	// with a plain folder.
	loadable, err := firstConfig("lite", "qwen3.6:35b", []string{"~/notes"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(loadable), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Models.Main != "qwen3.6:35b" || cfg.Profile != "lite" || !cfg.Models.ThinkOff() {
		t.Errorf("loaded %q main %q, think off %v, err %v; want lite, qwen3.6:35b, thinking off",
			cfg.Profile, cfg.Models.Main, cfg.Models.ThinkOff(), err)
	}
	tmpl, lines := strings.Split(config.Template(), "\n"), strings.Split(got, "\n")
	if len(tmpl) != len(lines) {
		t.Fatalf("firstConfig has %d lines, the template %d", len(lines), len(tmpl))
	}
	for i := range lines {
		if lines[i] == tmpl[i] {
			continue
		}
		if !want[lines[i]] {
			t.Errorf("line %d changed to %q", i+1, lines[i])
		}
		delete(want, lines[i])
	}
	if len(want) != 0 {
		t.Errorf("lines not written: %v", want)
	}
}

// TestConfigTemplateCmd checks that `meru config template` prints the
// template as it is.
func TestConfigTemplateCmd(t *testing.T) {
	var out strings.Builder
	if code := run(context.Background(), []string{"config", "template"}, &out, io.Discard); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if out.String() != config.Template() {
		t.Error("meru config template didn't print the template")
	}
}
