// This file tests the rule that keeps the last route for a follow-up the
// router is unsure of (unsure.go).

package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// TestLastRoute checks that lastRoute finds the newest answered turn's
// route and gives "" for a chat with no answer yet.
func TestLastRoute(t *testing.T) {
	tests := []struct {
		name  string
		lines []transcript.Line
		want  string
	}{
		{"new chat", nil, ""},
		{"question only", []transcript.Line{{Type: transcript.TypeUser, Text: "hi"}}, ""},
		{"newest answer wins", []transcript.Line{
			{Type: transcript.TypeUser}, {Type: transcript.TypeAssistant, Route: "search+tools"},
			{Type: transcript.TypeUser}, {Type: transcript.TypeAssistant, Route: "direct"},
			{Type: transcript.TypeUser},
		}, "direct"},
		{"answer without a route is skipped", []transcript.Line{
			{Type: transcript.TypeAssistant, Route: "tools"}, {Type: transcript.TypeAssistant},
		}, "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastRoute(tt.lines); got != tt.want {
				t.Errorf("lastRoute = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestKeepRoute checks that only an unsure decision in a chat with an
// earlier turn takes that turn's route.
func TestKeepRoute(t *testing.T) {
	tests := []struct {
		name string
		dec  Decision
		last string
		want string
	}{
		{"unsure after direct stays direct", Decision{Route: "search+tools", Outcome: "low_confidence"}, "direct", "direct"},
		{"unsure after tools stays tools", Decision{Route: "search+tools", Outcome: "low_confidence"}, "tools", "tools"},
		{"unsure in a new chat takes the fallback", Decision{Route: "search+tools", Outcome: "low_confidence"}, "", "search+tools"},
		{"a sure route stands", Decision{Route: "tools", Outcome: "ok"}, "direct", "tools"},
		{"a degraded router keeps its fallback", Decision{Route: "search+tools", Outcome: "degraded"}, "direct", "search+tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keepRoute(tt.dec, tt.last); got != tt.want {
				t.Errorf("keepRoute = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUnsureFollowUpKeepsRoute runs two turns of one chat: a direct turn,
// then a follow-up the router is unsure of. The follow-up stays direct and
// offers the direct turn's tools, so Ollama's cached prompt still fits.
func TestUnsureFollowUpKeepsRoute(t *testing.T) {
	eng := &fakeEngine{pieces: []string{"Yes."}}
	router := &seqRouter{decs: []Decision{
		{Route: "direct", Confidence: 0.9, Outcome: "ok"},
		{Route: "search+tools", Confidence: 0.33, Outcome: "low_confidence"},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("read_file"), spec("datetime"), spec("about_meru")}}
	a := New(testConfig(t), eng, router, nil, tools, nil, nil, quietLog())
	first, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "what is a haiku"})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Session: first[0].Session, Text: "and the one before that?"})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventRoute })
	if i < 0 || evs[i].Route != "direct" {
		t.Fatalf("route = %+v, want direct; events %v", evs, types(evs))
	}
	for _, s := range eng.lastCall().tools {
		if s.Name == "read_file" {
			t.Error("the follow-up offered read_file, a tool the direct turn didn't")
		}
	}
}
