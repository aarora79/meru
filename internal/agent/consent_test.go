// This file tests the question a turn asks before it goes to an answer
// endpoint off this machine: what it asks, what each answer does, and the
// turns that never leave.

package agent

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// consentAgent returns an agent whose answer model is "@lab", an endpoint
// at reach, over a fake engine that records each call's model.
func consentAgent(t *testing.T, reach string) (*Agent, *fakeEngine, config.Config) {
	t.Helper()
	cfg := testConfig(t)
	cfg.Endpoints = []config.Endpoint{{Name: "lab", Reach: reach, API: config.APIOpenAI,
		URL: "http://10.0.0.5:8000/v1", Model: "big-model", ContextLength: 32768}}
	cfg.Models.Main = "@lab"
	eng := &fakeEngine{pieces: []string{"An answer."}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, nil, nil, quietLog())
	return a, eng, cfg
}

// asker answers every approval with choice and remembers what it was asked.
type asker struct {
	choice rpc.Choice
	mu     sync.Mutex
	asked  []rpc.Approval
}

func (k *asker) approve(_ context.Context, ap rpc.Approval) (rpc.Choice, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.asked = append(k.asked, ap)
	return k.choice, nil
}

// consentLines returns the session's consent lines.
func consentLines(t *testing.T, cfg config.Config, session string) []transcript.Line {
	t.Helper()
	var out []transcript.Line
	for _, l := range allLines(t, cfg, session) {
		if l.Type == transcript.TypeConsent {
			out = append(out, l)
		}
	}
	return out
}

func TestConsentAsks(t *testing.T) {
	a, eng, cfg := consentAgent(t, config.ReachNearEdge)
	k := &asker{choice: rpc.ChoiceOnce}
	evs, err := runApprove(context.Background(), a, rpc.Request{Text: "what is in my notes?"}, k.approve)
	if err != nil {
		t.Fatal(err)
	}
	if len(k.asked) != 1 {
		t.Fatalf("asked %d times, want once", len(k.asked))
	}
	ap := k.asked[0]
	if ap.Kind != rpc.KindConsent || ap.Name != "@lab" || ap.Consent == nil ||
		!slices.Equal(ap.Choices, []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}) {
		t.Fatalf("approval = %+v", ap)
	}
	c := ap.Consent
	if c.Endpoint != "lab" || c.Reach != "near-edge" || c.Model != "big-model" ||
		c.Carries[0] != "your question" || !slices.Contains(c.Carries, "the results of any tools this turn runs") {
		t.Errorf("consent = %+v", c)
	}
	if got := eng.lastCall().opts.Model; got != "@lab" {
		t.Errorf("the turn answered with %q, want @lab", got)
	}
	if done := evs[len(evs)-1]; done.Type != rpc.EventDone || done.AnsweredBy != "@lab" || done.Reach != "near-edge" {
		t.Errorf("done = %+v, want it to name @lab and near-edge", done)
	}
	session := evs[0].Session
	if lines := consentLines(t, cfg, session); len(lines) != 1 || lines[0].Choice != "once" || lines[0].Reach != "near-edge" || lines[0].Tool != "@lab" {
		t.Errorf("consent lines = %+v", lines)
	}
	// "This time" covers one turn: the next one asks again.
	if _, err := runApprove(context.Background(), a, rpc.Request{Session: session, Text: "and the next?"}, k.approve); err != nil {
		t.Fatal(err)
	}
	if len(k.asked) != 2 {
		t.Errorf("asked %d times after two turns with \"this time\", want 2", len(k.asked))
	}
}

func TestConsentThisChat(t *testing.T) {
	a, _, _ := consentAgent(t, config.ReachRemote)
	k := &asker{choice: rpc.ChoiceSession}
	evs, err := runApprove(context.Background(), a, rpc.Request{Text: "first"}, k.approve)
	if err != nil {
		t.Fatal(err)
	}
	session := evs[0].Session
	if _, err := runApprove(context.Background(), a, rpc.Request{Session: session, Text: "second"}, k.approve); err != nil {
		t.Fatal(err)
	}
	if len(k.asked) != 1 {
		t.Errorf("asked %d times in one chat after \"this chat\", want once", len(k.asked))
	}
	// Another chat asks again.
	if _, err := runApprove(context.Background(), a, rpc.Request{Text: "another chat"}, k.approve); err != nil {
		t.Fatal(err)
	}
	if len(k.asked) != 2 {
		t.Errorf("a new chat asked %d times in all, want 2", len(k.asked))
	}
}

func TestConsentKeepsTheTurnHere(t *testing.T) {
	tests := []struct {
		name    string
		req     rpc.Request
		choice  rpc.Choice // "" for no approver
		ask     bool
		because string
	}{
		{"no", rpc.Request{Text: "q"}, rpc.ChoiceDeny, true, "you said no to sending it to @lab"},
		{"an answer it didn't offer", rpc.Request{Text: "q"}, rpc.ChoiceSiteAlways, true, "you said no"},
		{"incognito", rpc.Request{Text: "q", Incognito: true}, rpc.ChoiceOnce, false, "an incognito chat stays on this computer"},
		{"a job", rpc.Request{Text: "q", Job: "brief"}, rpc.ChoiceOnce, false, "a scheduled job runs on this computer"},
		{"a client that can't ask", rpc.Request{Text: "q"}, "", false, "this client can't ask"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, eng, cfg := consentAgent(t, config.ReachNearEdge)
			k := &asker{choice: tt.choice}
			var approve rpc.ApproveFunc
			if tt.choice != "" {
				approve = k.approve
			}
			evs, err := runApprove(context.Background(), a, tt.req, approve)
			if err != nil {
				t.Fatal(err)
			}
			if asked := len(k.asked) > 0; asked != tt.ask {
				t.Errorf("asked = %v, want %v", asked, tt.ask)
			}
			if done := evs[len(evs)-1]; done.AnsweredBy != "" || done.Reach != "" {
				t.Errorf("done = %+v; an answer written here names no endpoint", done)
			}
			if got := eng.lastCall().opts.Model; got != cfg.Models.Fast {
				t.Errorf("the turn answered with %q, want the fast model %q", got, cfg.Models.Fast)
			}
			i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
			if i < 0 || !strings.Contains(evs[i].Text, "Answered on this computer by "+cfg.Models.Fast) ||
				!strings.Contains(evs[i].Text, tt.because) {
				t.Errorf("notice = %v, want one that says why the turn stayed here", evs)
			}
		})
	}
}

func TestNoConsentOnThisMachine(t *testing.T) {
	a, eng, _ := consentAgent(t, config.ReachLocal)
	k := &asker{choice: rpc.ChoiceDeny}
	if _, err := runApprove(context.Background(), a, rpc.Request{Text: "q"}, k.approve); err != nil {
		t.Fatal(err)
	}
	if len(k.asked) != 0 || eng.lastCall().opts.Model != "@lab" {
		t.Errorf("a local endpoint asked %d times and answered with %q", len(k.asked), eng.lastCall().opts.Model)
	}
}

func TestCarries(t *testing.T) {
	got := carries(3, 1)
	want := []string{"your question", "3 earlier turns of this chat", "1 picture"}
	if !slices.Equal(got[:3], want) {
		t.Errorf("carries = %q, want it to start %q", got, want)
	}
	if got := carries(0, 0); got[1] != "your profile and the memories Meru recalls for it" {
		t.Errorf("carries with no history = %q", got)
	}
}
