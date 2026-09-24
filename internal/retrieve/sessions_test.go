// This file tests SearchSessions on a real store filled from transcripts,
// with a fake engine: the merge of the three lists, the best message, the
// session left out, and the span.

package retrieve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// addSession writes a session with one question and answer, and a summary
// with vector v when summary isn't "", replays it into st and returns its ID.
func addSession(t *testing.T, st *store.Store, dir string, ts time.Time, question, summary string, v engine.Vector) string {
	t.Helper()
	ctx := context.Background()
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := []transcript.Line{
		{TS: ts, Type: transcript.TypeUser, Text: question},
		{TS: ts.Add(time.Second), Type: transcript.TypeAssistant, Text: "Noted."},
	}
	if summary != "" {
		lines = append(lines, transcript.Line{TS: ts.Add(time.Hour), Type: transcript.TypeSummary, Text: summary})
	}
	for _, l := range lines {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ReplaySession(ctx, dir, sess.ID()); err != nil {
		t.Fatal(err)
	}
	if summary != "" {
		if err := st.SetSessionVector(ctx, sess.ID(), summary, v); err != nil {
			t.Fatal(err)
		}
	}
	return sess.ID()
}

func TestSearchSessions(t *testing.T) {
	ctx := context.Background()
	rec := useRecorder(t)
	st := testStore(t)
	dir := t.TempDir()
	day := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	garden := addSession(t, st, dir, day, "what should the garden budget be?",
		"Set the garden budget at 400 dollars.", engine.Vector{1, 0, 0})
	taxes := addSession(t, st, dir, day.Add(24*time.Hour), "when are taxes due?",
		"Taxes are due in April.", engine.Vector{0, 1, 0})
	hose := addSession(t, st, dir, day.Add(48*time.Hour), "the garden hose leaks", "", nil)
	current := addSession(t, st, dir, day.Add(72*time.Hour), "garden budget again",
		"Asked about the garden budget again.", engine.Vector{1, 0, 0})

	eng := &fakeEngine{vectors: map[string]engine.Vector{"garden budget": {1, 0, 0}}}
	got, err := SearchSessions(ctx, st, eng, "garden budget", current, 3)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3: %+v", len(got), got)
	}
	// garden tops all three lists. taxes (second by meaning) and hose (second
	// by message keyword) tie; the tie goes to the session seen first, in
	// the meaning list.
	if got[0].ID != garden || got[1].ID != taxes || got[2].ID != hose {
		t.Errorf("order = %s, %s, %s; want garden, taxes, hose", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[0].Summary != "Set the garden budget at 400 dollars." || !got[0].Started.Equal(day) {
		t.Errorf("garden = %+v", got[0].Session)
	}
	if got[0].Match == nil || got[0].Match.Role != "user" || got[0].Match.Text != "what should the garden budget be?" {
		t.Errorf("garden's match = %+v, want the question", got[0].Match)
	}
	if got[1].Match != nil {
		t.Errorf("taxes matched only by meaning but has match %+v", got[1].Match)
	}
	if got[2].Summary != "" || got[2].Match == nil {
		t.Errorf("hose = %+v, want no summary and a match", got[2])
	}
	if !(got[0].Score > got[1].Score) {
		t.Errorf("scores %v, %v: want garden first", got[0].Score, got[1].Score)
	}
	for _, r := range got {
		if r.ID == current {
			t.Error("the current session came back")
		}
	}
	spans := rec.Ended()
	if len(spans) == 0 || spans[len(spans)-1].Name() != "meru.retrieve.sessions" {
		t.Errorf("no meru.retrieve.sessions span")
	}

	// n cuts the list.
	if two, _ := SearchSessions(ctx, st, eng, "garden budget", current, 1); len(two) != 1 || two[0].ID != garden {
		t.Errorf("n = 1 gave %+v", two)
	}
}

func TestSearchSessionsEdges(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	eng := &fakeEngine{}
	if got, err := SearchSessions(ctx, st, eng, "  ", "", 3); got != nil || err != nil {
		t.Errorf("blank query = %v, %v; want nothing", got, err)
	}
	if got, err := SearchSessions(ctx, st, eng, "q", "", 0); got != nil || err != nil {
		t.Errorf("n = 0 gave %v, %v; want nothing", got, err)
	}
	if eng.calls != 0 {
		t.Errorf("Embed called %d times for no search", eng.calls)
	}
	eng.err = errors.New("down")
	if _, err := SearchSessions(ctx, st, eng, "q", "", 3); !errors.Is(err, eng.err) {
		t.Errorf("err = %v, want the embed error", err)
	}
	// An empty store gives no sessions and no error.
	eng.err = nil
	eng.vectors = map[string]engine.Vector{"q": {1, 0, 0}}
	if got, err := SearchSessions(ctx, st, eng, "q", "", 3); len(got) != 0 || err != nil {
		t.Errorf("empty store = %v, %v", got, err)
	}
}
