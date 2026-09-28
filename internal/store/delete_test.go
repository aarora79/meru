// This file tests what chat organizing asks of the store: tags from meta
// lines reaching the summary keyword search, DeleteSession, and the replay
// that drops the rows of a transcript that is gone.

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/transcript"
)

func TestReplayMetaTags(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	id := newSession(t, s, dir, at(9, 0, 0), "how deep should the pond be?", "Chose a pond 60 cm deep.")
	ss := loadSession(t, s, id)
	if err := s.SetSessionVector(ctx, id, ss.Summary, unit(0)); err != nil {
		t.Fatal(err)
	}
	sess, err := transcript.Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name    string
		tags    []string
		search  string
		found   bool
		keepVec bool
	}{
		{"a tag finds the session", []string{"wildlife", "backyard"}, "wildlife", true, true},
		{"the summary still does", []string{"wildlife", "backyard"}, "pond", true, true},
		{"the newest meta line wins", []string{"frogs"}, "wildlife", false, true},
		{"the new tag", []string{"frogs"}, "frogs", true, true},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if err := sess.SetMeta(transcript.Meta{Tags: st.tags}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReplaySession(ctx, dir, id); err != nil {
				t.Fatal(err)
			}
			hits, err := s.SearchSummaryKeyword(ctx, st.search, "", 5)
			if err != nil || (len(hits) == 1) != st.found {
				t.Errorf("SearchSummaryKeyword(%q) = %+v, %v; want found %v", st.search, hits, err, st.found)
			}
			// A tag change keeps the summary's vector: it holds the summary only.
			if n := count(t, s, `SELECT count(*) FROM session_vec WHERE session = ?`, id); (n == 1) != st.keepVec {
				t.Errorf("session_vec rows = %d", n)
			}
		})
	}

	// A new summary keeps the tags in the keyword index.
	appendLines(t, sess, transcript.Line{TS: at(12, 0, 0), Type: transcript.TypeSummary, Text: "Dug the pond."})
	if _, err := s.ReplaySession(ctx, dir, id); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.SearchSummaryKeyword(ctx, "frogs", "", 5); len(hits) != 1 {
		t.Errorf("after a new summary, a tag search = %+v", hits)
	}
	if n := count(t, s, `SELECT count(*) FROM summary_fts WHERE session = ?`, id); n != 1 {
		t.Errorf("summary_fts rows = %d, want 1", n)
	}
}

func TestDeleteSession(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	gone := newSession(t, s, dir, at(9, 0, 0), "what soil do blueberries need?", "Blueberries need acid soil.")
	kept := newSession(t, s, dir, at(10, 0, 0), "when do tulips flower?", "Tulips flower in April.")
	for _, id := range []string{gone, kept} {
		if err := s.SetSessionVector(ctx, id, loadSession(t, s, id).Summary, unit(0)); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertTurn(ctx, Turn{Session: id, Time: at(9, 0, 0), Route: "direct"}); err != nil {
			t.Fatal(err)
		}
		insertCall(t, s, id)
	}

	if err := s.DeleteSession(ctx, gone); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM sessions WHERE id = ?`,
		`SELECT count(*) FROM messages WHERE session = ?`,
		`SELECT count(*) FROM summary_fts WHERE session = ?`,
		`SELECT count(*) FROM session_vec WHERE session = ?`,
		`SELECT count(*) FROM turns WHERE session = ?`,
	} {
		if n := count(t, s, q, gone); n != 0 {
			t.Errorf("%s: %d rows left", q, n)
		}
		if n := count(t, s, q, kept); n == 0 {
			t.Errorf("%s: the other session lost its rows", q)
		}
	}
	checkStripped(t, s, gone, true)
	checkStripped(t, s, kept, false)
	checkMessageIndex(t, s)
	if hits, _ := s.SearchMessageKeyword(ctx, "blueberries", "", 5); len(hits) != 0 {
		t.Errorf("a message search still finds the deleted chat: %+v", hits)
	}
	// Deleting again, or a session never seen, is fine.
	if err := s.DeleteSession(ctx, "2026-01-01T000000-0000"); err != nil {
		t.Errorf("DeleteSession of an unknown session: %v", err)
	}
}

func TestReplayPrunesMissingFiles(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	gone := newSession(t, s, dir, time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC), "what is mulch?", "")
	kept := newSession(t, s, dir, time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC), "what is compost?", "")
	insertCall(t, s, gone)
	insertCall(t, s, kept)
	sess, err := transcript.Open(dir, gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sess.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaySessions(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM sessions WHERE id = ?`, gone); n != 0 {
		t.Error("the replay kept the row of a missing transcript")
	}
	if n := count(t, s, `SELECT count(*) FROM sessions WHERE id = ?`, kept); n != 1 {
		t.Error("the replay dropped a transcript that is still there")
	}
	checkStripped(t, s, gone, true)
	checkStripped(t, s, kept, false)
}

// insertCall writes one tool_calls row for session, with arguments and a
// result, as dispatch would.
func insertCall(t *testing.T, s *Store, session string) {
	t.Helper()
	err := s.InsertToolCall(context.Background(), ToolCall{
		CallID: "c1", Session: session, Time: at(9, 0, 0), Kind: "mcp", Server: "obsidian",
		Tool: "search", Args: []byte(`{"query":"soil"}`), Result: "soil.md: acid soil",
		Outcome: "ok", Approval: "once", DurationMillis: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkStripped checks session's one tool_calls row: still there, with its
// tool, server, kind, time, duration, outcome and approval, and, when
// stripped, with no arguments and no result.
func checkStripped(t *testing.T, s *Store, session string, stripped bool) {
	t.Helper()
	rows, err := s.ToolCalls(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []ToolCall
	for _, r := range rows {
		if r.Session == session {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("tool_calls rows for %s = %+v, want one", session, found)
	}
	r := found[0]
	if r.Tool != "search" || r.Server != "obsidian" || r.Kind != "mcp" || r.Outcome != "ok" ||
		r.Approval != "once" || r.DurationMillis != 40 || !r.Time.Equal(at(9, 0, 0)) {
		t.Errorf("row for %s lost its audit fields: %+v", session, r)
	}
	if empty := len(r.Args) == 0 && r.Result == ""; empty != stripped {
		t.Errorf("row for %s: args %s, result %q; stripped %v", session, r.Args, r.Result, stripped)
	}
}
