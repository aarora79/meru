//go:build integration

// This file checks OllamaEngine against the real local Ollama. The build tag
// above keeps it out of a plain `go test`; run it with
//
//	go test -tags integration -v -run Integration ./internal/engine/
//
// It skips when Ollama isn't running. MERU_TEST_OLLAMA, MERU_TEST_FAST_MODEL
// and MERU_TEST_EMBED_MODEL override the address and the models.

package engine

import (
	"context"
	"os"
	"testing"
	"time"
)

// envOr returns the environment variable key, or def when it is unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestIntegrationOllama(t *testing.T) {
	baseURL := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_FAST_MODEL", "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M")
	embedModel := envOr("MERU_TEST_EMBED_MODEL", "nomic-embed-text")

	e, err := NewOllama(baseURL, "5m", embedModel, nil, nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := e.Info(ctx); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}
	if err := e.CheckVersion(ctx); err != nil {
		t.Fatalf("CheckVersion: %v", err)
	}

	t.Run("generate with logprobs", func(t *testing.T) {
		c, err := e.Generate(ctx, []Message{{Role: RoleUser, Content: "Reply with the single letter B.\nAnswer: "}}, nil,
			Options{Model: model, MaxTokens: 1, LogProbs: true, TopLogProbs: 5})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if len(c.LogProbs) != 1 || len(c.LogProbs[0].Top) != 5 {
			t.Fatalf("LogProbs = %+v, want one position with 5 alternatives", c.LogProbs)
		}
		if c.Usage.PromptTokens == 0 || c.Usage.OutputTokens != 1 {
			t.Errorf("Usage = %+v, want prompt tokens and one output token", c.Usage)
		}
		t.Logf("text %q, top %+v", c.Text, c.LogProbs[0].Top)
	})

	t.Run("stream", func(t *testing.T) {
		seq, err := e.Stream(ctx, []Message{{Role: RoleUser, Content: "Count from one to five."}}, nil,
			Options{Model: model, MaxTokens: 400})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		var last Delta
		for d, err := range seq {
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			last = d
		}
		if !last.Done || last.Usage.OutputTokens == 0 {
			t.Errorf("last delta = %+v, want Done with usage", last)
		}
	})

	t.Run("embed", func(t *testing.T) {
		vs, err := e.Embed(ctx, []string{"hello", "world"})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(vs) != 2 || len(vs[0]) == 0 {
			t.Errorf("got %d vectors of size %d", len(vs), len(vs[0]))
		}
	})
}
