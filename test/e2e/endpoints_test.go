//go:build e2e

// This file tests an answer model in [[endpoints]]: real merud and meru
// binaries, with [models] main naming "@v1", an OpenAI-style endpoint on
// the fake Ollama's own /v1. The router and search stay on the fake's
// Ollama API; the answer, with its tool rounds, goes to the endpoint.

package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// servedModel is the model name the endpoint's server knows.
const servedModel = "served-model"

// endpointConfig is fakeConfig with main = "@v1" and the [[endpoints]]
// entry for the fake's /v1.
func endpointConfig(url string) string {
	cfg := fakeConfig(url, "[builtin]\ntools = [\"datetime\"]\nconfirm = []\n\n"+
		"[[endpoints]]\nname = \"v1\"\nreach = \"local\"\napi = \"openai\"\n"+
		"url = \""+url+"/v1\"\nmodel = \""+servedModel+"\"\ncontext_length = 32768\n")
	return strings.Replace(cfg, "main = \""+mainModel+"\"", "main = \"@v1\"", 1)
}

// v1Requests returns the bodies of the requests the fake's /v1 got.
func v1Requests(t *testing.T, f *fake) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range f.requests(t) {
		if r.Path != "/v1/chat/completions" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		out = append(out, body)
	}
	return out
}

func TestAnswerFromAnEndpoint(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, endpointConfig(f.url))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	// The warm-up sent the endpoint nothing: its prompt holds the profile.
	if got := v1Requests(t, f); len(got) != 0 {
		t.Fatalf("the endpoint got %d requests before any question", len(got))
	}

	const answer = "Paris is the capital of France."
	f.enqueue(t, fastModel, directRoute())
	f.enqueue(t, servedModel, fakeollama.Reply{Text: answer})
	res := runMeru(t, h, "What is the capital of France?")
	if res.code != 0 || res.stdout != answer+"\n" {
		t.Fatalf("meru exited %d with %q, stderr:\n%s\nlog:\n%s", res.code, res.stdout, res.stderr, h.log())
	}
	reqs := v1Requests(t, f)
	if len(reqs) != 1 || reqs[0]["model"] != servedModel {
		t.Fatalf("the endpoint got %v", reqs)
	}
	msgs, _ := reqs[0]["messages"].([]any)
	if len(msgs) < 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages = %v, want the system prompt first", msgs)
	}
	for _, r := range f.requests(t) {
		if r.Path == "/api/chat" && r.Model != fastModel {
			t.Errorf("the fake's Ollama API answered for %q; only the fast model belongs there", r.Model)
		}
	}
	files := sessionFiles(t, h)
	lines := readTranscript(t, files[0])
	if len(lines) < 2 || lines[1].Type != transcript.TypeModelSwitch || lines[1].To != "@v1" {
		t.Errorf("transcript = %+v, want a model_switch to @v1", lines)
	}
}

func TestEndpointToolRound(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, endpointConfig(f.url))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	f.enqueue(t, fastModel, toolsRoute())
	f.enqueue(t, servedModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{ID: "c1", Name: "datetime", Arguments: map[string]any{"zone": "Europe/Oslo"}}}},
		fakeollama.Reply{Text: "It is morning in Oslo."})
	events := ask(t, h.socket, "", "what time is it in Oslo?")
	if got := answerOf(events); got != "It is morning in Oslo." {
		t.Fatalf("answer = %q\nlog:\n%s", got, h.log())
	}
	reqs := v1Requests(t, f)
	if len(reqs) != 2 {
		t.Fatalf("the endpoint got %d requests, want 2", len(reqs))
	}
	// The second request carries the call and its result, tied by ID.
	var sawCall, sawResult bool
	for _, raw := range reqs[1]["messages"].([]any) {
		msg := raw.(map[string]any)
		if calls, ok := msg["tool_calls"].([]any); ok && len(calls) == 1 && calls[0].(map[string]any)["id"] == "c1" {
			sawCall = true
		}
		if msg["role"] == "tool" && msg["tool_call_id"] == "c1" {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Errorf("round 2's messages lack the call or its result: %v", reqs[1]["messages"])
	}
	// The tool ran on this machine, through dispatch, as any call does.
	if out := runMeru(t, h, "log"); !strings.Contains(out.stdout, "datetime") {
		t.Errorf("meru log doesn't list the datetime call:\n%s", out.stdout)
	}
}
