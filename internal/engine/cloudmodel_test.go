// This file tests the guard against Ollama's cloud models: the engine
// sends no chat or embedding request for a model that runs off this
// machine, whether its name says so or Ollama's /api/show does.

package engine

import (
	"context"
	"errors"
	"testing"
)

func TestCloudModelRefused(t *testing.T) {
	f := newFakeOllama(t, map[string]route{
		"/api/chat": {200, `{"message":{"role":"assistant","content":"hi"},"done":true}`},
		// A cloud model copied to a plain name: only /api/show tells.
		"/api/show": {200, `{"capabilities":["completion"],"remote_host":"https://ollama.com:443","remote_model":"gpt-oss:120b"}`},
	})
	e := newTestEngine(t, f, "")
	ctx := context.Background()

	if _, err := e.Generate(ctx, nil, nil, Options{Model: "gpt-oss:120b-cloud"}); !errors.Is(err, ErrCloudModel) {
		t.Errorf("Generate with a cloud tag = %v, want ErrCloudModel", err)
	}
	if _, err := e.Stream(ctx, nil, nil, Options{Model: "renamed"}); !errors.Is(err, ErrCloudModel) {
		t.Errorf("Stream of a model /api/show calls remote = %v, want ErrCloudModel", err)
	}
	if f.sent("/api/chat") {
		t.Error("the engine sent a chat request for a cloud model")
	}
}

func TestLocalModelPasses(t *testing.T) {
	f := newFakeOllama(t, map[string]route{
		"/api/chat": {200, `{"message":{"role":"assistant","content":"hi"},"done":true}`},
		"/api/show": {200, `{"capabilities":["completion"]}`},
	})
	e := newTestEngine(t, f, "")
	if _, err := e.Generate(context.Background(), nil, nil, Options{Model: "qwen3.6:35b"}); err != nil {
		t.Errorf("Generate with a local model = %v", err)
	}
}

func TestCloudEmbedModelRefused(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/embed": {200, `{"embeddings":[[0.1]]}`}})
	e, err := NewOllama(f.srv.URL, "", "nomic-embed:cloud", f.srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), []Input{{Text: "x"}}); !errors.Is(err, ErrCloudModel) {
		t.Errorf("Embed with a cloud model = %v, want ErrCloudModel", err)
	}
	if f.sent("/api/embed") {
		t.Error("the engine sent an embedding request for a cloud model")
	}
}

// sent reports whether the fake received any request on path.
func (f *fakeOllama) sent(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.bodies[path]
	return ok
}
