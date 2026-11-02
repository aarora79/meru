// This file tests Engines: that each call goes to the engine its model
// names, and that an endpoint answers the Ollama-only questions from its
// [[endpoints]] entry.

package engine

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// recordingEngine is an Engine that remembers the model of each call and
// answers with fixed text.
type recordingEngine struct {
	models []string
}

func (r *recordingEngine) Generate(_ context.Context, _ []Message, _ []ToolSpec, opts Options) (Completion, error) {
	r.models = append(r.models, opts.Model)
	return Completion{Text: "from the endpoint"}, nil
}

func (r *recordingEngine) Stream(_ context.Context, _ []Message, _ []ToolSpec, opts Options) (iter.Seq2[Delta, error], error) {
	r.models = append(r.models, opts.Model)
	return func(yield func(Delta, error) bool) { yield(Delta{Text: "streamed", Done: true}, nil) }, nil
}

func (r *recordingEngine) Embed(context.Context, []Input) ([]Vector, error) {
	return nil, errors.New("recordingEngine doesn't embed")
}

func (r *recordingEngine) Info(context.Context) (ModelInfo, error) { return ModelInfo{}, nil }

// newTestEngines returns Engines over a fake Ollama and two endpoints:
// "lab", which answers through rec, and "broken", which can't.
func newTestEngines(t *testing.T, rec *recordingEngine) (*Engines, *fakeOllama) {
	t.Helper()
	f := newFakeOllama(t, map[string]route{
		"/api/chat":  {200, `{"message":{"role":"assistant","content":"from ollama"},"done":true}`},
		"/api/show":  {200, `{"capabilities":["completion"]}`},
		"/api/tags":  {200, `{"models":[{"name":"qwen:7b","size":5}]}`},
		"/api/embed": {200, `{"embeddings":[[1,2]]}`},
	})
	local, err := NewOllama(f.srv.URL, "", "embed-model", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewEngines(local, map[string]Endpoint{
		"lab": {Engine: rec, Model: "big-model", Details: ModelDetails{
			Capabilities: []string{"completion", Vision, ToolUse}, ContextLength: 65536}},
		"broken": {Err: errors.New("secret \"k\" is not in secrets.toml")},
	}), f
}

func TestEnginesRouteByModel(t *testing.T) {
	rec := &recordingEngine{}
	e, _ := newTestEngines(t, rec)
	ctx := context.Background()
	msgs := []Message{{Role: RoleUser, Content: "q"}}

	c, err := e.Generate(ctx, msgs, nil, Options{Model: "@lab"})
	if err != nil || c.Text != "from the endpoint" {
		t.Errorf("@lab: %q, %v", c.Text, err)
	}
	if !reflect.DeepEqual(rec.models, []string{"big-model"}) {
		t.Errorf("the endpoint got model %v, want the entry's own name", rec.models)
	}
	if _, err := e.Stream(ctx, msgs, nil, Options{Model: "@lab"}); err != nil {
		t.Errorf("Stream @lab: %v", err)
	}
	c, err = e.Generate(ctx, msgs, nil, Options{Model: "qwen:7b"})
	if err != nil || c.Text != "from ollama" {
		t.Errorf("a local model: %q, %v", c.Text, err)
	}
	if v, err := e.Embed(ctx, Texts("x")); err != nil || len(v) != 1 {
		t.Errorf("Embed went somewhere other than Ollama: %v, %v", v, err)
	}
	if _, err := e.Generate(ctx, msgs, nil, Options{Model: "@nope"}); err == nil {
		t.Error("an unknown endpoint answered")
	}
	_, err = e.Generate(ctx, msgs, nil, Options{Model: "@broken"})
	if err == nil || !strings.Contains(err.Error(), "secrets.toml") {
		t.Errorf("a broken endpoint: err = %v, want its reason", err)
	}
}

func TestEnginesDescribeAnEndpoint(t *testing.T) {
	e, f := newTestEngines(t, &recordingEngine{})
	ctx := context.Background()

	caps, err := e.Capabilities(ctx, "@lab")
	if err != nil || !reflect.DeepEqual(caps, []string{"completion", Vision, ToolUse}) {
		t.Errorf("capabilities = %v, %v", caps, err)
	}
	if got := e.Window("@lab"); got != 65536 {
		t.Errorf("window = %d, want context_length", got)
	}
	e.SetWindow("@lab", 10) // ignored
	if got := e.Window("@lab"); got != 65536 {
		t.Errorf("SetWindow changed an endpoint's window to %d", got)
	}
	if err := e.Unload(ctx, "@lab"); err != nil {
		t.Errorf("Unload @lab: %v", err)
	}
	f.mu.Lock()
	_, unloaded := f.bodies["/api/generate"]
	f.mu.Unlock()
	if unloaded {
		t.Error("Unload of an endpoint asked Ollama to unload something")
	}

	pulled, err := e.Pulled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range pulled {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"qwen:7b", "@lab"}) {
		t.Errorf("pulled = %v; the broken endpoint stays off the list", names)
	}
}

func TestEnginesWithRealEndpoint(t *testing.T) {
	// The whole path: Engines, an OpenAIEngine, and a fake server.
	srv := newFakeEndpoint(t, http.StatusOK, `{"choices":[{"message":{"content":"hi"}}]}`)
	oa, err := NewOpenAI(EndpointConfig{Name: "lab", URL: srv.srv.URL, Model: "served-name"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOllama(t, nil)
	local, err := NewOllama(f.srv.URL, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngines(local, map[string]Endpoint{"lab": {Engine: oa, Model: "served-name"}})
	if _, err := e.Generate(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Options{Model: "@lab"}); err != nil {
		t.Fatal(err)
	}
	if _, _, body := srv.request(t); body["model"] != "served-name" {
		t.Errorf("model sent = %v", body["model"])
	}
}
