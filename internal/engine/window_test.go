// This file tests the context windows: the cache size worked out from a
// model's attention shape, FitWindow's choice, and the num_ctx a chat call
// sends for a model with a window of its own.

package engine

import (
	"context"
	"encoding/json"
	"testing"
)

// info turns a JSON object of model_info, as /api/show sends it, into the
// map kvBytesPerToken reads.
func info(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestKVBytesPerToken(t *testing.T) {
	// The shapes are what Ollama reported for these models.
	tests := []struct {
		name string
		info string
		want int64
	}{
		// qwen3.6:35b: a head pair in every fourth of 40 layers, 256 wide:
		// 10 layers × 2 heads × (256 + 256) × 2 bytes.
		{"hybrid with a list per layer", `{"qwen35moe.block_count":40,
			"qwen35moe.attention.head_count_kv":[0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2,0,0,0,2],
			"qwen35moe.attention.key_length":256,"qwen35moe.attention.value_length":256}`, 20480},
		// MiniCPM5-2B: 2 heads in each of 42 layers, 128 wide.
		{"one number for every layer", `{"llama.block_count":42,"llama.attention.head_count_kv":2,
			"llama.attention.key_length":128,"llama.attention.value_length":128}`, 43008},
		// No key_length: the head is embedding_length / head_count wide.
		{"head size from the embedding", `{"x.block_count":10,"x.attention.head_count_kv":4,
			"x.attention.head_count":16,"x.embedding_length":2048}`, 10 * 4 * 256 * 2},
		// gemma4 reports no heads, so the size is unknown.
		{"no shape", `{"gemma4.block_count":30,"gemma4.context_length":262144}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kvBytesPerToken(info(t, tt.info)); got != tt.want {
				t.Errorf("kvBytesPerToken = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFitWindow(t *testing.T) {
	const gb = int64(1) << 30
	qwen := ModelDetails{ContextLength: 262144, KVBytesPerToken: 20480}
	tests := []struct {
		name     string
		d        ModelDetails
		budget   int64
		fallback int
		want     int
	}{
		{"the model's whole window fits", qwen, 8 * gb, 32768, 262144},
		{"memory caps it", qwen, 2 * gb, 32768, 104448}, // 2 GiB / 20 KiB = 104,857, down to a multiple of 1,024
		{"never below the fallback", qwen, gb / 8, 32768, 32768},
		{"a small model keeps the fallback", ModelDetails{ContextLength: 8192, KVBytesPerToken: 1024}, 8 * gb, 32768, 32768},
		{"no shape keeps the fallback", ModelDetails{ContextLength: 262144}, 8 * gb, 32768, 32768},
		{"fallback 0 leaves it to Ollama", qwen, 8 * gb, 0, 0},
		{"unknown memory keeps the fallback", qwen, 0, 32768, 32768},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FitWindow(tt.d, tt.budget, tt.fallback); got != tt.want {
				t.Errorf("FitWindow = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestChatSendsTheModelsWindow(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, chatReply}})
	e := newTestEngine(t, f, "-1")
	e.ContextLength = 32768
	e.SetWindow("qwen3.6:35b", 262144)
	numCtx := func(model string) any {
		t.Helper()
		if _, err := e.Generate(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Options{Model: model}); err != nil {
			t.Fatal(err)
		}
		opts, _ := f.body(t, "/api/chat")["options"].(map[string]any)
		return opts["num_ctx"]
	}
	if got := numCtx("qwen3.6:35b"); got != float64(262144) {
		t.Errorf("answer model num_ctx = %v, want 262144", got)
	}
	if got := numCtx("minicpm"); got != float64(32768) {
		t.Errorf("other model num_ctx = %v, want the fallback 32768", got)
	}
	e.SetWindow("qwen3.6:35b", 0)
	if got := numCtx("qwen3.6:35b"); got != float64(32768) {
		t.Errorf("num_ctx after SetWindow 0 = %v, want the fallback", got)
	}
}
