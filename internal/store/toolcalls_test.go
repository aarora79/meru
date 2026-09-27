// This file tests the tool_calls audit log: writing and reading rows, the
// cap on result text, and rebuilding the table from session transcripts.

package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/transcript"
)

// at returns 2026-09-24 at the given hour, minute and second, in UTC.
func at(h, m, s int) time.Time {
	return time.Date(2026, 9, 24, h, m, s, 0, time.UTC)
}

func TestInsertAndReadToolCalls(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	rows := []ToolCall{
		{CallID: "c1", Session: "s1", Time: at(10, 0, 0), Kind: "mcp", Server: "web", Tool: "search",
			Args: json.RawMessage(`{"q":"go"}`), Result: "3 hits", Outcome: "ok", DurationMillis: 120, TraceID: "t1", Caller: "meru"},
		{CallID: "c2", Session: "s1", Time: at(10, 0, 5), Kind: "builtin", Server: "meru", Tool: "write_file",
			Outcome: "declined", Approval: "deny"},
		// Same second as c2: the later write comes back first.
		{CallID: "c3", Session: "s2", Time: at(10, 0, 5), Kind: "a2a", Server: "helper", Tool: "plan",
			Result: strings.Repeat("é", MaxToolResult+10), Outcome: "error"},
	}
	for _, r := range rows {
		if err := s.InsertToolCall(ctx, r); err != nil {
			t.Fatalf("InsertToolCall(%s): %v", r.CallID, err)
		}
	}

	tests := []struct {
		limit int
		want  []string
	}{
		{0, []string{"c3", "c2", "c1"}},
		{2, []string{"c3", "c2"}},
		{10, []string{"c3", "c2", "c1"}},
	}
	for _, tt := range tests {
		got, err := s.ToolCalls(ctx, tt.limit)
		if err != nil {
			t.Fatalf("ToolCalls(%d): %v", tt.limit, err)
		}
		var ids []string
		for _, r := range got {
			ids = append(ids, r.CallID)
		}
		if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
			t.Errorf("ToolCalls(%d) = %v, want %v", tt.limit, ids, tt.want)
		}
	}

	got, err := s.ToolCalls(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	c1 := got[2]
	if c1.Kind != "mcp" || c1.Server != "web" || c1.Tool != "search" || string(c1.Args) != `{"q":"go"}` ||
		c1.Result != "3 hits" || c1.Outcome != "ok" || c1.DurationMillis != 120 || c1.TraceID != "t1" ||
		!c1.Time.Equal(at(10, 0, 0)) || c1.ID == 0 || c1.Caller != "meru" {
		t.Errorf("row c1 = %+v", c1)
	}
	if got[1].Approval != "deny" || got[1].Args != nil || got[1].Caller != "" {
		t.Errorf("row c2 = %+v, want approval deny and no args", got[1])
	}
	if n := len([]rune(got[0].Result)); n != MaxToolResult {
		t.Errorf("row c3 result has %d characters, want %d", n, MaxToolResult)
	}
}

// writeSession writes lines to sessions/2026/09/<id>.jsonl under dir, one
// JSON object per line, and returns the file's path.
func writeSession(t *testing.T, dir, id string, lines []transcript.Line) string {
	t.Helper()
	path := filepath.Join(dir, "2026", "09", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplayToolCalls(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	// Session one: an ok call merud made itself, an approved call
	// interleaved with it, a call ID reused in a later turn, and a call
	// merud never finished.
	writeSession(t, dir, "2026-09-24T100000-aaaa", []transcript.Line{
		{TS: at(10, 0, 0), Type: transcript.TypeUser, Text: "hi"},
		{TS: at(10, 0, 1), Type: transcript.TypeToolCall, CallID: "a", Kind: "mcp", Server: "web", Tool: "search",
			Args: json.RawMessage(`{"q":"x"}`), TraceID: "t1", Caller: "meru"},
		{TS: at(10, 0, 1), Type: transcript.TypeToolCall, CallID: "b", Kind: "builtin", Server: "meru", Tool: "write_file", TraceID: "t1"},
		{TS: at(10, 0, 2), Type: transcript.TypeApproval, CallID: "b", Server: "meru", Tool: "write_file", Choice: "session", TraceID: "t1"},
		{TS: at(10, 0, 3), Type: transcript.TypeToolResult, CallID: "a", Outcome: "ok", OK: true, Ms: 40, Result: "found", TraceID: "t1"},
		{TS: at(10, 0, 4), Type: transcript.TypeToolResult, CallID: "b", Outcome: "ok", OK: true, Ms: 5, Result: "wrote", TraceID: "t1"},
		{TS: at(10, 1, 0), Type: transcript.TypeToolCall, CallID: "a", Kind: "mcp", Server: "web", Tool: "fetch", TraceID: "t2"},
		{TS: at(10, 1, 1), Type: transcript.TypeToolResult, CallID: "a", Outcome: "timeout", Ms: 60000, TraceID: "t2"},
		{TS: at(10, 2, 0), Type: transcript.TypeToolCall, CallID: "c", Kind: "mcp", Server: "web", Tool: "search", TraceID: "t3"},
	})
	// Session two: no tool lines at all.
	writeSession(t, dir, "2026-09-24T110000-bbbb", []transcript.Line{
		{TS: at(11, 0, 0), Type: transcript.TypeUser, Text: "hello"},
	})
	// A file that isn't a transcript is skipped.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := openTest(t)
	ctx := context.Background()
	n, err := s.ReplayToolCalls(ctx, dir)
	if err != nil {
		t.Fatalf("ReplayToolCalls: %v", err)
	}
	if n != 4 {
		t.Fatalf("ReplayToolCalls wrote %d rows, want 4", n)
	}

	got, err := s.ToolCalls(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ callID, tool, outcome, approval, result string }
	want := []row{
		{"c", "search", "cancelled", "", ""},
		{"a", "fetch", "timeout", "", ""},
		{"b", "write_file", "ok", "session", "wrote"},
		{"a", "search", "ok", "", "found"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := row{got[i].CallID, got[i].Tool, got[i].Outcome, got[i].Approval, got[i].Result}
		if g != w {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
		if got[i].Session != "2026-09-24T100000-aaaa" {
			t.Errorf("row %d session = %q", i, got[i].Session)
		}
	}
	if string(got[3].Args) != `{"q":"x"}` || got[3].DurationMillis != 40 || got[3].TraceID != "t1" || got[3].Caller != "meru" {
		t.Errorf("row a/search = %+v", got[3])
	}

	// A second replay finds rows already there and writes nothing.
	n, err = s.ReplayToolCalls(ctx, dir)
	if err != nil || n != 0 {
		t.Errorf("second ReplayToolCalls = %d, %v; want 0, nil", n, err)
	}
}

func TestReplayToolCallsNoSessions(t *testing.T) {
	s := openTest(t)
	n, err := s.ReplayToolCalls(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err != nil || n != 0 {
		t.Errorf("ReplayToolCalls on a missing folder = %d, %v; want 0, nil", n, err)
	}
}

// TestReplayMatchesLive checks that a row rebuilt from the transcript equals
// the row written live, apart from the row ID.
func TestReplayMatchesLive(t *testing.T) {
	ctx := context.Background()
	live := ToolCall{CallID: "x", Session: "2026-09-24T100000-cccc", Time: at(9, 30, 0), Kind: "mcp",
		Server: "web", Tool: "search", Args: json.RawMessage(`{"q":"y"}`), Result: "r", Outcome: "ok",
		Approval: "once", DurationMillis: 7, TraceID: "t9"}

	dir := filepath.Join(t.TempDir(), "sessions")
	writeSession(t, dir, live.Session, []transcript.Line{
		{TS: live.Time, Type: transcript.TypeToolCall, CallID: "x", Kind: "mcp", Server: "web", Tool: "search",
			Args: live.Args, TraceID: "t9"},
		{TS: live.Time, Type: transcript.TypeApproval, CallID: "x", Server: "web", Tool: "search", Choice: "once", TraceID: "t9"},
		{TS: live.Time, Type: transcript.TypeToolResult, CallID: "x", Outcome: "ok", OK: true, Ms: 7, Result: "r", TraceID: "t9"},
	})

	a := openTest(t)
	if err := a.InsertToolCall(ctx, live); err != nil {
		t.Fatal(err)
	}
	b := openTest(t)
	if _, err := b.ReplayToolCalls(ctx, dir); err != nil {
		t.Fatal(err)
	}
	ra, err := a.ToolCalls(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.ToolCalls(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra) != 1 || len(rb) != 1 {
		t.Fatalf("rows: live %d, replayed %d; want 1 each", len(ra), len(rb))
	}
	ra[0].ID, rb[0].ID = 0, 0
	ja, _ := json.Marshal(ra[0])
	jb, _ := json.Marshal(rb[0])
	if string(ja) != string(jb) {
		t.Errorf("replayed row differs from live row:\nlive   %s\nreplay %s", ja, jb)
	}
}
