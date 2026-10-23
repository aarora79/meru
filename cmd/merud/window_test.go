// This file tests windowFitter: the answer model's window from what a
// fake Ollama reports, and the fallback when it can't say.

package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
)

func TestWindowFitter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body strings.Builder
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body.Write(buf[:n])
		if !strings.Contains(body.String(), "qwen3.6:35b") {
			http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
			return
		}
		// The shape qwen3.6:35b reports: about 20 KB of cache a token.
		_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"model_info":{"qwen35moe.context_length":262144,
			"qwen35moe.block_count":40,"qwen35moe.attention.head_count_kv":[0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2],
			"qwen35moe.attention.key_length":256,"qwen35moe.attention.value_length":256}}`))
	}))
	t.Cleanup(srv.Close)
	e, err := engine.NewOllama(srv.URL, "", "", srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.ContextLength = 32768
	quiet := slog.New(slog.DiscardHandler)

	fit := windowFitter(e, 32768, 64, quiet)
	fit(context.Background(), "qwen3.6:35b")
	if got := e.Window("qwen3.6:35b"); got != 262144 {
		t.Errorf("64 GB Mac: window = %d, want the model's 262144", got)
	}
	fit(context.Background(), "missing:1b")
	if got := e.Window("missing:1b"); got != 32768 {
		t.Errorf("a model Ollama can't describe: window = %d, want the fallback", got)
	}

	// On a 16 GB Mac the eighth is 2 GB, which holds about 104,000 tokens.
	small, _ := engine.NewOllama(srv.URL, "", "", srv.Client(), nil)
	windowFitter(small, 32768, 16, quiet)(context.Background(), "qwen3.6:35b")
	if got := small.Window("qwen3.6:35b"); got != 104448 {
		t.Errorf("16 GB Mac: window = %d, want 104448", got)
	}

	if windowFitter(e, 0, 64, quiet) != nil {
		t.Error("context_length = 0 should leave every window to Ollama")
	}
}
