// This file tests the engine parts behind picture turns and about_meru: a
// message's Images go out as base64 strings in /api/chat, and Capabilities
// and Details read /api/show once per model.

package engine

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestChatSendsImagesAsBase64(t *testing.T) {
	f := newFakeOllama(t, map[string]route{"/api/chat": {200, `{"message":{"content":"A tomato bed."},"done":true}`}})
	e := newTestEngine(t, f, "")
	png := []byte("\x89PNG\r\n\x1a\nfake")
	msgs := []Message{
		{Role: RoleSystem, Content: "Be brief."},
		{Role: RoleUser, Content: "What grows here?", Images: [][]byte{png}},
	}
	if _, err := e.Generate(context.Background(), msgs, nil, Options{Model: "m"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body := f.body(t, "/api/chat")
	sent, _ := body["messages"].([]any)
	if len(sent) != 2 {
		t.Fatalf("messages = %v", body["messages"])
	}
	system, _ := sent[0].(map[string]any)
	if _, ok := system["images"]; ok {
		t.Errorf("the system message has images: %v", system)
	}
	user, _ := sent[1].(map[string]any)
	want := []any{base64.StdEncoding.EncodeToString(png)}
	if !reflect.DeepEqual(user["images"], want) {
		t.Errorf("user images = %v, want %v", user["images"], want)
	}
}

func TestCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    []string
		wantErr bool
	}{
		{"vision model", 200, `{"capabilities":["completion","vision","tools","thinking"],"details":{"family":"qwen"}}`,
			[]string{"completion", "vision", "tools", "thinking"}, false},
		{"text model", 200, `{"capabilities":["completion","tools"]}`, []string{"completion", "tools"}, false},
		{"older Ollama without the field", 200, `{"details":{}}`, nil, false},
		{"model not pulled", 404, `{"error":"model 'm' not found"}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// calls counts the requests, to check the cache. atomic.Int32
			// is safe to add to from the server's goroutine.
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/show" || r.Method != http.MethodPost {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			e, err := NewOllama(srv.URL, "", "", srv.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, err := e.Capabilities(context.Background(), "m")
				if (err != nil) != tt.wantErr {
					t.Fatalf("Capabilities error = %v, want error %v", err, tt.wantErr)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Capabilities = %v, want %v", got, tt.want)
				}
			}
			// A good answer is kept; a failure asks again.
			want := int32(1)
			if tt.wantErr {
				want = 2
			}
			if calls.Load() != want {
				t.Errorf("Ollama got %d requests, want %d", calls.Load(), want)
			}
		})
	}
}

// TestDetails checks that Details reads the size, the quantization and the
// context length from /api/show, whatever the architecture's key prefix,
// and that Capabilities and Details share one request.
func TestDetails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"capabilities":["completion","vision","tools","thinking"],` +
			`"details":{"family":"qwen35moe","parameter_size":"36.0B","quantization_level":"Q4_K_M"},` +
			`"model_info":{"general.architecture":"qwen35moe","qwen35moe.context_length":262144,"qwen35moe.block_count":40},` +
			`"license":"a long text Meru skips"}`))
	}))
	t.Cleanup(srv.Close)
	e, err := NewOllama(srv.URL, "", "", srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Details(context.Background(), "qwen3.6:35b")
	if err != nil {
		t.Fatal(err)
	}
	want := ModelDetails{
		Capabilities:  []string{"completion", "vision", "tools", "thinking"},
		ParameterSize: "36.0B", Quantization: "Q4_K_M", ContextLength: 262144,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Details = %+v, want %+v", got, want)
	}
	// Changing the copy must not change the cache.
	got.Capabilities[0] = "changed"
	caps, err := e.Capabilities(context.Background(), "qwen3.6:35b")
	if err != nil || caps[0] != "completion" {
		t.Errorf("Capabilities = %v, %v; the cache changed with a caller's copy", caps, err)
	}
	if calls.Load() != 1 {
		t.Errorf("Ollama got %d requests, want 1", calls.Load())
	}
}

func TestCapabilitiesNeedsAModel(t *testing.T) {
	f := newFakeOllama(t, nil)
	e := newTestEngine(t, f, "")
	if _, err := e.Capabilities(context.Background(), ""); err == nil {
		t.Error("Capabilities with no model succeeded")
	}
}
