// This file tests the past-conversation tables: replaying transcripts into
// them a few lines at a time, the lists of sessions due a summary or a
// vector, and the three session searches.

package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/transcript"
)

// appendLines writes lines to sess or fails the test.
func appendLines(t *testing.T, sess *transcript.Session, lines ...transcript.Line) {
	t.Helper()
	for _, l := range lines {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}
}

// count returns the result of a count(*) query or fails the test.
func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// checkMessageIndex fails the test when message_fts disagrees with
// messages, using FTS5's integrity check.
func checkMessageIndex(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO message_fts (message_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Errorf("message_fts integrity check: %v", err)
	}
}

// loadSession returns the one session with id, or fails the test.
func loadSession(t *testing.T, s *Store, id string) Session {
	t.Helper()
	got, err := s.Sessions(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("Sessions(%s) = %v, %v", id, got, err)
	}
	return got[0]
}

func TestReplaySessionsIsIncremental(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "sessions")

	if n, err := s.ReplaySessions(ctx, dir); err != nil || n != 0 {
		t.Fatalf("ReplaySessions on a missing folder = %d, %v; want 0, nil", n, err)
	}

	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendLines(t, sess,
		transcript.Line{TS: at(10, 0, 0), Type: transcript.TypeUser, Text: "when do I sow the tomatoes?", TraceID: "t1"},
		transcript.Line{TS: at(10, 0, 1), Type: transcript.TypeToolCall, CallID: "c1", Kind: "builtin", Tool: "remember"},
		transcript.Line{TS: at(10, 0, 2), Type: transcript.TypeToolResult, CallID: "c1", Outcome: "ok"},
		transcript.Line{TS: at(10, 0, 3), Type: transcript.TypeAssistant, Text: "On 12 April.", TraceID: "t1"},
	)

	n, err := s.ReplaySessions(ctx, dir)
	if err != nil || n != 2 {
		t.Fatalf("ReplaySessions = %d, %v; want 2 (the tool lines stay out)", n, err)
	}
	got := loadSession(t, s, sess.ID())
	if !got.Started.Equal(at(10, 0, 0)) || !got.Last.Equal(at(10, 0, 3)) || got.Turns != 1 || got.Summary != "" {
		t.Errorf("session = %+v", got)
	}

	// Nothing new: a second replay adds nothing.
	if n, err := s.ReplaySessions(ctx, dir); err != nil || n != 0 {
		t.Errorf("second ReplaySessions = %d, %v; want 0, nil", n, err)
	}

	// New lines, a summary among them: only they are read.
	appendLines(t, sess,
		transcript.Line{TS: at(10, 40, 0), Type: transcript.TypeSummary, Text: "Chose two raised beds for the garden."},
		transcript.Line{TS: at(11, 0, 0), Type: transcript.TypeUser, Text: "and the seeds?"},
		transcript.Line{TS: at(11, 0, 2), Type: transcript.TypeAssistant, Text: "Next week."},
	)
	if n, err := s.ReplaySession(ctx, dir, sess.ID()); err != nil || n != 3 {
		t.Fatalf("ReplaySession = %d, %v; want 3", n, err)
	}
	got = loadSession(t, s, sess.ID())
	if got.Turns != 2 || !got.Last.Equal(at(11, 0, 2)) || got.Summary != "Chose two raised beds for the garden." ||
		!got.SummaryTime.Equal(at(10, 40, 0)) {
		t.Errorf("session after the new lines = %+v", got)
	}
	if n := count(t, s, `SELECT count(*) FROM messages WHERE session = ?`, sess.ID()); n != 4 {
		t.Errorf("messages = %d, want 4", n)
	}
	checkMessageIndex(t, s)

	// A file cut by hand is read again from the start.
	b, err := os.ReadFile(sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	first := b[:indexAfterLine(b, 1)]
	if err := os.WriteFile(sess.Path(), first, 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReplaySession(ctx, dir, sess.ID()); err != nil || n != 1 {
		t.Fatalf("ReplaySession after a cut = %d, %v; want 1", n, err)
	}
	got = loadSession(t, s, sess.ID())
	if got.Turns != 0 || got.Summary != "" {
		t.Errorf("session after a cut = %+v", got)
	}
	if n := count(t, s, `SELECT count(*) FROM summary_fts`); n != 0 {
		t.Errorf("summary_fts rows after a cut = %d, want 0", n)
	}
	checkMessageIndex(t, s)
}

// indexAfterLine returns the offset just past the nth newline in b.
func indexAfterLine(b []byte, n int) int {
	for i, c := range b {
		if c == '\n' {
			n--
			if n == 0 {
				return i + 1
			}
		}
	}
	return len(b)
}

func TestReplaySessionRejectsBadID(t *testing.T) {
	s := openTest(t)
	if _, err := s.ReplaySession(context.Background(), t.TempDir(), "../../etc/passwd"); err == nil {
		t.Error("ReplaySession accepted a path as an ID")
	}
}

func TestConcurrentReplaysAddEachLineOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendLines(t, sess,
		transcript.Line{Type: transcript.TypeUser, Text: "q"},
		transcript.Line{Type: transcript.TypeAssistant, Text: "a"},
	)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ReplaySession(ctx, dir, sess.ID()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := count(t, s, `SELECT count(*) FROM messages`); n != 2 {
		t.Errorf("messages = %d, want 2", n)
	}
}

// newSession writes a session whose one turn happened at ts, with an
// optional summary line after it, and replays it.
func newSession(t *testing.T, s *Store, dir string, ts time.Time, question, summary string) string {
	t.Helper()
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendLines(t, sess,
		transcript.Line{TS: ts, Type: transcript.TypeUser, Text: question},
		transcript.Line{TS: ts.Add(time.Second), Type: transcript.TypeAssistant, Text: "ok"},
	)
	if summary != "" {
		appendLines(t, sess, transcript.Line{TS: ts.Add(time.Hour), Type: transcript.TypeSummary, Text: summary})
	}
	if _, err := s.ReplaySession(context.Background(), dir, sess.ID()); err != nil {
		t.Fatal(err)
	}
	return sess.ID()
}

func TestDueSummaries(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	old := newSession(t, s, dir, now.Add(-72*time.Hour), "q old", "")
	older := newSession(t, s, dir, now.Add(-96*time.Hour), "q older", "")
	newSession(t, s, dir, now.Add(-48*time.Hour), "q done", "already summarized")
	newSession(t, s, dir, now.Add(-5*time.Minute), "q active", "")

	ids, err := s.DueSummaries(ctx, now.Add(-30*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != old || ids[1] != older {
		t.Errorf("DueSummaries = %v, want [%s %s], newest first", ids, old, older)
	}
	if ids, _ := s.DueSummaries(ctx, now.Add(-30*time.Minute), 1); len(ids) != 1 || ids[0] != old {
		t.Errorf("DueSummaries with limit 1 = %v, want [%s]", ids, old)
	}
}

func TestSessionVectorsAndModelChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meru.db")
	dir := t.TempDir()
	s, err := Open(ctx, Options{Path: path, EmbedModel: "m1", Dims: testDims})
	if err != nil {
		t.Fatal(err)
	}
	id := newSession(t, s, dir, at(9, 0, 0), "q", "Planned the garden.")

	todo, err := s.SummariesWithoutVector(ctx, 10)
	if err != nil || len(todo) != 1 || todo[0].Session != id || todo[0].Summary != "Planned the garden." {
		t.Fatalf("SummariesWithoutVector = %+v, %v", todo, err)
	}
	// A vector for a summary that has since changed is not stored.
	if err := s.SetSessionVector(ctx, id, "an older summary", unit(0)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM session_vec`); n != 0 {
		t.Errorf("session_vec holds a vector for a stale summary")
	}
	if err := s.SetSessionVector(ctx, id, "Planned the garden.", unit(0)); err != nil {
		t.Fatal(err)
	}
	if todo, _ := s.SummariesWithoutVector(ctx, 10); len(todo) != 0 {
		t.Errorf("SummariesWithoutVector after storing = %+v, want none", todo)
	}
	if err := s.SetSessionVector(ctx, id, "Planned the garden.", engine.Vector{1}); err == nil {
		t.Error("SetSessionVector took a vector of the wrong size")
	}
	s.Close()

	// A new embedding model clears the summary vectors too.
	s = openAt(t, path, "m2", testDims)
	if todo, _ := s.SummariesWithoutVector(ctx, 10); len(todo) != 1 {
		t.Errorf("SummariesWithoutVector after a model change = %+v, want the one summary", todo)
	}
}

func TestSessionSearches(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	garden := newSession(t, s, dir, at(9, 0, 0), "how many raised beds should the garden have?", "Chose two raised beds for the garden.")
	library := newSession(t, s, dir, at(10, 0, 0), "when does the library close?", "The library closes at 6 pm on Saturdays.")
	current := newSession(t, s, dir, at(11, 0, 0), "garden beds again", "Asked about the garden beds again.")
	for i, id := range []string{garden, library, current} {
		ss := loadSession(t, s, id)
		if err := s.SetSessionVector(ctx, id, ss.Summary, unit(i)); err != nil {
			t.Fatal(err)
		}
	}

	vec, err := s.SearchSessionVector(ctx, unit(1), current, 5)
	if err != nil || len(vec) != 2 || vec[0].Session != library || vec[1].Session != garden {
		t.Errorf("SearchSessionVector = %+v, %v; want library, then garden", vec, err)
	}
	sum, err := s.SearchSummaryKeyword(ctx, "garden beds", current, 5)
	if err != nil || len(sum) != 1 || sum[0].Session != garden {
		t.Errorf("SearchSummaryKeyword = %+v, %v; want garden alone", sum, err)
	}
	msgs, err := s.SearchMessageKeyword(ctx, "garden", current, 5)
	if err != nil || len(msgs) != 1 || msgs[0].Session != garden || msgs[0].MessageID == 0 {
		t.Fatalf("SearchMessageKeyword = %+v, %v; want one garden message", msgs, err)
	}
	loaded, err := s.Messages(ctx, []int64{msgs[0].MessageID, 9999})
	if err != nil || len(loaded) != 1 || loaded[0].Role != "user" || loaded[0].Text != "how many raised beds should the garden have?" {
		t.Errorf("Messages = %+v, %v", loaded, err)
	}
	// Excluding nothing finds the current session too.
	if all, _ := s.SearchSummaryKeyword(ctx, "garden", "", 5); len(all) != 2 {
		t.Errorf("SearchSummaryKeyword with no exclusion = %+v, want 2", all)
	}
	if _, err := s.SearchSessionVector(ctx, engine.Vector{1}, "", 5); err == nil {
		t.Error("SearchSessionVector took a vector of the wrong size")
	}
}
