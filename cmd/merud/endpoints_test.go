// This file tests the [[endpoints]] wiring: an endpoint whose key is
// missing still lets merud start, and a switch to a set whose main model
// is an endpoint sends that endpoint nothing.

package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// quietLog is a logger that writes nowhere.
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNewEndpointsKeepsOneWithNoKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.toml"), []byte("open_key = \"sk-123456789\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Dir: dir, Endpoints: []config.Endpoint{
		{Name: "open", Reach: config.ReachLocal, URL: "http://127.0.0.1:1/v1",
			APIKey: "secret:open_key", Model: "m", ContextLength: 8192},
		{Name: "locked", Reach: config.ReachLocal, URL: "http://127.0.0.1:1/v1",
			APIKey: "secret:missing_key", Model: "m", ContextLength: 8192, Vision: true, Tools: new(false)},
	}}
	eps, err := newEndpoints(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if ep := eps["open"]; ep.Err != nil || ep.Engine == nil {
		t.Errorf("open = %+v", ep)
	}
	locked := eps["locked"]
	if !errors.Is(locked.Err, errNoKey) {
		t.Errorf("locked's error = %v, want errNoKey", locked.Err)
	}
	if !slices.Equal(locked.Details.Capabilities, []string{"completion", engine.Vision}) {
		t.Errorf("locked's capabilities = %v; tools = false leaves tools out", locked.Details.Capabilities)
	}
}

func TestModelUseToAnEndpointLoadsNothing(t *testing.T) {
	// The endpoint counts the requests it gets; a switch must send none.
	var calls atomic.Int32
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"hi"}}]}`)
	}))
	t.Cleanup(ep.Close)
	oa, err := engine.NewOpenAI(engine.EndpointConfig{Name: "lab", URL: ep.URL, Model: "served"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	srv := fakeollama.Start(t, fakeollama.Config{Version: "0.40.5", Models: everyModel, Loaded: []string{miniCPM, qwen}, Capabilities: ollamaCaps})
	local, err := engine.NewOllama(srv.URL, "-1", "nomic-embed-text", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.NewEngines(local, map[string]engine.Endpoint{"lab": {Engine: oa, Model: "served",
		Details: engine.ModelDetails{Capabilities: []string{"completion", engine.ToolUse}, ContextLength: 32768}}})

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(modelsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	answer := &fixedAnswer{main: qwen}
	sets := append(slices.Clone(testSets), config.ModelSet{Name: "far", Main: "@lab"})
	cfg := config.Config{Profile: "lite", Models: config.Models{Fast: miniCPM, Main: qwen, Embed: "nomic-embed-text", Sets: sets}}
	m := newModelService(cfg, path, "", eng, answer, func(f func() error) error { return f() }, quietLog())
	m.wait, m.poll = 200*time.Millisecond, 5*time.Millisecond
	m.fitWindow = windowFitter(eng, 8192, 64, quietLog())

	got, err := use(t, m, "far", false)
	if err != nil {
		t.Fatalf("use far: %v", err)
	}
	if answer.main != "@lab" || got.Active != "far" {
		t.Errorf("after the switch: main %q, active %q", answer.main, got.Active)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the switch sent the endpoint %d requests, want none", n)
	}
	if w := eng.Window("@lab"); w != 32768 {
		t.Errorf("the endpoint's window = %d, want its context_length", w)
	}
	// The old answer model left Ollama's memory, as for any switch.
	if info := m.info(t.Context(), m.activeSet()); slices.Contains(info.Loaded, qwen) {
		t.Errorf("loaded = %v; qwen should be unloaded", info.Loaded)
	}
	// And back to a local set: nothing to unload from the endpoint.
	if _, err := use(t, m, "qwen-moe", false); err != nil {
		t.Fatalf("use qwen-moe: %v", err)
	}
	if answer.main != qwenMoE {
		t.Errorf("main = %q after switching back", answer.main)
	}
}
