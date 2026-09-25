//go:build integration

// This file routes a few obvious questions through the real local Ollama and
// prints the distribution for each. The build tag above keeps it out of a
// plain `go test`; run it with
//
//	go test -tags integration -v -run Integration ./internal/router/
//
// It skips when Ollama isn't running. MERU_TEST_OLLAMA and
// MERU_TEST_FAST_MODEL override the address and the model.

package router

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// envOr returns the environment variable key, or def when it is unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestIntegrationDecide(t *testing.T) {
	baseURL := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_FAST_MODEL", "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M")

	eng, err := engine.NewOllama(baseURL, "5m", "", nil, nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	info, err := eng.Info(ctx)
	if err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}
	if err := eng.CheckVersion(ctx); err != nil {
		t.Fatalf("CheckVersion: %v", err)
	}
	t.Logf("Ollama %s, model %s", info.RuntimeVersion, model)

	cfg := Config{Model: model, TopLogProbs: 20, Temperature: 1, MinConfidence: 0.45, Fallback: RouteSearchTools}

	// Warm the model first, so the timings below measure a warm turn.
	if _, err := Decide(ctx, eng, cfg, Turn{Question: "hello"}); err != nil {
		t.Fatalf("warm-up Decide: %v", err)
	}

	questions := []struct {
		q    string
		want Route // what a person would pick; logged, not asserted
	}{
		{"What is the capital of France?", RouteDirect},
		{"Explain what a hash map is in two sentences.", RouteDirect},
		{"What did I write in my notes about the Q3 launch plan?", RouteSearch},
		{"Summarise the design doc in my meru repository.", RouteSearch},
		{"What is the weather in Paris right now?", RouteTools},
		{"Send an email to Sam saying I'll be late.", RouteTools},
		{"Compare my notes on the vendor contract with the vendor's latest news.", RouteSearchTools},
	}
	matched := 0
	for _, tt := range questions {
		start := time.Now()
		d, err := Decide(ctx, eng, cfg, Turn{Question: tt.q})
		took := time.Since(start)
		if err != nil {
			t.Fatalf("Decide(%q): %v", tt.q, err)
		}
		if d.Outcome == OutcomeDegraded {
			t.Errorf("Decide(%q): degraded; the model didn't weigh the letters", tt.q)
		}
		mark := " "
		if d.Route == tt.want {
			mark = "*"
			matched++
		}
		t.Logf("%s %-13s %-14s conf=%.3f %6.1fms  [%s]  %q",
			mark, d.Route, d.Outcome, d.Confidence, float64(took.Microseconds())/1000, formatProbs(d.Probs), tt.q)
	}
	t.Logf("%d of %d matched the expected route (* marks a match)", matched, len(questions))
}
