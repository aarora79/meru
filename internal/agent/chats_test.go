// This file tests what the model learns about its past chats: the note on
// where they are, the "From earlier conversations" header, and the rule
// that moves a direct question about past chats to the search route, end
// to end with the real file tools reading a made-up chat.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestAboutPastChats(t *testing.T) {
	tests := []struct {
		question string
		want     bool
	}{
		{"check my previous conversations with you, what were they all about", true},
		{"what did we talk about in our last chat?", true},
		{"list my past sessions", true},
		{"what have we discussed this month?", true},
		{"find me chats that are tagged garden", true},
		{"which conversations are in my recipes folder?", true},
		{"what is a chat protocol?", false},
		{"what was the last film to win the prize?", false},
		{"what did we decide about the garden beds?", false},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			if got := aboutPastChats(tt.question); got != tt.want {
				t.Errorf("aboutPastChats = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEarlierHeader(t *testing.T) {
	tests := []struct {
		name    string
		chats   string
		want    []string
		notWant []string
	}{
		{
			name:    "the file tools read the chats",
			chats:   "~/.meru/sessions",
			want:    []string{"your own past chats", "only the few that best match", "Every chat is in ~/.meru/sessions", "grep, list_folder and read_file", "Never tell the user you have no access to past chats"},
			notWant: []string{"you see only these"},
		},
		{
			name:    "the file tools are off",
			chats:   "",
			want:    []string{"your own past chats", "only the few that best match", "you see only these", "Never tell the user you have no access to past chats"},
			notWant: []string{"read_file"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := earlierHeader(tt.chats)
			// The section's first line names it, and the line after it is
			// the only other one: the recall tests count on that.
			lines := strings.Split(h, "\n")
			if len(lines) != 2 || lines[0] != "From earlier conversations:" {
				t.Fatalf("header = %q, want the title line and one more", h)
			}
			for _, w := range tt.want {
				if !strings.Contains(h, w) {
					t.Errorf("header lacks %q:\n%s", w, h)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(h, w) {
					t.Errorf("header holds %q:\n%s", w, h)
				}
			}
		})
	}
}

// TestPastChatsTurn runs the question from the real turn that started this
// work, which the router sends direct. The turn must move to the search
// route, tell the model where the chats are, offer grep, and let grep find
// a line in a past chat through dispatch.
func TestPastChatsTurn(t *testing.T) {
	cfg := testConfig(t)
	folder := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Index.Folders = []string{folder}
	sessions := filepath.Join(cfg.Dir, "sessions")

	// A past chat about the garden, made up for the test.
	past, err := transcript.New(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []transcript.Line{
		{Type: transcript.TypeUser, Text: "how deep should a raised bed be?"},
		{Type: transcript.TypeAssistant, Text: "About 30 cm suits most vegetables."},
	} {
		if err := past.Append(l); err != nil {
			t.Fatal(err)
		}
	}

	ix, err := index.New(cfg.Index, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", ix, nil, nil)
	tools.ReadSessions(sessions)
	disp := dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{})

	args, _ := json.Marshal(map[string]string{"pattern": "raised bed", "path": sessions})
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{{Name: builtin.Grep, Arguments: args}}},
		{pieces: []string{"We talked about how deep a raised bed should be."}},
	}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, disp, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "check my previous conversations with you, what were they all about"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var route string
	var result *rpc.ToolEvent
	for _, ev := range evs {
		switch ev.Type {
		case rpc.EventRoute:
			route = ev.Route
		case rpc.EventToolResult:
			result = ev.Tool
		}
	}
	if route != "search" {
		t.Errorf("route = %q, want search", route)
	}
	if result == nil || result.Name != builtin.Grep || result.Outcome != dispatch.OutcomeOK {
		t.Fatalf("tool_result = %+v, want grep, ok", result)
	}

	first := eng.calls[0]
	if !slices.ContainsFunc(first.tools, func(s engine.ToolSpec) bool { return s.Name == builtin.Grep }) {
		t.Errorf("the first round didn't offer grep")
	}
	if want := "Your past chats with the user are in " + sessions + ":"; !strings.Contains(first.msgs[0].Content, want) {
		t.Errorf("system prompt lacks %q:\n%s", want, first.msgs[0].Content)
	}
	msgs := eng.lastCall().msgs
	if last := msgs[len(msgs)-1]; last.Role != engine.RoleTool || !strings.Contains(last.Content, "how deep should a raised bed be?") {
		t.Errorf("last message = %+v, want grep's result with the past chat's line", last)
	}
}

// TestNoChatsNoteWithoutFileTools checks a setup whose file tools are off
// gets no note on the chats, and that a question about past chats then
// stays on the route the router picked when nothing could search.
func TestNoChatsNoteWithoutFileTools(t *testing.T) {
	cfg := testConfig(t)
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	disp := dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{})
	eng := &fakeEngine{pieces: []string{"I see no past chats here."}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, disp, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "what did we talk about in our last chat?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == rpc.EventRoute && ev.Route != "direct" {
			t.Errorf("route = %q, want direct", ev.Route)
		}
	}
	if system := eng.lastCall().msgs[0].Content; strings.Contains(system, "Your past chats") {
		t.Errorf("system prompt names the chats with the file tools off:\n%s", system)
	}
}
