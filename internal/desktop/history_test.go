// This file tests the history and status methods against an in-process
// rpc server, the day grouping, and which links the Bridge opens.

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// historyServer answers the session and status ops the way merud does.
func historyServer(t *testing.T, updated string) string {
	return startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		switch req.Op {
		case rpc.OpSessions:
			return emit(rpc.Event{Type: rpc.EventSessions, Sessions: []rpc.SessionInfo{
				{ID: "2026-09-20T090000-a1b2", Title: "Which hotel did I book in Lisbon?", Updated: updated, Turns: 2},
				{ID: "2026-09-01T090000-c3d4", Title: "Garden plan", Updated: "2026-09-01T09:00:00Z", Turns: 1},
			}})
		case rpc.OpSessionTurns:
			if req.Session != "2026-09-20T090000-a1b2" {
				return errors.New("no such session")
			}
			return emit(rpc.Event{Type: rpc.EventTurns, Turns: []rpc.TurnInfo{{
				Question: "Which hotel did I book in Lisbon?", Answer: "The Casa do Rio [1].", Route: "search+tools", Notice: "a note",
				Sources:        []string{"~/Notes/lisbon.md", "~/Notes/garden.md"},
				Tools:          []rpc.ToolStep{{Name: "google.search_gmail_messages", Kind: "mcp", Args: json.RawMessage(`{}`), Outcome: "ok", DurationMillis: 840}},
				DurationMillis: 5100, TokensOut: 14,
			}}})
		case rpc.OpIndexStatus:
			return emit(rpc.Event{Type: rpc.EventStatus, Status: &rpc.IndexStatus{Documents: 1284, Scanning: true}})
		case rpc.OpMCPStatus:
			return emit(rpc.Event{Type: rpc.EventMCPStatus, MCP: []rpc.MCPStatus{
				{Name: "google", State: rpc.MCPConnected}, {Name: "obsidian", State: rpc.MCPNotConnected},
				{Name: "notes", State: rpc.MCPNotConnected, Connector: rpc.ConnectorNeedsConfig},
				{Name: "files", State: rpc.MCPConnected, Connector: rpc.ConnectorOK},
			}})
		}
		return errors.New(`unknown op "` + string(req.Op) + `"`)
	})
}

func TestSessions(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	b, _ := newBridge(historyServer(t, now))
	got, err := b.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	want := []SessionView{
		{ID: "2026-09-20T090000-a1b2", Title: "Which hotel did I book in Lisbon?", Updated: now, Group: GroupToday},
		{ID: "2026-09-01T090000-c3d4", Title: "Garden plan", Updated: "2026-09-01T09:00:00Z", Group: GroupEarlier},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sessions =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSessionTurns(t *testing.T) {
	b, _ := newBridge(historyServer(t, ""))
	got, err := b.SessionTurns(context.Background(), "2026-09-20T090000-a1b2")
	if err != nil {
		t.Fatalf("SessionTurns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SessionTurns = %+v, want one turn", got)
	}
	v := got[0]
	if v.Answer != "The Casa do Rio [1]." || v.Notice != "a note" || v.DurationMillis != 5100 || v.TokensOut != 14 {
		t.Errorf("turn = %+v", v)
	}
	if !reflect.DeepEqual(v.Sources, []rpc.Citation{{N: 1, Path: "~/Notes/lisbon.md"}, {N: 2, Path: "~/Notes/garden.md"}}) {
		t.Errorf("sources = %+v, want lisbon.md as [1] and garden.md as [2]", v.Sources)
	}
	if !reflect.DeepEqual(v.Cited, []rpc.Citation{{N: 1, Path: "~/Notes/lisbon.md"}}) {
		t.Errorf("cited = %+v, want lisbon.md alone, the one the answer cites", v.Cited)
	}
	wantStep := Step{Name: "google.search_gmail_messages", Kind: "mcp", Server: "google", Label: "Searched mail", Outcome: "ok", DurationMillis: 840}
	if !reflect.DeepEqual(v.Steps, []Step{wantStep}) {
		t.Errorf("steps = %+v, want %+v", v.Steps, wantStep)
	}
	if !reflect.DeepEqual(v.Contacted, []string{"google"}) {
		t.Errorf("contacted = %v, want [google]", v.Contacted)
	}

	if _, err := b.SessionTurns(context.Background(), "2026-01-01T000000-ffff"); err == nil || !strings.Contains(err.Error(), "no such session") {
		t.Errorf("SessionTurns of an unknown session = %v, want merud's error", err)
	}
}

func TestGroupOf(t *testing.T) {
	loc := time.FixedZone("here", 2*3600)
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, loc)
	tests := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 25, 0, 0, 0, 0, loc), GroupToday},
		{time.Date(2026, 9, 24, 23, 59, 0, 0, loc), GroupYesterday},
		{time.Date(2026, 9, 24, 0, 0, 0, 0, loc), GroupYesterday},
		{time.Date(2026, 9, 23, 23, 59, 0, 0, loc), GroupEarlier},
		// 22:30 UTC on the 24th is 00:30 on the 25th here.
		{time.Date(2026, 9, 24, 22, 30, 0, 0, time.UTC), GroupToday},
		{time.Time{}, GroupEarlier},
	}
	for _, tt := range tests {
		if got := groupOf(tt.at, now); got != tt.want {
			t.Errorf("groupOf(%v) = %s, want %s", tt.at, got, tt.want)
		}
	}
}

func TestStatus(t *testing.T) {
	b, _ := newBridge(historyServer(t, ""))
	s := b.Status(context.Background())
	if !s.Up || s.Documents != 1284 || !s.Scanning || s.Model != "main-model" {
		t.Errorf("Status = %+v, want up with 1284 files, scanning, and the model", s)
	}
	if !reflect.DeepEqual(s.Connections, []string{"google", "notes (needs config)", "files"}) {
		t.Errorf("Connections = %v, want the connected server and each connector, with its state unless ok", s.Connections)
	}

	down, _ := newBridge(filepath.Join(t.TempDir(), "none.sock"))
	s = down.Status(context.Background())
	if s.Up || s.Problem == "" || s.Hint != StartHint {
		t.Errorf("Status with no merud = %+v, want down with the reason and the hint", s)
	}
	if s.Busy {
		t.Error("Status with no merud says busy; nothing listens, so it isn't running")
	}

	// A merud that takes the connection but doesn't answer in time is
	// busy, not stopped: no advice to start it. The short deadline stands
	// in for the five seconds a real check waits.
	slow, _ := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s = slow.Status(ctx)
	if s.Up || !s.Busy || s.Hint != "" || s.Problem == "" {
		t.Errorf("Status with a slow merud = %+v, want busy, a reason and no start hint", s)
	}
	if s.Connections == nil {
		t.Error("Connections is nil; the page wants an empty list")
	}
}

func TestMachineName(t *testing.T) {
	if got := machineName("darwin"); got != "this Mac" {
		t.Errorf("machineName(darwin) = %q", got)
	}
	if got := machineName("linux"); got != "this computer" {
		t.Errorf("machineName(linux) = %q", got)
	}
}

// TestOpenURL checks which links the Bridge hands to the opener: only
// http, https and file.
func TestOpenURL(t *testing.T) {
	tests := []struct {
		url    string
		opened bool
	}{
		{"https://example.com/trams", true},
		{"http://example.com/", true},
		{"file:///Users/dana/Notes/lisbon.md", true},
		{"javascript:alert(1)", false},
		{"wails://wails/index.html", false},
		{"data:text/html,<b>x</b>", false},
		{"mailto:dana@example.com", false},
		{"-https://example.com/", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			var got []string
			b := New(Options{Open: func(u string) error { got = append(got, u); return nil }})
			err := b.OpenURL(tt.url)
			if (err == nil) != tt.opened || (len(got) == 1) != tt.opened {
				t.Errorf("OpenURL(%q) = %v, opened %v; want opened %v", tt.url, err, got, tt.opened)
			}
		})
	}
}

func TestOpenSource(t *testing.T) {
	var got []string
	b := New(Options{Home: "/Users/dana", Open: func(u string) error { got = append(got, u); return nil }})
	if err := b.OpenSource(filepath.FromSlash("~/Notes/Lisbon trip.md")); err != nil {
		t.Fatalf("OpenSource: %v", err)
	}
	if want := []string{"file:///Users/dana/Notes/Lisbon%20trip.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("opened %v, want %v", got, want)
	}

	noHome := New(Options{Open: func(string) error { t.Error("opened with no home"); return nil }})
	if err := noHome.OpenSource(filepath.FromSlash("~/Notes/a.md")); err == nil {
		t.Error("OpenSource with no home = nil, want an error")
	}
}
