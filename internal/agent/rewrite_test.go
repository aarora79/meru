// This file tests rewrite.go: which questions count as writing help on text
// the question gives.

package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestWritingOnText(t *testing.T) {
	tests := []struct {
		question string
		want     bool
	}{
		{"rewrite\nMeru is great and helps me with my tasks", true},
		{"rewrite \n\"Meru streamlines my workflow by handling my tasks efficiently.\"", true},
		{`proofread this: "I sent my manager the report"`, true},
		{"translate “where is the train station” into Portuguese", true},
		{"Please fix the grammar: me and my boss was late", true},
		{"can you shorten this:\nWe will meet on Friday at the cafe.", true},
		{"rewrite my resume from my documents", false},
		{"rewrite what's in my garden notes", false},
		{"what did I write about the gym contract: the price?", false},
		{"edits I made yesterday", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			if got := writingOnText(tt.question); got != tt.want {
				t.Errorf("writingOnText(%q) = %v, want %v", tt.question, got, tt.want)
			}
		})
	}
}

// TestGivenTextGoesDirect checks a whole turn: the router says search, as it
// did for the real question, and the route event says direct.
func TestGivenTextGoesDirect(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{pieces: []string{"Meru speeds up my work."}}
	router := &fakeRouter{dec: Decision{Route: "search", Confidence: 0.52, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, nil, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk,
		Text: "rewrite \n\"Meru streamlines my workflow by handling my tasks efficiently.\""})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventRoute })
	if i < 0 || evs[i].Route != "direct" {
		t.Errorf("route event = %+v, want direct; events %v", evs, types(evs))
	}
}
