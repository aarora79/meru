// This file tests the Summarizer against a real store in a temporary
// folder and a fake engine: which sessions get a summary and when, the cap
// on the backlog, the text the model reads, and what happens when the model
// fails or writes nothing.

package summarize

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// dims is the vector size of the fake embedding model.
const dims = 4

// fakeEngine answers Generate with a fixed text and records each call.
type fakeEngine struct {
	text string
	err  error

	mu    sync.Mutex // guards calls and embeds
	calls []engine.Options
	msgs  [][]engine.Message
	// embeds counts the texts passed to Embed.
	embeds int
}

func (f *fakeEngine) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, opts)
	f.msgs = append(f.msgs, msgs)
	if f.err != nil {
		return engine.Completion{}, f.err
	}
	return engine.Completion{Text: f.text, DoneReason: "stop"}, nil
}

func (f *fakeEngine) Stream(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (iter.Seq2[engine.Delta, error], error) {
	return nil, errors.New("not used")
}

func (f *fakeEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	f.mu.Lock()
	f.embeds += len(texts)
	f.mu.Unlock()
	out := make([]engine.Vector, len(texts))
	for i := range texts {
		out[i] = engine.Vector{1, 0, 0, 0}
	}
	return out, nil
}

func (f *fakeEngine) Info(ctx context.Context) (engine.ModelInfo, error) {
	return engine.ModelInfo{}, nil
}

// setup opens a store in a temporary folder and returns it with a sessions
// folder and a Summarizer whose clock reads now.
func setup(t *testing.T, eng *fakeEngine, now time.Time) (*store.Store, string, *Summarizer) {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "e", Dims: dims,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := filepath.Join(t.TempDir(), "sessions")
	s := New(st, eng, "fast-model", 30*time.Minute, dir, nil)
	s.now = func() time.Time { return now }
	return st, dir, s
}

// writeSession writes one question and answer at ts to a new session,
// replays it, and returns it.
func writeSession(t *testing.T, st *store.Store, dir string, ts time.Time, question string) *transcript.Session {
	t.Helper()
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []transcript.Line{
		{TS: ts, Type: transcript.TypeUser, Text: question},
		{TS: ts.Add(2 * time.Second), Type: transcript.TypeAssistant, Text: "An answer about " + question},
	} {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ReplaySession(context.Background(), dir, sess.ID()); err != nil {
		t.Fatal(err)
	}
	return sess
}

// summaries returns the text of each summary line in sess, in file order.
func summaries(t *testing.T, sess *transcript.Session) []string {
	t.Helper()
	lines, err := transcript.ReadLines(sess.Path())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range lines {
		if l.Type == transcript.TypeSummary {
			out = append(out, l.Text)
		}
	}
	return out
}

func TestTickSummarizesQuietSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	eng := &fakeEngine{text: "  The user chose two raised\nbeds for the garden.  "}
	st, dir, s := setup(t, eng, now)

	quiet := writeSession(t, st, dir, now.Add(-2*time.Hour), "garden beds")
	active := writeSession(t, st, dir, now.Add(-5*time.Minute), "library")

	if n := s.Tick(ctx); n != 1 {
		t.Fatalf("Tick wrote %d summaries, want 1", n)
	}
	if got := summaries(t, quiet); len(got) != 1 || got[0] != "The user chose two raised beds for the garden." {
		t.Errorf("quiet session's summaries = %q", got)
	}
	if got := summaries(t, active); len(got) != 0 {
		t.Errorf("active session got a summary: %q", got)
	}
	opts := eng.calls[0]
	if opts.Model != "fast-model" || !opts.NoThink || opts.MaxTokens != maxTokens || opts.LogProbs {
		t.Errorf("model call options = %+v; want the fast model, thinking off, a token cap", opts)
	}
	if !strings.Contains(eng.msgs[0][1].Content, "User: garden beds") {
		t.Errorf("model read %q, want the conversation", eng.msgs[0][1].Content)
	}
	// The store has the summary and its vector.
	rows, err := st.Sessions(ctx, []string{quiet.ID()})
	if err != nil || len(rows) != 1 || rows[0].Summary == "" {
		t.Fatalf("Sessions = %+v, %v", rows, err)
	}
	if todo, _ := st.SummariesWithoutVector(ctx, 10); len(todo) != 0 || eng.embeds != 1 {
		t.Errorf("summaries without a vector = %+v, embeds = %d; want none, 1", todo, eng.embeds)
	}

	// Nothing new: the next pass writes nothing.
	if n := s.Tick(ctx); n != 0 {
		t.Errorf("second Tick wrote %d summaries, want 0", n)
	}

	// The quiet session grows, then goes quiet again: it gets a new summary,
	// and the newest wins.
	if err := quiet.Append(transcript.Line{TS: now.Add(10 * time.Minute), Type: transcript.TypeUser, Text: "and seeds?"}); err != nil {
		t.Fatal(err)
	}
	if err := quiet.Append(transcript.Line{TS: now.Add(11 * time.Minute), Type: transcript.TypeAssistant, Text: "Next week."}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaySession(ctx, dir, quiet.ID()); err != nil {
		t.Fatal(err)
	}
	eng.text = "Chose the beds and ordered seeds."
	s.now = func() time.Time { return now.Add(time.Hour) }
	// Both sessions are quiet now, so both get a summary.
	if n := s.Tick(ctx); n != 2 {
		t.Fatalf("Tick after new turns wrote %d, want 2", n)
	}
	rows, _ = st.Sessions(ctx, []string{quiet.ID()})
	if rows[0].Summary != "Chose the beds and ordered seeds." {
		t.Errorf("summary = %q, want the newest", rows[0].Summary)
	}
	if got := summaries(t, quiet); len(got) != 2 {
		t.Errorf("summary lines = %q, want two", got)
	}
}

func TestTickCapsTheBacklog(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	eng := &fakeEngine{text: "A summary."}
	st, dir, s := setup(t, eng, now)
	var sessions []*transcript.Session
	for i := range 7 {
		// Session i is i days old.
		sessions = append(sessions, writeSession(t, st, dir, now.Add(-time.Duration(i+1)*24*time.Hour), "q"))
	}
	if n := s.Tick(ctx); n != maxPerTick {
		t.Fatalf("first Tick wrote %d, want %d", n, maxPerTick)
	}
	// The newest five went first.
	for i, sess := range sessions {
		want := 0
		if i < maxPerTick {
			want = 1
		}
		if got := len(summaries(t, sess)); got != want {
			t.Errorf("session %d days old has %d summaries, want %d", i+1, got, want)
		}
	}
	if n := s.Tick(ctx); n != 2 {
		t.Errorf("second Tick wrote %d, want 2", n)
	}
}

func TestTickWhenTheModelFailsOrSaysNothing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	t.Run("fails", func(t *testing.T) {
		eng := &fakeEngine{err: errors.New("ollama down")}
		st, dir, s := setup(t, eng, now)
		sess := writeSession(t, st, dir, now.Add(-time.Hour), "q")
		if n := s.Tick(ctx); n != 0 {
			t.Errorf("Tick wrote %d, want 0", n)
		}
		if got := summaries(t, sess); len(got) != 0 {
			t.Errorf("summaries = %q, want none", got)
		}
		// The session stays due, for the next pass.
		if ids, _ := st.DueSummaries(ctx, now, 5); len(ids) != 1 {
			t.Errorf("due = %v, want the session", ids)
		}
	})
	t.Run("says nothing", func(t *testing.T) {
		eng := &fakeEngine{text: " \n "}
		st, dir, s := setup(t, eng, now)
		sess := writeSession(t, st, dir, now.Add(-time.Hour), "when do I sow the tomatoes?")
		if n := s.Tick(ctx); n != 1 {
			t.Fatalf("Tick wrote %d, want 1", n)
		}
		if got := summaries(t, sess); len(got) != 1 || got[0] != "when do I sow the tomatoes?" {
			t.Errorf("summaries = %q, want the first question", got)
		}
	})
}

func TestRunStopsWithContext(t *testing.T) {
	eng := &fakeEngine{text: "x"}
	_, _, s := setup(t, eng, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't stop after cancel")
	}
}

func TestInput(t *testing.T) {
	long := strings.Repeat("word ", 400) // 2,000 characters
	tests := []struct {
		name  string
		lines []transcript.Line
		limit int
		want  string
	}{
		{"empty", nil, 100, ""},
		{"tool lines only", []transcript.Line{{Type: transcript.TypeToolCall, Tool: "x"}}, 100, ""},
		{"all fit", []transcript.Line{
			{Type: transcript.TypeUser, Text: "q1"},
			{Type: transcript.TypeAssistant, Text: "a1\n\nmore"},
			{Type: transcript.TypeSummary, Text: "old summary"},
		}, 100, "User: q1\nMeru: a1 more"},
		{"keeps the first question and the newest lines", []transcript.Line{
			{Type: transcript.TypeUser, Text: "first"},
			{Type: transcript.TypeAssistant, Text: "aaaaaaaaaa"},
			{Type: transcript.TypeUser, Text: "second"},
			{Type: transcript.TypeAssistant, Text: "newest"},
		}, 40, "User: first\n[…]\nUser: second\nMeru: newest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Input(tt.lines, tt.limit); got != tt.want {
				t.Errorf("Input = %q, want %q", got, tt.want)
			}
		})
	}

	// A long answer is cut to maxLineChars.
	got := Input([]transcript.Line{{Type: transcript.TypeAssistant, Text: long}}, maxInputChars)
	if n := len([]rune(got)); n != len("Meru: ")+maxLineChars || !strings.HasSuffix(got, "…") {
		t.Errorf("long line gave %d characters, want %d ending in …", n, len("Meru: ")+maxLineChars)
	}
}
