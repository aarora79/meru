// This file tests the Google step: the input checks, the start script and
// its mode, the launchd job, and the whole step with a fake launchctl and
// a fake server, including that the secret never reaches the screen.

package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// goodGoogle is a well-formed input. The secret is made up.
func goodGoogle() GoogleInput {
	return GoogleInput{
		Email:    "dana.reyes@example.com",
		ClientID: "12345-abc.apps.googleusercontent.com",
		Secret:   "GOCSPX-madeUpSecretForTests",
	}
}

// TestGoogleInputCheck checks each refusal, and that no message quotes the
// secret.
func TestGoogleInputCheck(t *testing.T) {
	tests := []struct {
		name string
		edit func(*GoogleInput)
		want string // "" means no error
	}{
		{"good", func(*GoogleInput) {}, ""},
		{"no email", func(in *GoogleInput) { in.Email = "" }, "Google address is empty"},
		{"email without @", func(in *GoogleInput) { in.Email = "dana" }, "needs an @"},
		{"wrong client ID", func(in *GoogleInput) { in.ClientID = "12345" }, "apps.googleusercontent.com"},
		{"no secret", func(in *GoogleInput) { in.Secret = "" }, "client secret is empty"},
		{"a quote in the secret", func(in *GoogleInput) { in.Secret = "GOCSPX-a'b" }, "client secret holds"},
		{"a newline in the secret", func(in *GoogleInput) { in.Secret = "GOCSPX-a\nexport X=1" }, "client secret holds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := goodGoogle()
			tt.edit(&in)
			err := in.check()
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one holding %q", err, tt.want)
			}
			if in.Secret != "" && strings.Contains(err.Error(), in.Secret) {
				t.Errorf("the message quotes the secret: %v", err)
			}
		})
	}
}

// TestWriteStartScript checks the script's lines and its mode, 0700, on a
// first write and over an older, wider file.
func TestWriteStartScript(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "start.sh")
	writeFile(t, old, "#!/bin/sh\n")
	if err := os.Chmod(old, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := WriteStartScript(dir, goodGoogle())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
	text := readFile(t, path)
	for _, want := range []string{
		"export GOOGLE_OAUTH_CLIENT_ID='12345-abc.apps.googleusercontent.com'\n",
		"export GOOGLE_OAUTH_CLIENT_SECRET='GOCSPX-madeUpSecretForTests'\n",
		"export USER_GOOGLE_EMAIL='dana.reyes@example.com'\n",
		`export WORKSPACE_ATTACHMENT_DIR="$HOME/meru-output/attachments"`,
		"--tool-tier extended --tools gmail calendar drive docs",
		"--transport streamable-http",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("start.sh lacks %q", want)
		}
	}
}

// TestGooglePlist checks the launchd job's paths.
func TestGooglePlist(t *testing.T) {
	p := Paths{Home: "/Users/dana"}
	plist := GooglePlist(p)
	for _, want := range []string{
		"<string>com.meru.workspace-mcp</string>",
		"<string>/Users/dana/.config/workspace-mcp/start.sh</string>",
		"<string>/opt/homebrew/bin:/usr/local/bin:/Users/dana/.local/bin:/usr/bin:/bin</string>",
		"<string>/Users/dana/.config/workspace-mcp/server.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(GooglePlist(Paths{Home: "/Users/a&b"}), "a&b") {
		t.Error("a home folder with & went into the plist unescaped")
	}
}

// TestSetUpGoogle runs the whole step: launchctl loads the job, the fake
// server answers, config gets the google entry, and neither the news nor
// the result nor the config holds the secret. A second run keeps one
// entry.
func TestSetUpGoogle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotAcceptable) // what workspace-mcp answers a plain GET
	}))
	defer srv.Close()
	p := tempHome(t)
	in := goodGoogle()

	for run := range 2 {
		r := &fakeRunner{}
		say, lines := collect()
		msg, err := SetUpGoogle(context.Background(), r.run, clientTo(srv), p, in, say)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		plist := filepath.Join(p.LaunchAgents(), "com.meru.workspace-mcp.plist")
		calls := strings.Join(r.called(), "\n")
		if !strings.Contains(calls, "launchctl unload "+plist) || !strings.Contains(calls, "launchctl load -w "+plist) {
			t.Errorf("calls:\n%s", calls)
		}
		for _, text := range append(*lines, msg, calls, readFile(t, p.Config())) {
			if strings.Contains(text, in.Secret) {
				t.Fatalf("the secret leaked into %q", text)
			}
		}
		if !strings.Contains(msg, "sign-in link") {
			t.Errorf("the result doesn't say how the first sign-in works: %q", msg)
		}
	}
	cfg, err := config.Load(p.Config())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range cfg.MCP.Servers {
		if s.Name == "google" {
			n++
			if s.URL != "http://127.0.0.1:8000/mcp" {
				t.Errorf("google url = %q", s.URL)
			}
		}
	}
	if n != 1 {
		t.Errorf("config has %d google entries, want 1", n)
	}
}

// TestSetUpGoogleBadInput checks that a bad input stops the step before it
// runs anything or writes a file.
func TestSetUpGoogleBadInput(t *testing.T) {
	p := tempHome(t)
	r := &fakeRunner{}
	in := goodGoogle()
	in.ClientID = "not-a-client"
	if _, err := SetUpGoogle(context.Background(), r.run, deadClient(), p, in, func(string) {}); err == nil {
		t.Fatal("a bad client ID passed")
	}
	if calls := r.called(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
	if _, err := os.Stat(p.Workspace()); err == nil {
		t.Error("wrote the start script for a bad input")
	}
}
