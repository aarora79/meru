// This file tests the session ops: turnsOf's grouping of transcript lines
// into turns, and the two handlers over a real transcript folder.

package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// lisbonLines is one session of two turns: the first searches mail and
// cites a note, the second failed before its answer.
func lisbonLines(home string) []transcript.Line {
	at := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	return []transcript.Line{
		{TS: at.Add(-time.Minute), Type: transcript.TypeSummary, Text: "a summary before any question is skipped"},
		{TS: at, Type: transcript.TypeUser, Text: "Which hotel did I book in Lisbon?"},
		{TS: at, Type: transcript.TypeToolCall, CallID: "c1", Kind: "mcp", Server: "google", Tool: "search_gmail_messages",
			Args: json.RawMessage(`{"query":"Lisbon hotel"}`)},
		{TS: at, Type: transcript.TypeApproval, CallID: "c1", Choice: "once"},
		{TS: at, Type: transcript.TypeToolResult, CallID: "c1", Outcome: "ok", OK: true, Ms: 840},
		{TS: at, Type: transcript.TypeToolCall, CallID: "c2", Kind: "builtin", Server: "meru", Tool: "read_file"},
		{TS: at, Type: transcript.TypeAssistant, Text: "The Casa do Rio [1].", Route: "search+tools", Ms: 5100,
			TokensIn: 1800, TokensOut: 14, Sources: []string{filepath.Join(home, "Notes", "lisbon.md")}},
		{TS: at.Add(time.Minute), Type: transcript.TypeUser, Text: "And the check-in time?"},
	}
}

func TestTurnsOf(t *testing.T) {
	home := filepath.FromSlash("/Users/dana")
	got := turnsOf(lisbonLines(home), home)
	want := []rpc.TurnInfo{
		{
			Time:     "2026-09-20T09:00:00Z",
			Question: "Which hotel did I book in Lisbon?",
			Answer:   "The Casa do Rio [1].",
			Route:    "search+tools",
			Sources:  []string{filepath.FromSlash("~/Notes/lisbon.md")},
			Tools: []rpc.ToolStep{
				{Name: "google.search_gmail_messages", Kind: "mcp", Args: json.RawMessage(`{"query":"Lisbon hotel"}`),
					Outcome: "ok", DurationMillis: 840},
				// A call with no result line keeps an empty outcome.
				{Name: "read_file", Kind: "builtin"},
			},
			DurationMillis: 5100, TokensIn: 1800, TokensOut: 14,
		},
		{Time: "2026-09-20T09:01:00Z", Question: "And the check-in time?"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("turnsOf =\n%+v\nwant\n%+v", got, want)
	}
}

// TestTurnsOfNotice checks that an answer's notice, the warning under an
// answer that claimed an action no tool performed, comes back with the
// past turn, so the desktop app shows it again.
func TestTurnsOfNotice(t *testing.T) {
	const notice = "Meru didn't run any tool for this answer, so nothing changed on your computer."
	at := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	got := turnsOf([]transcript.Line{
		{TS: at, Type: transcript.TypeUser, Text: "move the garden folder to ~/Projects"},
		{TS: at, Type: transcript.TypeAssistant, Text: "Done.", Route: "direct", Notice: notice},
	}, "")
	if len(got) != 1 || got[0].Notice != notice {
		t.Errorf("turnsOf = %+v, want the notice on the turn", got)
	}
}

// TestTurnsOfImages checks that a question's images come back with the
// past turn, as full paths, so the desktop app can show them again.
func TestTurnsOfImages(t *testing.T) {
	img := filepath.FromSlash("/Users/dana/meru-output/uploads/garden-bed.jpg")
	got := turnsOf([]transcript.Line{
		{Type: transcript.TypeUser, Text: "which plant is this?", Images: []string{img}},
		{Type: transcript.TypeAssistant, Text: "Basil.", Route: "direct"},
	}, filepath.FromSlash("/Users/dana"))
	if len(got) != 1 || len(got[0].Images) != 1 || got[0].Images[0] != img {
		t.Errorf("turnsOf = %+v, want the image's full path on the turn", got)
	}
}

func TestHistoryHandlers(t *testing.T) {
	dir := t.TempDir()
	home := filepath.FromSlash("/Users/dana")
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lisbonLines(home) {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	h := historyService{dir: dir, home: home}

	var events []rpc.Event
	emit := func(ev rpc.Event) error { events = append(events, ev); return nil }
	if err := h.handleSessions(0, emit); err != nil {
		t.Fatalf("handleSessions: %v", err)
	}
	if len(events) != 1 || len(events[0].Sessions) != 1 {
		t.Fatalf("sessions events = %+v, want one event with one session", events)
	}
	s := events[0].Sessions[0]
	if s.ID != sess.ID() || s.Title != "Which hotel did I book in Lisbon?" || s.Turns != 2 {
		t.Errorf("session = %+v, want the Lisbon session with 2 turns", s)
	}
	if _, err := time.Parse(time.RFC3339, s.Updated); err != nil {
		t.Errorf("Updated = %q, want RFC 3339: %v", s.Updated, err)
	}

	events = nil
	if err := h.handleTurns(sess.ID(), emit); err != nil {
		t.Fatalf("handleTurns: %v", err)
	}
	if len(events) != 1 || events[0].Type != rpc.EventTurns || len(events[0].Turns) != 2 {
		t.Fatalf("turns events = %+v, want one event with two turns", events)
	}

	// A bad or unknown ID fails, and never reads outside the folder.
	for _, id := range []string{"", "../../etc/passwd", "2026-01-01T000000-ffff"} {
		if err := h.handleTurns(id, emit); err == nil {
			t.Errorf("handleTurns(%q) = nil, want an error", id)
		}
	}
	if err := h.handleTurns("", emit); err == nil || !strings.Contains(err.Error(), "session ID") {
		t.Errorf("handleTurns(\"\") = %v, want it to ask for a session ID", err)
	}
}
