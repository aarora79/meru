// This file tests turns with a scope the user picked: which route and
// tools each scope gives, that the router and the widening rules stay out
// of it, and the "memories" event that lists what a turn recalled.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
)

func TestScopedTurns(t *testing.T) {
	// A mail server, a notes server, the web tools, the file tools and a
	// command: everything a scope might pick from.
	all := []engine.ToolSpec{
		spec("google.search_gmail_messages"), spec("google.get_events"), spec("google.search_drive_files"),
		spec("obsidian.obsidian_simple_search"), spec("web_search"), spec("web_fetch"),
		spec("read_file"), spec("grep"), spec("datetime"), spec("remember"), spec("cmd.git-log"),
	}
	tests := []struct {
		scope      string
		route      string
		want       []string // tools offered, in order
		wantSearch bool     // the turn searched the files first
	}{
		{rpc.ScopeFiles, "search", []string{"read_file", "grep", "datetime", "cmd.git-log"}, true},
		{rpc.ScopeMail, "tools", []string{"google.search_gmail_messages", "google.get_events", "google.search_drive_files", "datetime"}, false},
		{rpc.ScopeWeb, "tools", []string{"web_search", "web_fetch", "datetime"}, false},
		{rpc.ScopeTalk, "direct", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"ok"}}
			// The router would say "tools": a scoped turn must not ask it.
			router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
			search := &fakeSearcher{results: []retrieve.Result{result("/n/garden.md", "Garden", "Sow in April.", 1, 3, 0.03)}}
			tools := &fakeTools{specs: slices.Clone(all)}
			a := New(testConfig(t), eng, router, search, tools, nil, nil, quietLog())
			// "search my obsidian vault" names a server; in auto it would
			// widen the route. A scope ignores that.
			evs, err := run(context.Background(), a, rpc.Request{Text: "search my obsidian vault for the garden plan", Scope: tt.scope})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var route rpc.Event
			searched := false
			for _, ev := range evs {
				switch ev.Type {
				case rpc.EventRoute:
					route = ev
				case rpc.EventSources:
					searched = true
				}
			}
			if route.Route != tt.route || route.Confidence != 1 || route.Fallback {
				t.Errorf("route = %+v, want %s", route, tt.route)
			}
			if router.calls != 0 {
				t.Error("the router ran on a scoped turn")
			}
			if searched != tt.wantSearch {
				t.Errorf("searched first = %v, want %v", searched, tt.wantSearch)
			}
			var got []string
			for _, s := range eng.lastCall().tools {
				got = append(got, s.Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("tools = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopeOf(t *testing.T) {
	for _, s := range []string{"", "auto", "files", "mail", "web", "talk"} {
		if _, err := scopeOf(s); err != nil {
			t.Errorf("scopeOf(%q) = %v", s, err)
		}
	}
	if got, _ := scopeOf(""); got != rpc.ScopeAuto {
		t.Errorf(`scopeOf("") = %q, want auto`, got)
	}
	if _, err := scopeOf("everything"); err == nil {
		t.Error("scopeOf accepted an unknown scope")
	}
}

func TestMailServers(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  []string
	}{
		{"gmail and calendar", []string{"google.search_gmail_messages", "cal.list_calendars"}, []string{"google", "cal"}},
		{"an event tool", []string{"work.get_events"}, []string{"work"}},
		{"an agent", []string{"a2a.post.send_email"}, []string{"post"}},
		{"notes are not mail", []string{"obsidian.obsidian_simple_search", "web_search", "cmd.mail-sync"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var specs []engine.ToolSpec
			for _, n := range tt.specs {
				specs = append(specs, spec(n))
			}
			if got := mailServers(specs); !slices.Equal(got, tt.want) {
				t.Errorf("mailServers = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMemoriesEvent checks that a turn tells the client which memories
// recall put in its prompt, before the answer, and sends no event when it
// recalled none.
func TestMemoriesEvent(t *testing.T) {
	sam := recalled("people", "Sam is the user's manager")
	for _, tt := range []struct {
		name string
		mems []retrieve.Memory
		want int
	}{
		{"one recalled", []retrieve.Memory{sam}, 1},
		{"none", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"ok"}}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}},
				nil, nil, nil, &fakeRecall{recalled: tt.mems}, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: "should I tell Sam?"})
			if err != nil {
				t.Fatal(err)
			}
			var got []rpc.MemoryInfo
			seenToken := false
			for _, ev := range evs {
				switch ev.Type {
				case rpc.EventToken:
					seenToken = true
				case rpc.EventMemories:
					if seenToken {
						t.Error("the memories event came after the answer started")
					}
					got = ev.Memories
				}
			}
			if len(got) != tt.want {
				t.Fatalf("memories = %+v, want %d", got, tt.want)
			}
			if tt.want > 0 && (got[0].ID != "people/x.md" || got[0].Kind != "people" || !strings.Contains(got[0].Text, "Sam")) {
				t.Errorf("memory = %+v", got[0])
			}
		})
	}
}
