//go:build integration

// This file holds the tests that download for real, into a temporary
// Meru home: the pinned Node from nodejs.org, then obsidian-mcp 2.0.1
// from the npm registry; and the pinned uv, then workspace-mcp 1.30.0
// from PyPI. They run only with the integration build tag:
//
//	go test -tags integration -run TestIntegration ./internal/connectors/
//
// CI never runs them, since they need the internet and a few hundred MB.

package connectors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestIntegrationObsidian installs the real obsidian connector, then runs
// its program with the pinned Node and asks it for its help text.
func TestIntegrationObsidian(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m Manifest) bool { return m.ID == "obsidian" })
	m := ms[i]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	meruDir := filepath.Join(t.TempDir(), ".meru")
	in := NewInstaller(meruDir, t.TempDir())
	inst, err := in.Install(ctx, m, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Logf("installed %+v", inst)

	vault := t.TempDir()
	path, args, env, err := in.LaunchCommand(m, inst, map[string]string{"vault_path": vault, "vault_name": "notes"})
	if err != nil {
		t.Fatalf("LaunchCommand: %v", err)
	}
	// Run the script with --help in place of serve and its arguments,
	// so it prints and exits instead of waiting on stdin for MCP.
	c := Cmd{Path: path, Args: []string{args[0], "--help"}, Env: []string{"HOME=" + t.TempDir(), "PATH=" + env["PATH"]}}
	out, err := in.Run(ctx, c, nil)
	if err != nil {
		t.Fatalf("run %s: %v\n%s", path, err, out)
	}
	if !strings.Contains(out, "serve") {
		t.Errorf("--help printed %q, want it to mention serve", out)
	}
	// Nothing landed outside the temporary Meru home.
	if _, err := os.Stat(filepath.Join(meruDir, "runtime", "cache", "npm")); err != nil {
		t.Errorf("npm's cache isn't inside the runtime folder: %v", err)
	}
}

// TestIntegrationGoogle installs the real Google connector, starts
// workspace-mcp on a free port with a made-up OAuth client and an empty
// home folder, and runs the manifest's health check. With no saved
// sign-in, the check must come back as a *SignInError whose link is
// Google's sign-in page, calling back to the server's own port. This is
// the evidence behind the manifest's [health] table. The test never uses
// port 8000, where the user's own server may run.
func TestIntegrationGoogle(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m Manifest) bool { return m.ID == "google" })
	m := ms[i]

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	meruDir := filepath.Join(t.TempDir(), ".meru")
	home := t.TempDir()
	in := NewInstaller(meruDir, home)
	inst, err := in.Install(ctx, m, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Logf("installed %+v", inst)

	// Invented values: the server builds the sign-in link from them and
	// sends nothing to Google before the user opens it.
	values := map[string]string{
		"email":         "dana@example.com",
		"client_id":     "123-invented.apps.googleusercontent.com",
		"client_secret": "invented-secret",
	}
	c, err := childCmd(in, m, inst, values)
	if err != nil {
		t.Fatalf("childCmd: %v", err)
	}
	// Take a free port for this test, and give the server its own empty
	// HOME, so no saved sign-in of the user's plays a part.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	for i, kv := range c.Env {
		switch {
		case strings.HasPrefix(kv, "WORKSPACE_MCP_PORT="):
			c.Env[i] = fmt.Sprintf("WORKSPACE_MCP_PORT=%d", port)
		case strings.HasPrefix(kv, "HOME="):
			c.Env[i] = "HOME=" + home
		}
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	procCtx, kill := context.WithCancel(context.Background())
	out := &lockedBuffer{}
	startCtx, stopWait := context.WithTimeout(ctx, 2*time.Minute)
	defer stopWait()
	tr, exited, err := httpTransport(startCtx, procCtx, c, out, endpoint)
	if err != nil {
		kill()
		t.Fatalf("start workspace-mcp: %v\n%s", err, out.String())
	}
	defer func() {
		kill()
		<-exited
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "meru-test"}, nil)
	cs, err := client.Connect(startCtx, tr, nil)
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, out.String())
	}
	defer cs.Close()
	var names []string
	for tool, err := range cs.Tools(startCtx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	for _, want := range append([]string{m.Health.Tool}, m.MCP.Allow...) {
		if !slices.Contains(names, want) {
			t.Errorf("workspace-mcp %s doesn't offer %s; it offers %v", m.Install.Version, want, names)
		}
	}

	err = runHealthCheck(startCtx, cs, m.Health, true)
	var signIn *SignInError
	if !errors.As(err, &signIn) {
		t.Fatalf("health check = %v, want a *SignInError\n%s", err, out.String())
	}
	u, perr := url.Parse(signIn.URL)
	if perr != nil || u.Host != "accounts.google.com" {
		t.Fatalf("sign-in link = %v (%v), want one on accounts.google.com", u, perr)
	}
	q := u.Query()
	if got, want := q.Get("redirect_uri"), fmt.Sprintf("http://localhost:%d/oauth2callback", port); got != want {
		t.Errorf("redirect_uri = %q, want %q", got, want)
	}
	if q.Get("client_id") != values["client_id"] || q.Get("login_hint") != values["email"] {
		t.Errorf("the link names client_id %q and login_hint %q", q.Get("client_id"), q.Get("login_hint"))
	}
	t.Logf("%d tools; the check wants a sign-in at https://%s%s", len(names), u.Host, u.Path)
}

// lockedBuffer is an io.Writer that keeps what the program prints, for
// the test's error messages. The program's stdout and stderr may write
// at once, so a mutex guards it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

// Write keeps p.
func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// String returns what was written so far.
func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}
