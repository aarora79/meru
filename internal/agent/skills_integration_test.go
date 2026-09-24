//go:build integration

// This file runs the skill pick against the real local Ollama with the
// built-in skills and prints what it picks and how long each call takes.
// The build tag above keeps it out of a plain `go test`; run it with
//
//	go test -tags integration -v -run Integration ./internal/agent/
//
// It skips when Ollama isn't running. MERU_TEST_OLLAMA and
// MERU_TEST_FAST_MODEL override the address and the model.

package agent

import (
	"context"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/router"
)

// envOr returns the environment variable key, or def when it is unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestIntegrationPickSkills(t *testing.T) {
	baseURL := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_FAST_MODEL", "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M")
	eng, err := engine.NewOllama(baseURL, "5m", "", nil, nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := eng.Info(ctx); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}

	cfg := testConfig(t)
	cfg.Models.Fast = model
	a := New(cfg, eng, nil, nil, nil, nil, nil, quietLog())
	reg := builtinRegistry(t)

	// Warm the model, so the timings below measure a warm turn.
	a.pickSkills(ctx, reg, "hello")

	questions := []struct {
		q    string
		want []string // what a person would pick; the first case is asserted
	}{
		{"write a short email to my landlord", []string{"writing"}},
		{"make an explainer page about how DNS works", []string{"explainer"}},
		{"tidy up this paragraph so it reads better: we was going to the shop", []string{"writing"}},
		{"what is the capital of France?", nil},
		{"what's the weather in Paris right now?", nil},
		{"hi there", nil},
	}
	var times []time.Duration
	matched := 0
	for i, tt := range questions {
		start := time.Now()
		got := a.pickSkills(ctx, reg, tt.q)
		took := time.Since(start)
		times = append(times, took)
		mark := " "
		if slices.Equal(got, tt.want) {
			matched++
			mark = "✓"
		}
		t.Logf("%s %-70q -> %v (want %v) in %v", mark, tt.q, got, tt.want, took.Round(time.Millisecond))
		if i == 0 && !slices.Equal(got, tt.want) {
			t.Errorf("pick for %q = %v, want %v", tt.q, got, tt.want)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("matched %d of %d; median %v, slowest %v", matched, len(questions),
		times[len(times)/2].Round(time.Millisecond), times[len(times)-1].Round(time.Millisecond))
}

// TestIntegrationRouteAndPick times the router and the pick one after the
// other and side by side, the way Handle runs them, on the same questions.
// It asserts nothing; it records why Handle runs them together.
func TestIntegrationRouteAndPick(t *testing.T) {
	baseURL := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_FAST_MODEL", "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M")
	eng, err := engine.NewOllama(baseURL, "5m", "", nil, nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := eng.Info(ctx); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}

	cfg := testConfig(t)
	cfg.Models.Fast = model
	rc, err := router.ConfigFrom(cfg.Router, model)
	if err != nil {
		t.Fatal(err)
	}
	reg := builtinRegistry(t)
	a := New(cfg, eng, realRouter{eng: eng, cfg: rc}, nil, nil, nil, nil, quietLog())
	a.UseSkills(fixedSkills{reg: reg})

	questions := []string{
		"write a short email to my landlord",
		"what is the capital of France?",
		"make an explainer page about how DNS works",
		"what did I note about the gym contract",
	}
	// Warm both prompts first.
	for _, q := range questions {
		if _, _, err := a.routeAndPick(ctx, q, nil); err != nil {
			t.Fatal(err)
		}
	}
	const reps = 3
	var serial, together time.Duration
	for range reps {
		for _, q := range questions {
			start := time.Now()
			if _, err := a.route(ctx, q, nil); err != nil {
				t.Fatal(err)
			}
			a.pickSkills(ctx, reg, q)
			serial += time.Since(start)

			start = time.Now()
			if _, _, err := a.routeAndPick(ctx, q, nil); err != nil {
				t.Fatal(err)
			}
			together += time.Since(start)
		}
	}
	n := time.Duration(reps * len(questions))
	t.Logf("route then pick: %v a turn; side by side: %v a turn",
		(serial / n).Round(time.Millisecond), (together / n).Round(time.Millisecond))
}
