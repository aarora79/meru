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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
const fakeKey = "fake-brave-key-0123456789"

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
	if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "brave"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	servers := loadServers(t, dir)
	if len(servers) != 1 || servers[0].Name != "brave" || servers[0].Env["BRAVE_API_KEY"] != "secret:brave_api_key" {
		t.Errorf("servers = %+v", servers)
	}
	s, err := secrets.Load(secrets.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve("secret:brave_api_key"); got != fakeKey {
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
	if err := secrets.Set(secrets.Path(dir), "brave_api_key", fakeKey); err != nil {
		t.Fatal(err)
	}
	c, out, _ := scripted("d\ny\n")
	if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "brave"}, c); err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Using brave_api_key") {
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
		{"show me how", "s\n", "brave_api_key = \"<paste it here>\""},
		{"skip", "x\nk\n", "[d/s/k]"},
		{"say no", "d\n" + fakeKey + "\nn\n", "Nothing was written"},
		{"empty key", "d\n\n", "No key given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			c, out, _ := scripted(tt.input)
			if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "brave"}, c); err != nil {
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
		[]byte("[[mcp.servers]]\nname = \"fetch\"\ncommand = \"uvx\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, out, _ := scripted("")
	if err := mcpCmd(context.Background(), filepath.Join(dir, "merud.sock"), []string{"add", "fetch"}, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already has") {
		t.Errorf("output = %s", out)
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
		strings.Repeat("k\n", len(setupEntries(runtime.GOOS))) // skip each catalog server
	c, out, ran := scripted(input)
	checks := 0
	c.ollamaVersion = func(context.Context, string) (string, error) {
		checks++
		if checks == 1 {
			return "", errors.New("connection refused")
		}
		return "0.12.11", nil
	}

	if err := setupCmd(context.Background(), filepath.Join(dir, "merud.sock"), c); err != nil {
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
	c, out, ran := scripted("n\n" + strings.Repeat("k\n", len(setupEntries(runtime.GOOS))) + "n\n")
	if err := setupCmd(context.Background(), sock, c); err != nil {
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

func TestSetupStopsWithoutOllama(t *testing.T) {
	c, _, _ := scripted("q\n")
	c.ollamaVersion = func(context.Context, string) (string, error) { return "", errors.New("down") }
	err := setupCmd(context.Background(), filepath.Join(t.TempDir(), "merud.sock"), c)
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
