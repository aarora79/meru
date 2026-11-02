//go:build remote

// This file checks OpenAIEngine and AnthropicEngine against real hosted
// endpoints, with the owner's keys. The build tag keeps it out of every
// other test run and out of CI; run it by hand:
//
//	MERU_TEST_OPENAI_URL=https://… MERU_TEST_OPENAI_MODEL=… \
//	MERU_TEST_ANTHROPIC_URL=https://… MERU_TEST_ANTHROPIC_MODEL=… \
//	go test -tags remote -v -run Remote ./internal/engine/
//
// The keys come from ~/.meru/secrets.toml, as remote_test_openai and
// remote_test_anthropic, never from the environment, so a CI runner can't
// supply one. The URLs and models come from the environment, so the code
// names no provider. A dialect whose key, URL or model is missing skips.
// The questions hold nothing personal.

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/secrets"
)

// remoteEngine returns the engine for one dialect from the owner's setup,
// or skips the test when part of it is missing.
func remoteEngine(t *testing.T, api, secret, urlEnv, modelEnv string, key bool) Engine {
	t.Helper()
	url, model := os.Getenv(urlEnv), os.Getenv(modelEnv)
	if url == "" || model == "" {
		t.Skipf("set %s and %s to run this test", urlEnv, modelEnv)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	sec, err := secrets.Load(secrets.Path(filepath.Join(home, ".meru")))
	if err != nil || !sec.Has(secret) {
		t.Skipf("add %s to ~/.meru/secrets.toml to run this test", secret)
	}
	k := "not-a-key"
	if key {
		k, _ = sec.Resolve("secret:" + secret)
	}
	cfg := EndpointConfig{Name: "remote-test", Reach: "remote", URL: url, Key: k, Model: model}
	var e Engine
	if api == "openai" {
		e, err = NewOpenAI(cfg, nil, nil)
	} else {
		e, err = NewAnthropic(cfg, nil, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// redSquare returns a small PNG filled with red.
func redSquare(t *testing.T) []byte {
	t.Helper()
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
	return buf.Bytes()
}

func TestRemoteOpenAI(t *testing.T) {
	remoteSuite(t, "openai", "remote_test_openai", "MERU_TEST_OPENAI_URL", "MERU_TEST_OPENAI_MODEL")
}

func TestRemoteAnthropic(t *testing.T) {
	remoteSuite(t, "anthropic", "remote_test_anthropic", "MERU_TEST_ANTHROPIC_URL", "MERU_TEST_ANTHROPIC_MODEL")
}

// remoteSuite runs the same checks on one dialect: a streamed answer, a
// tool call and its result, a picture, an answer cut at its cap, and a
// wrong key.
func remoteSuite(t *testing.T, api, secret, urlEnv, modelEnv string) {
	e := remoteEngine(t, api, secret, urlEnv, modelEnv, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	opts := Options{MaxTokens: 1024}

	t.Run("stream", func(t *testing.T) {
		seq, err := e.Stream(ctx, []Message{{Role: RoleSystem, Content: "Be brief."},
			{Role: RoleUser, Content: "Reply with the word ready and nothing else."}}, nil, opts)
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
			t.Fatalf("tool calls = %+v", last.ToolCalls)
		}
		msgs = append(msgs, Message{Role: RoleAssistant, ToolCalls: last.ToolCalls},
			Message{Role: RoleTool, ToolName: "get_time", Content: "10:42"})
		seq, err = e.Stream(ctx, msgs, tools, opts)
		if err != nil {
			t.Fatal(err)
		}
		if text, _, err := collect(t, seq); err != nil || !strings.Contains(text, "10:42") {
			t.Errorf("answer after the result = %q, %v", text, err)
		}
	})

	t.Run("picture", func(t *testing.T) {
		c, err := e.Generate(ctx, []Message{{Role: RoleUser, Content: "What color fills this picture? One word.",
			Images: [][]byte{redSquare(t)}}}, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(c.Text), "red") {
			t.Errorf("answer = %q", c.Text)
		}
	})

	t.Run("cut off", func(t *testing.T) {
		seq, err := e.Stream(ctx, []Message{{Role: RoleUser, Content: "Count from one to two hundred in words."}}, nil, Options{MaxTokens: 16})
		if err != nil {
			t.Fatal(err)
		}
		if _, last, err := collect(t, seq); err != nil || last.DoneReason != "length" {
			t.Errorf("done reason = %q, %v; want length", last.DoneReason, err)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		bad := remoteEngine(t, api, secret, urlEnv, modelEnv, false)
		_, err := bad.Generate(ctx, []Message{{Role: RoleUser, Content: "hi"}}, nil, opts)
		var ee *EndpointError
		if !errors.As(err, &ee) || (ee.StatusCode != 401 && ee.StatusCode != 403) {
			t.Fatalf("err = %v, want a 401 or 403", err)
		}
		if !strings.Contains(err.Error(), "check the api_key entry") || strings.Contains(err.Error(), "not-a-key") {
			t.Errorf("err = %v", err)
		}
	})
}
