// This file tests the turns table: the window starts, the sums over each
// window, rebuilding the table from session transcripts, and the size on
// disk. It ends with a benchmark of Usage over 50,000 rows.

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// est is a fixed zone five hours behind UTC, so the tests show that the
// windows follow now's location rather than UTC.
var est = time.FixedZone("EST", -5*60*60)

// local returns the given time in est.
func local(y int, m time.Month, d, h, min, s int) time.Time {
	return time.Date(y, m, d, h, min, s, 0, est)
}

func TestWindowStarts(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		// The expected starts of today, week and month. 1h, 30d and all
		// follow from now and are checked once, below the table.
		today, week, month time.Time
	}{
		{"mid-week", local(2026, 9, 23, 15, 30, 0),
			local(2026, 9, 23, 0, 0, 0), local(2026, 9, 21, 0, 0, 0), local(2026, 9, 1, 0, 0, 0)},
		{"Monday just after midnight", local(2026, 9, 21, 0, 30, 0),
			local(2026, 9, 21, 0, 0, 0), local(2026, 9, 21, 0, 0, 0), local(2026, 9, 1, 0, 0, 0)},
		{"Sunday night", local(2026, 9, 27, 23, 59, 59),
			local(2026, 9, 27, 0, 0, 0), local(2026, 9, 21, 0, 0, 0), local(2026, 9, 1, 0, 0, 0)},
		{"week starts in the month before", local(2026, 10, 1, 10, 0, 0),
			local(2026, 10, 1, 0, 0, 0), local(2026, 9, 28, 0, 0, 0), local(2026, 10, 1, 0, 0, 0)},
		{"week starts in the year before", local(2027, 1, 1, 9, 0, 0),
			local(2027, 1, 1, 0, 0, 0), local(2026, 12, 28, 0, 0, 0), local(2027, 1, 1, 0, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowStarts(tt.now)
			want := []time.Time{
				tt.now.Add(-time.Hour), tt.today, tt.week, tt.month,
				tt.now.Add(-30 * 24 * time.Hour), {},
			}
			if len(got) != len(want) {
				t.Fatalf("got %d starts, want %d", len(got), len(want))
			}
			for i := range want {
				if !got[i].Equal(want[i]) {
					t.Errorf("%s starts %v, want %v", windowNames()[i], got[i], want[i])
				}
			}
		})
	}
}

func TestUsage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// Thursday 24 September 2026, noon in est.
	now := local(2026, 9, 24, 12, 0, 0)

	rows := []Turn{
		// In the last hour.
		{Session: "s1", Time: local(2026, 9, 24, 11, 30, 0), TokensIn: 10, TokensOut: 5,
			DurationMillis: 1000, ToolCalls: 1, Docs: []string{"/a", "/b"}},
		// Earlier today.
		{Session: "s1", Time: local(2026, 9, 24, 8, 0, 0), TokensIn: 20, TokensOut: 10,
			DurationMillis: 2000, Docs: []string{"/b"}},
		// One second before today's midnight, and earlier this week.
		{Session: "s2", Time: local(2026, 9, 23, 23, 59, 59), TokensIn: 1, TokensOut: 1, DurationMillis: 100},
		{Session: "s2", Time: local(2026, 9, 22, 10, 0, 0), TokensIn: 30, TokensOut: 15,
			DurationMillis: 3000, ToolCalls: 2, Docs: []string{"/c"}},
		// One second before this week's Monday, and earlier this month.
		{Session: "s5", Time: local(2026, 9, 20, 23, 59, 59), TokensIn: 2, TokensOut: 2,
			DurationMillis: 200, Docs: []string{"/f"}},
		{Session: "s3", Time: local(2026, 9, 2, 9, 0, 0), TokensIn: 40, TokensOut: 20, DurationMillis: 4000},
		// Last month, within 30 days.
		{Session: "s3", Time: local(2026, 8, 30, 9, 0, 0), TokensIn: 50, TokensOut: 25,
			DurationMillis: 5000, ToolCalls: 1, Docs: []string{"/a", "/d"}},
		// Long ago.
		{Session: "s4", Time: local(2026, 1, 5, 9, 0, 0), TokensIn: 60, TokensOut: 30,
			DurationMillis: 6000, Docs: []string{"/e"}},
	}
	for _, r := range rows {
		if err := s.InsertTurn(ctx, r); err != nil {
			t.Fatalf("InsertTurn: %v", err)
		}
	}

	got, err := s.Usage(ctx, now)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	want := []rpc.UsageWindow{
		{Name: rpc.Usage1h, Since: "2026-09-24T11:00:00-05:00", Sessions: 1, Turns: 1,
			TokensIn: 10, TokensOut: 5, ActiveMillis: 1000, ToolCalls: 1, Docs: 2},
		{Name: rpc.UsageToday, Since: "2026-09-24T00:00:00-05:00", Sessions: 1, Turns: 2,
			TokensIn: 30, TokensOut: 15, ActiveMillis: 3000, ToolCalls: 1, Docs: 2},
		{Name: rpc.UsageWeek, Since: "2026-09-21T00:00:00-05:00", Sessions: 2, Turns: 4,
			TokensIn: 61, TokensOut: 31, ActiveMillis: 6100, ToolCalls: 3, Docs: 3},
		{Name: rpc.UsageMonth, Since: "2026-09-01T00:00:00-05:00", Sessions: 4, Turns: 6,
			TokensIn: 103, TokensOut: 53, ActiveMillis: 10300, ToolCalls: 3, Docs: 4},
		{Name: rpc.Usage30d, Since: "2026-08-25T12:00:00-05:00", Sessions: 4, Turns: 7,
			TokensIn: 153, TokensOut: 78, ActiveMillis: 15300, ToolCalls: 4, Docs: 5},
		{Name: rpc.UsageLifetime, Sessions: 5, Turns: 8,
			TokensIn: 213, TokensOut: 108, ActiveMillis: 21300, ToolCalls: 4, Docs: 6},
	}
	for i := range want {
		if i >= len(got) || !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("window %d = %+v\nwant %+v", i, at2(got, i), want[i])
		}
	}
}

// at2 returns ws[i], or the zero window when ws is too short, so a failed
// check prints instead of panicking.
func at2(ws []rpc.UsageWindow, i int) rpc.UsageWindow {
	if i < len(ws) {
		return ws[i]
	}
	return rpc.UsageWindow{}
}

func TestUsageEmpty(t *testing.T) {
	s := openTest(t)
	got, err := s.Usage(context.Background(), local(2026, 9, 24, 12, 0, 0))
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d windows, want 6", len(got))
	}
	for _, w := range got {
		if w.Turns != 0 || w.Sessions != 0 || w.TokensIn != 0 || w.Docs != 0 {
			t.Errorf("window %s = %+v, want zeros", w.Name, w)
		}
	}
}

// readTurns returns every row of turns, oldest first.
func readTurns(t *testing.T, s *Store) []Turn {
	t.Helper()
	rows, err := s.db.Query(`SELECT session, ts, route, tokens_in, tokens_out, duration_ms, tool_calls, docs, trace_id
		FROM turns ORDER BY ts, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var r Turn
		var ts, docs string
		if err := rows.Scan(&r.Session, &ts, &r.Route, &r.TokensIn, &r.TokensOut,
			&r.DurationMillis, &r.ToolCalls, &docs, &r.TraceID); err != nil {
			t.Fatal(err)
		}
		r.Time, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Fatal(err)
		}
		// docs is checked as text, which also shows "[]" for none.
		r.Docs = []string{docs}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReplayTurns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "sessions")

	// A missing sessions folder means no sessions yet.
	if n, err := s.ReplayTurns(ctx, dir); err != nil || n != 0 {
		t.Fatalf("ReplayTurns on a missing folder = %d, %v; want 0, nil", n, err)
	}

	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := []transcript.Line{
		// A v0.3 turn with every field.
		{TS: at(10, 0, 0), Type: transcript.TypeUser, Text: "q1", TraceID: "t1"},
		{TS: at(10, 0, 4), Type: transcript.TypeAssistant, Text: "a1", TokensIn: 100, TokensOut: 20,
			Route: "search", Ms: 4100, Sources: []string{"/n/a.md", "/n/b.md"}, TraceID: "t1"},
		// A turn that called two tools, written before the route field.
		{TS: at(10, 1, 0), Type: transcript.TypeUser, Text: "q2"},
		{TS: at(10, 1, 1), Type: transcript.TypeToolCall, CallID: "c1", Kind: "mcp", Server: "web", Tool: "search"},
		{TS: at(10, 1, 1), Type: transcript.TypeToolCall, CallID: "c2", Kind: "builtin", Server: "meru", Tool: "remember"},
		{TS: at(10, 1, 2), Type: transcript.TypeToolResult, CallID: "c1", Outcome: "ok", Ms: 900},
		{TS: at(10, 1, 3), Type: transcript.TypeToolResult, CallID: "c2", Outcome: "ok", Ms: 5},
		{TS: at(10, 1, 5), Type: transcript.TypeAssistant, Text: "a2", TokensIn: 300, TokensOut: 40},
		// A question with no answer, then one that got one.
		{TS: at(10, 2, 0), Type: transcript.TypeUser, Text: "q3"},
		{TS: at(10, 3, 0), Type: transcript.TypeUser, Text: "q4"},
		{TS: at(10, 3, 2), Type: transcript.TypeAssistant, Text: "a4", TokensIn: 50, TokensOut: 5, Route: "direct", Ms: 2000},
	}
	for _, l := range lines {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.ReplayTurns(ctx, dir)
	if err != nil || n != 3 {
		t.Fatalf("ReplayTurns = %d, %v; want 3, nil", n, err)
	}
	want := []Turn{
		{Session: sess.ID(), Time: at(10, 0, 0), Route: "search", TokensIn: 100, TokensOut: 20,
			DurationMillis: 4100, Docs: []string{`["/n/a.md","/n/b.md"]`}, TraceID: "t1"},
		{Session: sess.ID(), Time: at(10, 1, 0), TokensIn: 300, TokensOut: 40, ToolCalls: 2, Docs: []string{"[]"}},
		{Session: sess.ID(), Time: at(10, 3, 0), Route: "direct", TokensIn: 50, TokensOut: 5,
			DurationMillis: 2000, Docs: []string{"[]"}},
	}
	if got := readTurns(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v\nwant %+v", got, want)
	}

	// The table has rows now, so a second replay does nothing.
	if n, err := s.ReplayTurns(ctx, dir); err != nil || n != 0 {
		t.Errorf("second ReplayTurns = %d, %v; want 0, nil", n, err)
	}
}

func TestDiskBytes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.InsertTurn(ctx, Turn{Session: "s1", Time: at(10, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		want += info.Size()
	}
	got, err := s.DiskBytes()
	if err != nil {
		t.Fatalf("DiskBytes: %v", err)
	}
	if got != want || got == 0 {
		t.Errorf("DiskBytes = %d, want %d and more than 0", got, want)
	}

	// Missing files count as 0 bytes, so a store whose files are all gone
	// reports 0.
	none := &Store{path: filepath.Join(t.TempDir(), "gone.db")}
	if got, err := none.DiskBytes(); err != nil || got != 0 {
		t.Errorf("DiskBytes with no files = %d, %v; want 0, nil", got, err)
	}
}

// BenchmarkUsage measures Usage over 50,000 turns spread over a year, each
// citing up to three of 2,000 files. The target is under 50 ms per call.
//
//	go test -run '^$' -bench Usage ./internal/store/
func BenchmarkUsage(b *testing.B) {
	s, err := Open(context.Background(), Options{Path: filepath.Join(b.TempDir(), "meru.db"), EmbedModel: "m1", Dims: testDims})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	const rows = 50_000
	err = s.write(ctx, func(tx *sql.Tx) error {
		for i := range rows {
			docs := make([]string, i%4)
			for j := range docs {
				docs[j] = fmt.Sprintf("/notes/%d.md", (i*7+j)%2000)
			}
			t := Turn{
				Session: fmt.Sprintf("s%d", i/10),
				// About one turn every ten minutes, going back a year.
				Time:     now.Add(-time.Duration(i) * 10 * time.Minute),
				TokensIn: 1200, TokensOut: 200, DurationMillis: 3000, ToolCalls: i % 2, Docs: docs,
			}
			if err := insertTurn(ctx, tx, t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if _, err := s.Usage(ctx, now); err != nil {
			b.Fatal(err)
		}
	}
}
