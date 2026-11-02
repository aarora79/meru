// This file tests OpenAIEngine against a fake OpenAI-style server. The
// integration test in openai_integration_test.go runs it against Ollama's
// own /v1.

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// newTestOpenAI returns an OpenAIEngine for the fake f, with key.
func newTestOpenAI(t *testing.T, f *fakeEndpoint, key string) *OpenAIEngine {
	t.Helper()
	e, err := NewOpenAI(EndpointConfig{Name: "lab", URL: f.srv.URL + "/v1", Key: key, Model: "my-model"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// sse joins events into a text/event-stream body, a blank line after each.
func sse(events ...string) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString(ev)
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestOpenAIStreamText(t *testing.T) {
	f := newFakeEndpoint(t, http.StatusOK, sse(
		`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"let me think"}}]}`,
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":", world"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
		`data: [DONE]`,
	))
	e := newTestOpenAI(t, f, "sk-1")
	seq, err := e.Stream(context.Background(), []Message{
		{Role: RoleSystem, Content: "be brief"},
		{Role: RoleUser, Content: "hi"},
	}, nil, Options{Model: "@lab", MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	text, last, err := collect(t, seq)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello, world" {
		t.Errorf("text = %q; the reasoning must stay out", text)
	}
	if !last.Done || last.DoneReason != "stop" || last.Usage.PromptTokens != 12 || last.Usage.OutputTokens != 3 {
		t.Errorf("last delta = %+v", last)
	}

	path, h, body := f.request(t)
	if path != "/v1/chat/completions" {
		t.Errorf("path = %q", path)
	}
	if got := h.Get("Authorization"); got != "Bearer sk-1" {
		t.Errorf("Authorization = %q", got)
	}
	if body["model"] != "my-model" || body["stream"] != true || body["max_tokens"] != 100.0 {
		t.Errorf("body = %v", body)
	}
	if so, _ := body["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage", body["stream_options"])
	}
}

func TestOpenAINoKeyNoHeader(t *testing.T) {
	f := newFakeEndpoint(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	e := newTestOpenAI(t, f, "")
	c, err := e.Generate(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "ok" {
		t.Errorf("text = %q", c.Text)
	}
	_, h, body := f.request(t)
	if h.Get("Authorization") != "" {
		t.Error("sent an Authorization header with no key")
	}
	if body["max_tokens"] != float64(defaultMaxTokens) {
		t.Errorf("max_tokens = %v, want the default %d", body["max_tokens"], defaultMaxTokens)
	}
}

func TestOpenAIStreamJoinsToolCalls(t *testing.T) {
	// Two calls; the first one's arguments split mid-string, and the ID and
	// name come only in each call's first piece.
	f := newFakeEndpoint(t, http.StatusOK, sse(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"grep","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pattern\":\"hel"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"datetime","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"lo\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	))
	e := newTestOpenAI(t, f, "")
	seq, err := e.Stream(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, []ToolSpec{{Name: "grep"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, last, err := collect(t, seq)
	if err != nil {
		t.Fatal(err)
	}
	if last.DoneReason != "stop" || len(last.ToolCalls) != 2 {
		t.Fatalf("last delta = %+v", last)
	}
	c0, c1 := last.ToolCalls[0], last.ToolCalls[1]
	if c0.ID != "c1" || c0.Name != "grep" || string(c0.Arguments) != `{"pattern":"hello"}` {
		t.Errorf("call 0 = %+v (%s)", c0, c0.Arguments)
	}
	if c1.ID != "c2" || c1.Name != "datetime" || string(c1.Arguments) != "{}" {
		t.Errorf("call 1 = %+v (%s)", c1, c1.Arguments)
	}
	_, _, body := f.request(t)
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	if fn := tools[0].(map[string]any)["function"].(map[string]any); fn["name"] != "grep" {
		t.Errorf("tool = %v", tools[0])
	}
}

func TestOpenAIMessages(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n rest of a picture")
	msgs := []Message{
		{Role: RoleUser, Content: "what is this?", Images: [][]byte{png}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "view_image", Arguments: json.RawMessage(`{"path":"a.png"}`)}}},
		{Role: RoleTool, ToolName: "view_image", Content: "the picture", Images: [][]byte{png}},
		{Role: RoleAssistant, Content: "a cat"},
	}
	out := toOAMessages(msgs)
	if len(out) != 5 {
		t.Fatalf("got %d messages, want 5 (the tool's picture in a user message after it): %+v", len(out), out)
	}
	parts, ok := out[0].Content.([]oaPart)
	if !ok || len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("first message's content = %#v", out[0].Content)
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if parts[1].ImageURL.URL != wantURL {
		t.Errorf("image url = %q", parts[1].ImageURL.URL)
	}
	if got := out[1].ToolCalls[0]; got.ID == "" || got.Function.Arguments != `{"path":"a.png"}` {
		t.Errorf("tool call = %+v", got)
	}
	if out[2].Role != "tool" || out[2].ToolCallID != out[1].ToolCalls[0].ID || out[2].Content != "the picture" {
		t.Errorf("tool result = %+v", out[2])
	}
	if out[3].Role != "user" {
		t.Errorf("message after the tool result = %+v, want the picture as a user message", out[3])
	}
	if out[4].Content != "a cat" {
		t.Errorf("last message = %+v", out[4])
	}
}

func TestOpenAILengthIsCutOff(t *testing.T) {
	f := newFakeEndpoint(t, http.StatusOK, sse(
		`data: {"choices":[{"delta":{"content":"a long"},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	))
	seq, err := newTestOpenAI(t, f, "").Stream(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Options{MaxTokens: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, last, err := collect(t, seq); err != nil || last.DoneReason != "length" {
		t.Errorf("done reason = %q, err = %v; want length", last.DoneReason, err)
	}
}

func TestOpenAIStreamErrors(t *testing.T) {
	cases := map[string]struct {
		body      string
		modelSide bool // the error wraps ErrModelOutput
	}{
		"error event": {sse(`data: {"choices":[{"delta":{"content":"a"}}]}`, `data: {"error":{"message":"overloaded"}}`), true},
		"cut short":   {sse(`data: {"choices":[{"delta":{"content":"a"}}]}`), false},
		"not JSON":    {sse(`data: {not json`), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeEndpoint(t, http.StatusOK, c.body)
			seq, err := newTestOpenAI(t, f, "").Stream(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = collect(t, seq)
			if err == nil {
				t.Fatal("the stream ended with no error")
			}
			if errors.Is(err, ErrModelOutput) != c.modelSide {
				t.Errorf("errors.Is(err, ErrModelOutput) = %v, want %v: %v", !c.modelSide, c.modelSide, err)
			}
			if name == "cut short" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
			}
		})
	}
}

func TestOpenAIRefusesLogProbsAndEmbed(t *testing.T) {
	f := newFakeEndpoint(t, http.StatusOK, `{}`)
	e := newTestOpenAI(t, f, "")
	if _, err := e.Generate(context.Background(), nil, nil, Options{LogProbs: true}); !errors.Is(err, ErrNoLogProbs) {
		t.Errorf("Generate with LogProbs: err = %v", err)
	}
	if _, err := e.Embed(context.Background(), Texts("x")); !errors.Is(err, ErrNoEmbed) {
		t.Errorf("Embed: err = %v", err)
	}
}

func TestOpenAICancel(t *testing.T) {
	// The fake answers with one piece of text and then holds the
	// connection open until the test ends.
	release := make(chan struct{})
	f := &fakeEndpoint{}
	f.srv = newHoldingServer(t, release, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
	e, err := NewOpenAI(EndpointConfig{Name: "lab", URL: f.srv.URL, Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seq, err := e.Stream(ctx, []Message{{Role: RoleUser, Content: "q"}}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var gotErr error
	for d, err := range seq {
		if d.Text == "a" {
			cancel()
		}
		gotErr = err
	}
	close(release)
	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", gotErr)
	}
}
