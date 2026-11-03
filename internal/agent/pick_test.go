// This file tests which model answers a turn: the fast model for an easy
// question when routing is on, the main model otherwise, the main model
// again when the fast answer ends early, and the large model on request.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// pickAgent returns an agent with answers set, distinct fast, main and
// large models, and a router that returns dec.
func pickAgent(t *testing.T, answers string, dec Decision, eng *fakeEngine) (*Agent, config.Config) {
	t.Helper()
	cfg := testConfig(t)
	cfg.Router.Answers = answers
	cfg.Models.Fast, cfg.Models.Main, cfg.Models.Large = "fast-model", "main-model", "large-model"
	return New(cfg, eng, &fakeRouter{dec: dec}, nil, nil, nil, nil, quietLog()), cfg
}

// modelsCalled returns the model of each Stream call the engine got.
func modelsCalled(eng *fakeEngine) []string {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	var out []string
	for _, c := range eng.calls {
		out = append(out, c.opts.Model)
	}
	return out
}

func TestPickFast(t *testing.T) {
	sure := Decision{Route: "direct", Confidence: 0.92, Outcome: "ok"}
	tests := []struct {
		name    string
		answers string
		dec     Decision
		req     rpc.Request
		want    string
	}{
		{"routed, an easy question", config.AnswersRouted, sure, rpc.Request{Text: "what is 2 + 2?"}, "fast-model"},
		{"routing off", config.AnswersMain, sure, rpc.Request{Text: "what is 2 + 2?"}, "main-model"},
		{"not sure enough", config.AnswersRouted, Decision{Route: "direct", Confidence: 0.6, Outcome: "ok"}, rpc.Request{Text: "q"}, "main-model"},
		{"the fallback route", config.AnswersRouted, Decision{Route: "direct", Confidence: 0.9, Outcome: "low_confidence"}, rpc.Request{Text: "q"}, "main-model"},
		{"a search route", config.AnswersRouted, Decision{Route: "search", Confidence: 0.95, Outcome: "ok"}, rpc.Request{Text: "q"}, "main-model"},
		{"the large model asked for", config.AnswersRouted, sure, rpc.Request{Text: "q", Large: true}, "large-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"Four."}}
			a, _ := pickAgent(t, tt.answers, tt.dec, eng)
			evs, err := run(context.Background(), a, tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if got := modelsCalled(eng); !slices.Equal(got, []string{tt.want}) {
				t.Errorf("models called = %v, want [%s]", got, tt.want)
			}
			if done := evs[len(evs)-1]; done.Type != rpc.EventDone || done.Model != tt.want {
				t.Errorf("done = %+v, want model %s", done, tt.want)
			}
		})
	}
}

func TestEscalation(t *testing.T) {
	// The fast model's answer is cut off; the main model answers in full.
	eng := &fakeEngine{rounds: []fakeRound{
		{pieces: []string{"Paris is the cap"}, done: "length"},
		{pieces: []string{"Paris is the capital of France."}},
	}}
	a, cfg := pickAgent(t, config.AnswersRouted, Decision{Route: "direct", Confidence: 0.95, Outcome: "ok"}, eng)
	evs, err := run(context.Background(), a, rpc.Request{Text: "What is the capital of France?"})
	if err != nil {
		t.Fatal(err)
	}
	if got := modelsCalled(eng); !slices.Equal(got, []string{"fast-model", "main-model"}) {
		t.Fatalf("models called = %v, want fast then main", got)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventEscalate })
	if i < 0 || evs[i].Model != "main-model" || !strings.Contains(evs[i].Text, "cut off") {
		t.Fatalf("escalate event = %v", evs)
	}
	done := evs[len(evs)-1]
	if done.Model != "main-model" || done.Outcome != "" {
		t.Errorf("done = %+v, want a whole answer from main", done)
	}
	// The transcript keeps the main model's answer alone.
	lines := readLines(t, cfg, evs[0].Session)
	answer := lines[len(lines)-1]
	if answer.Type != transcript.TypeAssistant || answer.Text != "Paris is the capital of France." {
		t.Errorf("assistant line = %+v", answer)
	}
}

func TestNoSecondEscalation(t *testing.T) {
	// Both answers end early: the main one stands, cut off, and offers the
	// large model.
	eng := &fakeEngine{rounds: []fakeRound{
		{pieces: []string{"a"}, done: "length"},
		{pieces: []string{"b"}, done: "length"},
	}}
	a, _ := pickAgent(t, config.AnswersRouted, Decision{Route: "direct", Confidence: 0.95, Outcome: "ok"}, eng)
	evs, err := run(context.Background(), a, rpc.Request{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if got := modelsCalled(eng); len(got) != 2 {
		t.Errorf("models called = %v, want two calls", got)
	}
	done := evs[len(evs)-1]
	if done.Outcome != "cut_off" || !done.OfferLarge {
		t.Errorf("done = %+v, want cut off with the large model offered", done)
	}
}

func TestNoLargeModel(t *testing.T) {
	eng := &fakeEngine{pieces: []string{"x"}}
	a, _ := pickAgent(t, config.AnswersMain, Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}, eng)
	a.models.Large = ""
	if _, err := run(context.Background(), a, rpc.Request{Text: "q", Large: true}); err == nil || !strings.Contains(err.Error(), "[models] large") {
		t.Errorf("err = %v, want one that names [models] large", err)
	}
}
