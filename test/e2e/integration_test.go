//go:build e2e && integration

// This file runs merud against the real Ollama on this machine, with the
// default lite profile, and checks v0.1's "done when" target: the first token
// of an answer reaches the client within one second once the models are warm.
//
// Run it with:
//
//	go test -tags 'e2e integration' -count=1 -v -run Integration ./test/e2e/...
//
// It skips itself when Ollama isn't running or a lite model isn't pulled. CI
// doesn't run it, because its machines have no models.

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// ttftTarget is the v0.1 target for time to first token after warm-up.
const ttftTarget = time.Second

// TestIntegrationLiteTTFT starts merud with no config file, so it uses the
// lite profile and the default Ollama address, asks two warm-up questions,
// then asks a third and times it from the client's side.
func TestIntegrationLiteTTFT(t *testing.T) {
	// A missing config file gives the defaults, so this reads the lite
	// profile's model names and Ollama address without naming them here.
	h := newHome(t)
	cfg, err := config.Load(h.config)
	if err != nil {
		t.Fatal(err)
	}
	skipUnlessOllamaHas(t, cfg.Ollama.BaseURL, cfg.Models.Fast, cfg.Models.Main, cfg.Models.Embed)

	m := startMerud(t, h, nil)
	// A cold start loads the models from disk, which can take minutes.
	waitReady(t, h, m, 5*time.Minute)

	const question = "What is the capital of France? Answer in one word."
	for i := range 2 {
		ttft, answer := timedAsk(t, h.socket, question)
		t.Logf("warm-up %d: time to first token %v, answer %q", i+1, ttft, answer)
	}
	ttft, answer := timedAsk(t, h.socket, question)
	t.Logf("measured: time to first token %v, answer %q", ttft, answer)

	if !strings.Contains(strings.ToLower(answer), "paris") {
		t.Errorf("answer %q doesn't mention Paris", answer)
	}
	if ttft > ttftTarget {
		t.Errorf("time to first token %v is over the %v target", ttft, ttftTarget)
	}
}

// timedAsk asks question on a new session and returns the time from sending
// the request to the first token event, and the whole answer. It fails the
// test when the reply ends in an error or has no tokens.
func timedAsk(t *testing.T, socket, question string) (time.Duration, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var ttft time.Duration
	var answer strings.Builder
	start := time.Now()
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpAsk, Text: question, Source: rpc.SourceCLI}, nil) {
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
		switch ev.Type {
		case rpc.EventToken:
			if ttft == 0 {
				ttft = time.Since(start)
			}
			answer.WriteString(ev.Text)
		case rpc.EventError:
			t.Fatalf("merud replied with an error: %s", ev.Error)
		}
	}
	if ttft == 0 {
		t.Fatal("the answer had no tokens")
	}
	return ttft, answer.String()
}

// skipUnlessOllamaHas skips the test when Ollama at baseURL doesn't answer or
// hasn't pulled every one of models.
func skipUnlessOllamaHas(t *testing.T, baseURL string, models ...string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/version")
	if err != nil {
		t.Skipf("Ollama isn't reachable at %s: %v", baseURL, err)
	}
	_ = resp.Body.Close()

	resp, err = client.Get(baseURL + "/api/tags")
	if err != nil {
		t.Skipf("list Ollama's models: %v", err)
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Skipf("decode Ollama's model list: %v", err)
	}
	var have []string
	for _, m := range tags.Models {
		have = append(have, m.Name)
	}
	for _, want := range models {
		// Ollama lists a model pulled without a tag as "name:latest".
		if !slices.Contains(have, want) && !slices.Contains(have, want+":latest") {
			t.Skipf("model %s isn't pulled (have %v); try `ollama pull %s`", want, have, want)
		}
	}
}
