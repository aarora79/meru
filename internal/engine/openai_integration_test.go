//go:build integration

// This file checks OpenAIEngine against a real server that speaks the
// OpenAI chat completions API with no key: the local Ollama's own /v1.
// Run it with
//
//	go test -tags integration -v -run IntegrationOpenAI ./internal/engine/
//
// It skips when Ollama isn't running. MERU_TEST_OLLAMA sets the address,
// and MERU_TEST_MAIN_MODEL the model, which must call tools and see
// pictures.

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestIntegrationOpenAIOnOllama(t *testing.T) {
	base := envOr("MERU_TEST_OLLAMA", "http://127.0.0.1:11434")
	model := envOr("MERU_TEST_MAIN_MODEL", "qwen3.6:35b")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	local, err := NewOllama(base, "5m", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Info(ctx); err != nil {
		t.Skipf("Ollama not reachable at %s: %v", base, err)
	}
	e, err := NewOpenAI(EndpointConfig{Name: "ollama-v1", URL: base + "/v1", Model: model}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A thinking model spends tokens on reasoning before it answers, so
	// the cap leaves room for both.
	opts := Options{MaxTokens: 4096}

	t.Run("stream", func(t *testing.T) {
		seq, err := e.Stream(ctx, []Message{{Role: RoleUser, Content: "Reply with the word ready and nothing else."}}, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		text, last, err := collect(t, seq)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(text), "ready") || !last.Done || last.Usage.OutputTokens == 0 {
			t.Errorf("text %q, last %+v", text, last)
		}
		t.Logf("answer %q in %s, %d tokens", text, last.Usage.TotalDuration, last.Usage.OutputTokens)
	})

	t.Run("tool call and result", func(t *testing.T) {
		tools := []ToolSpec{{Name: "get_time", Description: "Returns the current local time.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
		msgs := []Message{{Role: RoleUser, Content: "What time is it now? Call get_time to find out."}}
		seq, err := e.Stream(ctx, msgs, tools, opts)
		if err != nil {
			t.Fatal(err)
		}
		_, last, err := collect(t, seq)
		if err != nil {
			t.Fatal(err)
		}
		if len(last.ToolCalls) == 0 || last.ToolCalls[0].Name != "get_time" {
			t.Fatalf("tool calls = %+v, want get_time", last.ToolCalls)
		}
		msgs = append(msgs,
			Message{Role: RoleAssistant, ToolCalls: last.ToolCalls},
			Message{Role: RoleTool, ToolName: "get_time", Content: "10:42"})
		seq, err = e.Stream(ctx, msgs, tools, opts)
		if err != nil {
			t.Fatal(err)
		}
		text, _, err := collect(t, seq)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "10:42") {
			t.Errorf("answer after the result = %q, want the time in it", text)
		}
	})

	t.Run("picture", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for x := 0; x < 64; x++ {
			for y := 0; y < 64; y++ {
				img.Set(x, y, color.RGBA{R: 220, A: 255})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		c, err := e.Generate(ctx, []Message{{Role: RoleUser, Content: "What color fills this picture? Answer in one word.",
			Images: [][]byte{buf.Bytes()}}}, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(c.Text), "red") {
			t.Errorf("answer = %q, want red", c.Text)
		}
	})

	t.Run("cut off", func(t *testing.T) {
		seq, err := e.Stream(ctx, []Message{{Role: RoleUser, Content: "Count from one to two hundred in words."}}, nil, Options{MaxTokens: 16})
		if err != nil {
			t.Fatal(err)
		}
		_, last, err := collect(t, seq)
		if err != nil {
			t.Fatal(err)
		}
		if last.DoneReason != "length" {
			t.Errorf("done reason = %q, want length", last.DoneReason)
		}
	})
}
