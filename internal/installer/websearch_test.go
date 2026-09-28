// This file tests the web search step: the settings.yml it writes, the
// docker arguments, the check against a fake SearXNG, and the whole step
// with a fake docker.

package installer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// TestSearXNGSettings checks the settings file: JSON on, the address on
// loopback, the limiter off, and a new random secret each time.
func TestSearXNGSettings(t *testing.T) {
	dir := t.TempDir()
	wrote, err := WriteSearXNGSettings(dir)
	if err != nil || !wrote {
		t.Fatalf("WriteSearXNGSettings = %v, %v", wrote, err)
	}
	path := filepath.Join(dir, "settings.yml")
	text := readFile(t, path)
	for _, want := range []string{
		"use_default_settings: true",
		"  formats:\n    - html\n    - json\n",
		`base_url: "http://127.0.0.1:8888/"`,
		"limiter: false",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings.yml lacks %q:\n%s", want, text)
		}
	}
	secret := secretOf(t, text)
	if len(secret) != 64 {
		t.Errorf("secret %q isn't 64 hex digits", secret)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("settings.yml mode = %v, want 0600", info.Mode().Perm())
	}

	// A second run keeps the file and its secret.
	wrote, err = WriteSearXNGSettings(dir)
	if err != nil || wrote {
		t.Fatalf("second WriteSearXNGSettings = %v, %v; want the file kept", wrote, err)
	}
	if secretOf(t, readFile(t, path)) != secret {
		t.Error("a second run changed the secret")
	}

	// Another install gets another secret.
	other, _ := NewSecret()
	if other == secret {
		t.Error("two secrets matched")
	}
}

// secretOf returns the secret_key value in settings text.
func secretOf(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "secret_key: "); ok {
			return strings.Trim(v, `"`)
		}
	}
	t.Fatal("no secret_key line")
	return ""
}

// TestDockerRunArgs checks the docker arguments: each option is its own
// string, the port listens on loopback only, and no shell appears.
func TestDockerRunArgs(t *testing.T) {
	args := DockerRunArgs("/Users/dana/.meru/searxng")
	want := []string{
		"run", "--detach", "--name", "meru-searxng", "--restart", "unless-stopped",
		"--publish", "127.0.0.1:8888:8080",
		"--volume", "/Users/dana/.meru/searxng:/etc/searxng",
		"--env", "SEARXNG_BASE_URL=http://127.0.0.1:8888/",
		"docker.io/searxng/searxng:latest",
	}
	if !slices.Equal(args, want) {
		t.Errorf("args =\n%q\nwant\n%q", args, want)
	}
	for _, a := range args {
		if a == "sh" || a == "-c" || strings.Contains(a, "0.0.0.0") {
			t.Errorf("argument %q has no place here", a)
		}
	}
}

// fakeSearXNG answers like SearXNG: JSON when json is true, the HTML page
// SearXNG sends when JSON is off otherwise. It counts the searches.
func fakeSearXNG(json bool, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		if !json {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "<!DOCTYPE html><html>403 Forbidden</html>")
			return
		}
		if r.URL.Query().Get("q") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error": "No query"}`)
			return
		}
		io.WriteString(w, `{"query": "test", "results": [{"title": "Test", "url": "https://example.com/"}]}`)
	}))
}

// TestVerifySearXNG checks the test search against each kind of answer.
func TestVerifySearXNG(t *testing.T) {
	ok := fakeSearXNG(true, nil)
	defer ok.Close()
	html := fakeSearXNG(false, nil)
	defer html.Close()
	noResults := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"query": "test"}`)
	}))
	defer noResults.Close()

	tests := []struct {
		name   string
		client *http.Client
		want   error // nil, or an error errors.Is must match; wantAny for a plain error
		fails  bool
	}{
		{"answers JSON", clientTo(ok), nil, false},
		{"JSON is off", clientTo(html), catalog.ErrSearXNGNoJSON, true},
		{"nothing listens", deadClient(), catalog.ErrSearXNGDown, true},
		{"JSON with no results", clientTo(noResults), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySearXNG(context.Background(), tt.client, SearXNGURL)
			if (err != nil) != tt.fails {
				t.Fatalf("err = %v, want failure %v", err, tt.fails)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestSetUpWebSearch runs the whole step with a fake docker. SearXNG
// answers only once the container "starts", so the test sees the pull,
// the run and then the config writes.
func TestSetUpWebSearch(t *testing.T) {
	var started atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !started.Load() {
			// Before docker run, nothing answers: close the connection.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		io.WriteString(w, `{"query": "test", "results": []}`)
	}))
	defer srv.Close()

	p := tempHome(t)
	// Take web_fetch out of [builtin] tools, to check the step puts it back.
	tools := slices.DeleteFunc(config.BuiltinTools(), func(s string) bool { return s == "web_fetch" })
	if err := catalog.SetTableLists(p.Config(), "builtin", map[string][]string{"tools": tools}, nil); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{answer: func(program string, args []string, line func(string)) (string, error) {
		switch args[0] {
		case "inspect":
			return "", errors.New("Error: No such object: meru-searxng")
		case "pull":
			line("latest: Pulling from searxng/searxng")
			line("Status: Downloaded newer image for searxng/searxng:latest")
		case "run":
			started.Store(true)
		}
		return "", nil
	}}
	say, lines := collect()
	msg, err := SetUpWebSearch(context.Background(), r.run, clientTo(srv), p, say)
	if err != nil {
		t.Fatal(err)
	}
	calls := r.called()
	wantCalls := []string{
		"docker version --format {{.Server.Version}}",
		"docker inspect --format {{.State.Running}} meru-searxng",
		"docker pull docker.io/searxng/searxng:latest",
		"docker " + strings.Join(DockerRunArgs(p.SearXNG()), " "),
	}
	if !slices.Equal(calls, wantCalls) {
		t.Errorf("calls =\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	news := strings.Join(*lines, "\n")
	for _, want := range []string{"Downloading the web search container…", "Starting it…", "Pulling from searxng/searxng"} {
		if !strings.Contains(news, want) {
			t.Errorf("the screen never said %q:\n%s", want, news)
		}
	}
	if !strings.Contains(msg, "answered a test search") {
		t.Errorf("result = %q", msg)
	}

	cfg, err := config.Load(p.Config())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.SearXNGURL != SearXNGURL || !slices.Contains(cfg.Builtin.Tools, "web_search") || !slices.Contains(cfg.Builtin.Tools, "web_fetch") {
		t.Errorf("config: searxng_url %q, tools %v", cfg.Web.SearXNGURL, cfg.Builtin.Tools)
	}
	if !strings.Contains(readFile(t, p.Config()), "# Where SearXNG, the search engine you run on this machine, answers.") {
		t.Error("the config lost its comments")
	}
}

// TestSetUpWebSearchNoDocker checks the two ways the step stops before it
// changes anything: no docker, and a docker whose engine isn't running.
func TestSetUpWebSearchNoDocker(t *testing.T) {
	for name, dockerErr := range map[string]error{
		"not installed": ErrMissing,
		"not running":   errors.New("Cannot connect to the Docker daemon"),
	} {
		t.Run(name, func(t *testing.T) {
			p := tempHome(t)
			before := readFile(t, p.Config())
			r := &fakeRunner{answer: func(string, []string, func(string)) (string, error) { return "", dockerErr }}
			_, err := SetUpWebSearch(context.Background(), r.run, deadClient(), p, func(string) {})
			if err == nil {
				t.Fatal("the step passed without docker")
			}
			if (dockerErr == ErrMissing) != errors.Is(err, ErrNoDocker) {
				t.Errorf("err = %v", err)
			}
			if readFile(t, p.Config()) != before {
				t.Error("the step changed config.toml")
			}
		})
	}
}

// TestSetUpWebSearchAlreadyRunning checks that a SearXNG the user already
// runs is left alone: no docker call, only the config write.
func TestSetUpWebSearchAlreadyRunning(t *testing.T) {
	srv := fakeSearXNG(true, nil)
	defer srv.Close()
	p := tempHome(t)
	r := &fakeRunner{}
	msg, err := SetUpWebSearch(context.Background(), r.run, clientTo(srv), p, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if calls := r.called(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
	if !strings.Contains(msg, "already answers") {
		t.Errorf("result = %q", msg)
	}
	// The settings file is untouched too: the user's SearXNG has its own.
	if _, err := os.Stat(filepath.Join(p.SearXNG(), "settings.yml")); err == nil {
		t.Error("wrote settings.yml for a SearXNG the installer didn't start")
	}
	var parsed map[string]any
	if _, err := toml.DecodeFile(p.Config(), &parsed); err != nil {
		t.Fatal(err)
	}
}
