// This file tests AnthropicEngine against a fake Anthropic-style server.

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

// newTestAnthropic returns an AnthropicEngine for the fake f, with key.
func newTestAnthropic(t *testing.T, f *fakeEndpoint, key string) *AnthropicEngine {
	t.Helper()
	e, err := NewAnthropic(EndpointConfig{Name: "lab", URL: f.srv.URL + "/v1", Key: key, Model: "my-model"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestAnthropicStreamTextAndTools(t *testing.T) {
	f := newFakeEndpoint(t, http.StatusOK, sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":40}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: ping\ndata: {\"type\":\"ping\"}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Let me \"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"look.\"}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"grep\",\"input\":{}}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"pattern\\\": \\\"he\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"llo\\\"}\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":21}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	))
	e := newTestAnthropic(t, f, "sk-ant")
	seq, err := e.Stream(context.Background(), []Message{
		{Role: RoleSystem, Content: "rule one"},
		{Role: RoleSystem, Content: "rule two"},
		{Role: RoleUser, Content: "find hello"},
	}, []ToolSpec{{Name: "grep", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}, Options{MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	text, last, err := collect(t, seq)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Let me look." {
		t.Errorf("text = %q; the thinking must stay out", text)
	}
	if last.DoneReason != "stop" || last.Usage.PromptTokens != 40 || last.Usage.OutputTokens != 21 {
		t.Errorf("last delta = %+v", last)
	}
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "tu_1" || last.ToolCalls[0].Name != "grep" ||
		string(last.ToolCalls[0].Arguments) != `{"pattern": "hello"}` {
		t.Errorf("tool calls = %+v", last.ToolCalls)
	}

	path, h, body := f.request(t)
	if path != "/v1/messages" {
		t.Errorf("path = %q", path)
	}
	if h.Get("x-api-key") != "sk-ant" || h.Get("anthropic-version") != anthropicVersion || h.Get("Authorization") != "" {
		t.Errorf("headers = %v", h)
	}
	if body["system"] != "rule one\n\nrule two" || body["max_tokens"] != 50.0 || body["model"] != "my-model" {
		t.Errorf("body = %v", body)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["input_schema"] == nil {
		t.Errorf("tools = %v", body["tools"])
	}
}

func TestAnthropicMessages(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xe0 rest of a picture")
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "what is in it?", Images: [][]byte{jpeg}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "grep"}, {Name: "view_image", Arguments: json.RawMessage(`{"path":"a.jpg"}`)}}},
		{Role: RoleTool, ToolName: "grep", Content: "no match"},
		{Role: RoleTool, ToolName: "view_image", Content: "the picture", Images: [][]byte{jpeg}},
		{Role: RoleUser, Content: "and?"},
		{Role: RoleAssistant, Content: ""},
	}
	system, out := toAnMessages(msgs)
	if system != "sys" {
		t.Errorf("system = %q", system)
	}
	// user, assistant, then one user message holding both results and the
	// next question; the empty assistant message goes.
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3: %+v", len(out), out)
	}
	u := out[0].Content
	if len(u) != 2 || u[0].Type != "image" || u[0].Source.MediaType != "image/jpeg" || u[1].Type != "text" {
		t.Errorf("first message = %+v", u)
	}
	a := out[1].Content
	if len(a) != 2 || a[0].Type != "tool_use" || a[0].ID != "t1" || string(a[0].Input) != "{}" || a[1].ID == "" {
		t.Errorf("assistant message = %+v", a)
	}
	r := out[2].Content
	if out[2].Role != "user" || len(r) != 3 {
		t.Fatalf("results message = %+v", out[2])
	}
	if r[0].Type != "tool_result" || r[0].ToolUseID != "t1" || r[1].ToolUseID != a[1].ID {
		t.Errorf("results = %+v", r)
	}
	if len(r[1].Content) != 2 || r[1].Content[1].Type != "image" {
		t.Errorf("the picture result = %+v", r[1].Content)
	}
	if r[2].Type != "text" || r[2].Text != "and?" {
		t.Errorf("the next question = %+v", r[2])
	}
}

func TestAnthropicGenerate(t *testing.T) {
	f := newFakeEndpoint(t, http.StatusOK, `{"content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"cut"}],`+
		`"stop_reason":"max_tokens","usage":{"input_tokens":5,"output_tokens":2}}`)
	c, err := newTestAnthropic(t, f, "").Generate(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "cut" || c.DoneReason != "length" || c.Usage.OutputTokens != 2 {
		t.Errorf("completion = %+v", c)
	}
	_, h, body := f.request(t)
	if h.Get("x-api-key") != "" {
		t.Error("sent x-api-key with no key")
	}
	if body["max_tokens"] != float64(defaultMaxTokens) {
		t.Errorf("max_tokens = %v, want the default", body["max_tokens"])
	}
}

func TestAnthropicStreamErrors(t *testing.T) {
	cases := map[string]struct {
		body      string
		modelSide bool
	}{
		"error event": {sse(
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{}}}",
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}",
		), true},
		"cut short": {sse(
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}",
		), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeEndpoint(t, http.StatusOK, c.body)
			seq, err := newTestAnthropic(t, f, "").Stream(context.Background(), []Message{{Role: RoleUser, Content: "q"}}, nil, Options{})
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
