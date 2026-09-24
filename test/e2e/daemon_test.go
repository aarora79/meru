//go:build e2e

// This file tests merud as a process: the startup checks that make it
// refuse to run, one daemon per socket, a clean shutdown, the modes of the
// files it creates, and that it sends nothing anywhere by default.

package e2e

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// TestStartupRefusals starts merud in setups it must refuse, and checks that
// it exits 1 with a message naming the problem, without claiming the socket.
func TestStartupRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fakeArgs []string // nil: no fake needed
		config   func(fakeURL string) string
		want     []string // pieces stderr must contain
	}{
		{
			name:     "Ollama too old",
			fakeArgs: []string{"-version", "0.12.0"},
			config:   func(u string) string { return fakeConfig(u, "") },
			want:     []string{"found Ollama 0.12.0", "too old", "0.12.11"},
		},
		{
			// 10.0.0.1 is a private address on another machine. merud must
			// refuse it from the config alone, without trying to connect.
			name:   "non-loopback Ollama",
			config: func(string) string { return fakeConfig("http://10.0.0.1:11434", "") },
			want:   []string{"ollama.base_url", "10.0.0.1", "not a loopback address"},
		},
		{
			name:     "non-loopback OTLP endpoint",
			fakeArgs: []string{},
			config: func(u string) string {
				return fakeConfig(u, "[observability]\notlp_endpoint = \"http://10.0.0.1:4318\"\n")
			},
			want: []string{"observability.otlp_endpoint", "not a loopback address"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			url := ""
			if tt.fakeArgs != nil {
				url = startFake(t, tt.fakeArgs...).url
			}
			h := newHome(t)
			h.writeConfig(t, tt.config(url))
			m := startMerud(t, h, nil)
			if code := m.wait(t, readyTimeout); code != 1 {
				t.Errorf("merud exited %d, want 1", code)
			}
			stderr := m.stderr.String()
			for _, want := range tt.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
			if !errNotExist(h.socket) {
				t.Errorf("merud left a socket at %s after refusing to start", h.socket)
			}
		})
	}
}

// TestSingleInstance starts a second merud on a socket the first one holds.
// The second must refuse, and the first must keep answering.
func TestSingleInstance(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	second := startMerud(t, s.home, nil)
	if code := second.wait(t, readyTimeout); code != 1 {
		t.Errorf("second merud exited %d, want 1", code)
	}
	if want := "another merud is already listening on " + s.home.socket; !strings.Contains(second.stderr.String(), want) {
		t.Errorf("second merud's stderr = %q, want it to contain %q", second.stderr.String(), want)
	}
	if res := runMeru(t, s.home, "ping"); res.code != 0 {
		t.Errorf("first merud stopped answering: exit %d, stderr %q", res.code, res.stderr)
	}
}

// TestShutdown stops merud with each signal it handles, and checks that it
// exits 0, removes its socket and logs the stop.
func TestShutdown(t *testing.T) {
	t.Parallel()
	signals := []struct {
		name string
		sig  os.Signal
	}{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGINT", syscall.SIGINT},
	}
	for _, tt := range signals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := startStack(t)
			s.merud.signal(t, tt.sig)
			if code := s.merud.wait(t, exitTimeout); code != 0 {
				t.Errorf("merud exited %d, want 0\nstderr:\n%s", code, s.merud.stderr.String())
			}
			if !errNotExist(s.home.socket) {
				t.Errorf("socket %s still exists after shutdown", s.home.socket)
			}
			if log := s.home.log(); !strings.Contains(log, "merud stopped") {
				t.Errorf("merud.log has no stop line:\n%s", log)
			}
			if res := runMeru(t, s.home, "ping"); res.code != 1 || !strings.Contains(res.stderr, "is merud running?") {
				t.Errorf("meru ping after shutdown: exit %d, stderr %q; want 1 and a hint", res.code, res.stderr)
			}
		})
	}
}

// TestFilePermissions checks that everything merud creates is private to the
// user: directories 0700, files and the socket 0600.
func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix file modes")
	}
	t.Parallel()
	s := startStack(t)
	s.fake.enqueue(t, fastModel, directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "private"})
	if res := runMeru(t, s.home, "keep this private"); res.code != 0 {
		t.Fatalf("meru exited %d, stderr %q", res.code, res.stderr)
	}
	files := sessionFiles(t, s.home)
	if len(files) != 1 {
		t.Fatalf("want one transcript, found %v", files)
	}
	month := filepath.Dir(files[0])
	year := filepath.Dir(month)

	tests := []struct {
		path string
		want fs.FileMode
	}{
		{filepath.Join(s.home.dir, "sessions"), 0o700},
		{year, 0o700},
		{month, 0o700},
		{files[0], 0o600},
		{s.home.socket, 0o600},
		{filepath.Join(s.home.dir, "merud.log"), 0o600},
	}
	for _, tt := range tests {
		info, err := os.Lstat(tt.path)
		if err != nil {
			t.Errorf("stat: %v", err)
			continue
		}
		// Perm keeps the nine rwx bits and drops the file-type bits.
		if got := info.Mode().Perm(); got != tt.want {
			t.Errorf("%s has mode %#o, want %#o", tt.path, got, tt.want)
		}
	}
}

// TestNoTelemetryByDefault runs a turn with no otlp_endpoint in config and
// checks that merud contacted nothing but Ollama.
//
// A process's outbound connections are hard to watch from outside without
// root, so the test sets traps instead. It starts a canary HTTP server and
// points every setting that could route traffic elsewhere at it: the
// standard OpenTelemetry environment variables and the HTTP proxy variables.
// Go's HTTP client sends every request to a non-loopback host through the
// proxy, so any such request, from Meru's code or a library's, would reach
// the canary. After the turn and a clean shutdown, which flushes any
// exporter, the canary must have seen nothing, and the fake must have seen
// only Ollama API calls.
func TestNoTelemetryByDefault(t *testing.T) {
	t.Parallel()
	// atomic.Int64 is a counter that goroutines can update without a lock;
	// the canary's handler runs in a goroutine of its own.
	var hits atomic.Int64
	var paths syncBuffer
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = paths.Write([]byte(r.Method + " " + r.URL.String() + "\n"))
	}))
	t.Cleanup(canary.Close)

	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, fakeConfig(f.url, ""))
	if strings.Contains(fakeConfig(f.url, ""), "otlp_endpoint") {
		t.Fatal("the test config sets otlp_endpoint; this test needs the default")
	}
	env := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + canary.URL,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=" + canary.URL,
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=" + canary.URL,
		"OTEL_TRACES_EXPORTER=otlp",
		"OTEL_METRICS_EXPORTER=otlp",
		"HTTP_PROXY=" + canary.URL,
		"HTTPS_PROXY=" + canary.URL,
		"http_proxy=" + canary.URL,
		"https_proxy=" + canary.URL,
		"NO_PROXY=",
		"no_proxy=",
	}
	m := startMerud(t, h, env)
	waitReady(t, h, m, readyTimeout)

	f.enqueue(t, fastModel, directRoute())
	f.enqueue(t, mainModel, fakeollama.Reply{Text: "nothing leaves"})
	if res := runMeru(t, h, "does anything leave?"); res.code != 0 {
		t.Fatalf("meru exited %d, stderr %q", res.code, res.stderr)
	}
	m.signal(t, syscall.SIGTERM)
	if code := m.wait(t, exitTimeout); code != 0 {
		t.Fatalf("merud exited %d\nstderr:\n%s", code, m.stderr.String())
	}

	if n := hits.Load(); n != 0 {
		t.Errorf("merud sent %d requests past loopback Ollama:\n%s", n, paths.String())
	}
	ollamaAPI := map[string]bool{"/api/version": true, "/api/ps": true, "/api/chat": true, "/api/embed": true}
	for _, r := range f.requests(t) {
		if !ollamaAPI[r.Path] {
			t.Errorf("fake got %s %s, which isn't an Ollama API call merud needs", r.Method, r.Path)
		}
	}
}
