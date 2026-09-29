// This file tests the profile section: its order, its cap, where it sits in
// the system prompt, and what a turn does when the profile is empty or
// can't be read. It also tests the rule that gives a "remember" question
// tools.

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

// fakeProfile hands back fixed memories and an error. Its Recall finds
// nothing; recall_test.go has a fake that recalls.
type fakeProfile struct {
	mems []memory.Memory
	err  error
}

func (f fakeProfile) Profile() ([]memory.Memory, error) { return f.mems, f.err }

func (f fakeProfile) Recall(context.Context, string) ([]retrieve.Memory, error) { return nil, nil }

// mem builds a memory of kind with text, created on day (a day of
// September 2026) and modified at minute past midnight that day.
func mem(kind, text string, day, minute int) memory.Memory {
	created := time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC)
	return memory.Memory{
		ID:       kind + "/" + strings.ReplaceAll(strings.ToLower(text), " ", "-") + ".md",
		Kind:     kind,
		Text:     text,
		Created:  created,
		Modified: created.Add(time.Duration(minute) * time.Minute),
	}
}

func TestFormatProfile(t *testing.T) {
	tests := []struct {
		name        string
		mems        []memory.Memory
		limit       int
		want        string
		wantDropped int
	}{
		{name: "empty", limit: 2000, want: ""},
		{name: "only blank text", mems: []memory.Memory{mem("me", "  \n ", 1, 0)}, limit: 2000, want: ""},
		{
			name: "me first, then preferences, oldest first in each",
			mems: []memory.Memory{
				mem("preferences", "Likes short answers", 2, 0),
				mem("me", "Lives in Washington DC", 3, 0),
				mem("preferences", "Prefers metric units", 1, 0),
				mem("me", "Works on the registry team", 2, 5),
				mem("me", "Name is Dana Reyes", 2, 1),
			},
			limit: 2000,
			want: "What you know about the user:\n" +
				"- Name is Dana Reyes\n" +
				"- Works on the registry team\n" +
				"- Lives in Washington DC\n" +
				"- Prefers metric units\n" +
				"- Likes short answers",
		},
		{
			name:  "line breaks become spaces",
			mems:  []memory.Memory{mem("me", "Name is\nDana   Reyes\n", 1, 0)},
			limit: 2000,
			want:  "What you know about the user:\n- Name is Dana Reyes",
		},
		{
			// The header is 29 characters and each line below adds 1 + 8,
			// so a limit of 50 holds two lines of three.
			name: "over the cap keeps the newest",
			mems: []memory.Memory{
				mem("me", "oldest", 1, 0),
				mem("me", "middle", 2, 0),
				mem("preferences", "newest", 3, 0),
			},
			limit:       50,
			want:        "What you know about the user:\n- middle\n- newest",
			wantDropped: 1,
		},
		{
			name: "a long memory that doesn't fit leaves room for a short one",
			mems: []memory.Memory{
				mem("me", "short", 1, 0),
				mem("me", strings.Repeat("x", 100), 2, 0),
			},
			limit:       50,
			want:        "What you know about the user:\n- short",
			wantDropped: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped := formatProfile(tt.mems, tt.limit)
			if got != tt.want || dropped != tt.wantDropped {
				t.Errorf("formatProfile = %q, %d\nwant %q, %d", got, dropped, tt.want, tt.wantDropped)
			}
			if n := len([]rune(got)); n > tt.limit {
				t.Errorf("section is %d characters, over the %d limit", n, tt.limit)
			}
		})
	}
}

// TestProfileInPrompt checks where the profile sits in the system prompt,
// and that an empty or unreadable profile leaves the header out without
// failing the turn.
func TestProfileInPrompt(t *testing.T) {
	name := mem("me", "Name is Dana Reyes", 1, 0)
	tests := []struct {
		name    string
		profile Profile
		want    string // text the system prompt must hold; "" for no profile
	}{
		{"no profile", nil, ""},
		{"empty profile", fakeProfile{}, ""},
		{"a profile", fakeProfile{mems: []memory.Memory{name}}, "What you know about the user:\n- Name is Dana Reyes"},
		{"unreadable folder", fakeProfile{err: errors.New("permission denied")}, ""},
		{"one bad file", fakeProfile{mems: []memory.Memory{name}, err: errors.New("me/x.md: too big")}, "- Name is Dana Reyes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			eng := &fakeEngine{pieces: []string{"ok"}}
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}},
				nil, nil, nil, tt.profile, quietLog())
			if _, err := run(context.Background(), a, rpc.Request{Text: "who am I?"}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			system := eng.lastCall().msgs[0].Content
			if tt.want == "" {
				want := DefaultSystemPrompt + "\n\n" + whoIsWho + "\n\n" + honestyRule + "\n\n" + today(time.Now()) + "\n\n" + filesNote(nil, false) + "\n\n" + canDoNote(nil, "")
				if withoutClock(system) != want {
					t.Errorf("system prompt = %q\nwant %q", system, want)
				}
				return
			}
			who := strings.Index(system, whoIsWho)
			prof := strings.Index(system, tt.want)
			files := strings.Index(system, filesNote(nil, false))
			if who < 0 || prof < 0 || files < 0 || !(who < prof && prof < files) {
				t.Errorf("system prompt = %q\nwant whoIsWho, then %q, then filesNote", system, tt.want)
			}
		})
	}
}

// TestRememberGetsTools covers the route rule for memory: a question that
// holds "remember" as a whole word gets tools when remember is on offer.
func TestRememberGetsTools(t *testing.T) {
	both := &fakeTools{specs: []engine.ToolSpec{spec("configure"), spec("remember")}}
	noRemember := &fakeTools{specs: []engine.ToolSpec{spec("configure")}}
	tests := []struct {
		name      string
		route     string
		question  string
		tools     *fakeTools
		wantRoute string
	}{
		{"direct gets tools", "direct", "Remember that my name is Dana", both, "tools"},
		{"search keeps its search", "search", "please remember I work on the registry", both, "search+tools"},
		{"tools stays tools", "tools", "remember this", both, "tools"},
		{"only the whole word", "direct", "I remembered the milk", both, "direct"},
		{"no remember tool", "direct", "remember that my name is Dana", noRemember, "direct"},
		{"no mention", "direct", "what is the capital of France", both, "direct"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"ok"}}
			evs, err := run(context.Background(), toolsAgent(t, tt.route, eng, tt.tools), rpc.Request{Text: tt.question})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventRoute && ev.Route != tt.wantRoute {
					t.Errorf("route = %q, want %q", ev.Route, tt.wantRoute)
				}
			}
			if got, want := len(eng.lastCall().tools) > 0, tt.wantRoute != "direct"; got != want {
				t.Errorf("tools offered = %v, want %v", got, want)
			}
		})
	}
}
