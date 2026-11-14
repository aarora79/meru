// Tests for the route rule that sends a short message of thanks or
// greeting direct.

package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

func TestEditDistance(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"great", "great", 0},
		{"thnaks", "thanks", 1}, // a swap is one edit
		{"greewat", "great", 2},
		{"grant", "great", 2},
		{"", "abc", 3},
	} {
		if got := editDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestThanksOnly(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		{"great job Meru! Yay!!!", true},     // the real turn
		{"you did a greewat job meru", true}, // the second real turn: a typo and "did"
		{"thnaks!", true},                    // two letters swapped
		{"awsome work", true},
		{"you are the best", false}, // "best" isn't on the list
		{"create a job", false},     // "create" isn't "great": another first letter
		{"grant it", false},         // one edit too many for a short word
		{"you did it", false},       // no word of thanks
		{"great job, now schedule the next one", false},
		{"thanks!", true},
		{"Thank you so much 🙏", true},
		{"perfect, that worked", true},
		{"hi Meru", true},
		{"good morning", true},
		{"thanks, now read the other one", false}, // a request follows
		{"thanks! can you check box 14 too?", false},
		{"great job?", false}, // a question mark
		{"yes", false},        // may answer Meru's own question
		{"ok", false},
		{"thanks for the W-2 summary", false}, // "for", "summary"
		{"", false},
		{"!!!", false},
		{"thank you thank you thank you thank you thank you so much", false}, // over the cap
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			if got := thanksOnly(tt.message); got != tt.want {
				t.Errorf("thanksOnly(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

// TestThanksGoesDirect checks a whole turn: the router falls back to
// search+tools, as it did for the real message, and the turn goes direct,
// searches nothing and offers no file tool.
func TestThanksGoesDirect(t *testing.T) {
	eng := &fakeEngine{pieces: []string{"Glad it helped!"}}
	router := &fakeRouter{dec: Decision{Route: "search+tools", Confidence: 0.391, Outcome: "low_confidence"}}
	search := &fakeSearcher{results: []retrieve.Result{result("/n/w2.md", "W-2", "Box 12.", 1, 3, 0.03)}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("read_file"), spec("datetime"), spec("about_meru")}}
	a := New(testConfig(t), eng, router, search, tools, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "great job Meru! Yay!!!"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventRoute })
	if i < 0 || evs[i].Route != "direct" {
		t.Fatalf("route = %+v, want direct; events %v", evs, types(evs))
	}
	if slices.ContainsFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventSources }) {
		t.Error("the turn searched the files")
	}
	for _, s := range eng.lastCall().tools {
		if s.Name == "read_file" {
			t.Error("the turn offered read_file")
		}
	}
}
