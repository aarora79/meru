// This file tests what Embed sends: text as plain strings, byte for byte
// as before images existed; images as {"image": base64} objects; the
// model's task prefixes; and num_ctx.

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// embedBody runs Embed against a fake that answers with one vector per
// input, and returns the raw request body.
func embedBody(t *testing.T, e *OllamaEngine, f *fakeOllama, inputs []Input) string {
	t.Helper()
	if _, err := e.Embed(context.Background(), inputs); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.bodies["/api/embed"])
}

// vectorsFor returns a fake /api/embed reply with n two-number vectors.
func vectorsFor(n int) string {
	return `{"embeddings":[` + strings.TrimSuffix(strings.Repeat(`[0.1,0.2],`, n), ",") + `]}`
}

func TestEmbedWire(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	tests := []struct {
		name   string
		inputs []Input
		input  string // the JSON of the request's "input"
	}{
		{"texts stay plain strings", Texts("a", "b"), `["a","b"]`},
		{"a title goes before the text, as embedText did", []Input{{Title: "Plan", Text: "sow"}}, `["Plan\n\nsow"]`},
		{"a query with no prefix is the text", []Input{{Text: "q", Query: true}}, `["q"]`},
		{"an image is an object", []Input{{Image: png}}, `[{"image":"iVBORw0KGgpmYWtl"}]`},
		{"text and image share one object", []Input{{Text: "a cat", Image: png}}, `[{"text":"a cat","image":"iVBORw0KGgpmYWtl"}]`},
		{"text and image mix in one call", []Input{{Text: "a"}, {Image: png}}, `["a",{"image":"iVBORw0KGgpmYWtl"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeOllama(t, map[string]route{"/api/embed": {200, vectorsFor(len(tt.inputs))}})
			e := newTestEngine(t, f, "")
			body := embedBody(t, e, f, tt.inputs)
			var req map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatal(err)
			}
			if string(req["input"]) != tt.input {
				t.Errorf("input = %s, want %s", req["input"], tt.input)
			}
			// The two shapes that look right and don't work: an "images"
			// list beside input, and an image as a data: URL.
			if _, ok := req["images"]; ok || strings.Contains(body, "data:image") {
				t.Errorf("request uses a shape Ollama ignores: %s", body)
			}
			if _, ok := req["options"]; ok {
				t.Errorf("options sent with no EmbedContext: %s", body)
			}
		})
	}
}

func TestEmbedPrompts(t *testing.T) {
	gemma := Prompts{Query: "task: search result | query: {query}", Document: "title: {title} | text: {text}"}
	tests := []struct {
		name    string
		prompts Prompts
		in      Input
		want    string
	}{
		{"query", gemma, Input{Text: "tomato dates", Query: true}, "task: search result | query: tomato dates"},
		{"document with a title", gemma, Input{Title: "Garden", Text: "sow in April"}, "title: Garden | text: sow in April"},
		{"document without a title", gemma, Input{Text: "sow in April"}, "title: none | text: sow in April"},
		{"placeholders in the text stay", gemma, Input{Text: "use {title} here"}, "title: none | text: use {title} here"},
		{"no prompts: query", Prompts{}, Input{Text: "q", Query: true}, "q"},
		{"no prompts: titled document", Prompts{}, Input{Title: "T", Text: "x"}, "T\n\nx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.prompts.apply(tt.in); got != tt.want {
				t.Errorf("apply = %q, want %q", got, tt.want)
			}
		})
	}

	// An image takes no prefix, even with prompts set.
	f := newFakeOllama(t, map[string]route{"/api/embed": {200, vectorsFor(1)}})
	e := newTestEngine(t, f, "")
	e.EmbedPrompts = gemma
	if body := embedBody(t, e, f, []Input{{Image: []byte("img")}}); strings.Contains(body, "task:") || strings.Contains(body, "title:") {
		t.Errorf("an image got a prefix: %s", body)
	}
}

func TestEmbedNumCtx(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/embed": {200, vectorsFor(1)}})
	e := newTestEngine(t, f, "")
	e.EmbedContext = 8192
	body := embedBody(t, e, f, Texts("x"))
	if !strings.Contains(body, `"options":{"num_ctx":8192}`) {
		t.Errorf("request lacks num_ctx: %s", body)
	}
}
