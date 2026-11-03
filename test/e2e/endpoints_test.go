//go:build e2e

// This file tests an answer model in [[endpoints]]: real merud and meru
// binaries, with [models] main naming "@v1", an OpenAI-style endpoint on
// the fake Ollama's own /v1. The router and search stay on the fake's
// Ollama API; the answer, with its tool rounds, goes to the endpoint.

package e2e

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// servedModel is the model name the endpoint's server knows.
const servedModel = "served-model"

// endpointConfig is fakeConfig with main = "@v1" and the [[endpoints]]
// entry for the fake's /v1.
func endpointConfig(url string) string {
	cfg := fakeConfig(url, "[builtin]\ntools = [\"datetime\"]\nconfirm = []\n\n"+
		"[[endpoints]]\nname = \"v1\"\nreach = \"local\"\n"+
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

// TestEndpointConsent runs a near-edge endpoint through real binaries. The
// fake's /v1 listens on loopback, which a near-edge endpoint may reach, so
// the turn asks first: "this time" sends it to the endpoint, a no answers
// with the fast model on this computer, and meru with no terminal can't ask,
// so it answers here too.
func TestEndpointConsent(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, strings.Replace(endpointConfig(f.url), `reach = "local"`, `reach = "near-edge"`, 1))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	// askWith asks question and answers merud's question with choice.
	askWith := func(question string, choice rpc.Choice) ([]rpc.Event, []rpc.Approval) {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		var asked []rpc.Approval
		approve := func(_ context.Context, a rpc.Approval) (rpc.Choice, error) {
			asked = append(asked, a)
			return choice, nil
		}
		var events []rpc.Event
		for ev, err := range rpc.Do(ctx, h.socket, rpc.Request{Op: rpc.OpAsk, Text: question, Source: rpc.SourceTUI}, approve) {
			if err != nil {
				t.Fatalf("ask %q: %v", question, err)
			}
			events = append(events, ev)
		}
		return events, asked
	}

	f.enqueue(t, fastModel, directRoute())
	f.enqueue(t, servedModel, fakeollama.Reply{Text: "From the lab."})
	events, asked := askWith("What is the capital of France?", rpc.ChoiceOnce)
	if len(asked) != 1 || asked[0].Consent == nil || asked[0].Consent.Reach != "near-edge" {
		t.Fatalf("asked = %+v, want one consent question", asked)
	}
	done := lastEvent(t, events)
	if answerOf(events) != "From the lab." || done.AnsweredBy != "@v1" || done.Reach != "near-edge" {
		t.Errorf("answer %q, done %+v", answerOf(events), done)
	}

	// No: the fast model answers, and the endpoint gets nothing more.
	before := len(v1Requests(t, f))
	f.enqueue(t, fastModel, directRoute(), fakeollama.Reply{Text: "From here."})
	events, _ = askWith("And of Spain?", rpc.ChoiceDeny)
	if got := answerOf(events); got != "From here." {
		t.Errorf("answer after no = %q\nlog:\n%s", got, h.log())
	}
	notices := eventsOf(events, rpc.EventNotice)
	if len(notices) != 1 || !strings.Contains(notices[0].Text, "Answered on this computer by "+fastModel) {
		t.Errorf("notices = %+v", notices)
	}
	if after := len(v1Requests(t, f)); after != before {
		t.Errorf("the endpoint got %d more requests after a no", after-before)
	}

	// meru with no terminal can't ask, so the turn stays here.
	f.enqueue(t, fastModel, directRoute(), fakeollama.Reply{Text: "Still here."})
	res := runMeru(t, h, "And of Italy?")
	if res.code != 0 || res.stdout != "Still here.\n" || !strings.Contains(res.stderr, "Answered on this computer") {
		t.Errorf("meru exited %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
	if after := len(v1Requests(t, f)); after != before {
		t.Error("meru with no terminal sent the turn to the endpoint")
	}

	// The transcript records each answer, never the question.
	var consents []string
	for _, file := range sessionFiles(t, h) {
		for _, l := range readTranscript(t, file) {
			if l.Type == transcript.TypeConsent {
				consents = append(consents, l.Choice)
				if l.Text != "" {
					t.Errorf("a consent line holds text: %q", l.Text)
				}
			}
		}
	}
	slices.Sort(consents)
	if !slices.Equal(consents, []string{"deny", "deny", "once"}) {
		t.Errorf("consent lines = %v, want once, deny, and meru's deny", consents)
	}
}

// TestRoutedAnswers runs [router] answers = "routed" through real binaries:
// an easy direct question gets the fast model, and a fast answer cut off at
// its length goes to the main model, with an escalate event between.
func TestRoutedAnswers(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, fakeConfig(f.url, "[router]\ntemperature = 1.0\nanswers = \"routed\"\n"))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	// directRoute is 0.9 sure, above easy_above's 0.8.
	f.enqueue(t, fastModel, directRoute(), fakeollama.Reply{Text: "Four."})
	events := ask(t, h.socket, "", "what is 2 + 2?")
	if done := lastEvent(t, events); answerOf(events) != "Four." || done.Model != fastModel {
		t.Errorf("answer %q from %q, want Four. from the fast model", answerOf(events), done.Model)
	}

	f.enqueue(t, fastModel, directRoute(), fakeollama.Reply{Text: "Paris is", DoneReason: "length"})
	f.enqueue(t, mainModel, fakeollama.Reply{Text: "Paris is the capital of France."})
	events = ask(t, h.socket, "", "What is the capital of France?")
	esc := eventsOf(events, rpc.EventEscalate)
	if len(esc) != 1 || esc[0].Model != mainModel {
		t.Fatalf("escalate events = %+v\nlog:\n%s", esc, h.log())
	}
	done := lastEvent(t, events)
	if done.Model != mainModel || done.Outcome != "" {
		t.Errorf("done = %+v, want a whole answer from main", done)
	}
	// The transcript holds the main model's answer, not the fast try.
	var answers []string
	for _, file := range sessionFiles(t, h) {
		for _, l := range readTranscript(t, file) {
			if l.Type == transcript.TypeAssistant {
				answers = append(answers, l.Text)
			}
		}
	}
	if !slices.Contains(answers, "Paris is the capital of France.") || slices.Contains(answers, "Paris is") {
		t.Errorf("assistant lines = %q", answers)
	}
}
