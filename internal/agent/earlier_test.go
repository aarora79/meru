// This file tests the "From earlier conversations" section: its format and
// cap, which routes add it, and the v0.4 "Done when" line, "it recalls
// something from last week without a reminder", end to end over a real
// store, real transcripts, the summarizer and a fake engine.

package agent

import (
	"context"
	"errors"
	"hash/fnv"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/summarize"
	"github.com/aarora79/meru/internal/transcript"
)

// pastSession builds one recalled session for the format tests.
func pastSession(started time.Time, summary string, match *store.Message) retrieve.SessionResult {
	return retrieve.SessionResult{Session: store.Session{ID: "s", Started: started, Summary: summary}, Match: match}
}

func TestFormatEarlier(t *testing.T) {
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.Local)
	lastWeek := now.AddDate(0, 0, -7)
	results := []retrieve.SessionResult{
		pastSession(lastWeek, "Chose two raised\nbeds for the garden.",
			&store.Message{Role: "user", Text: "how many beds for the garden?"}),
		pastSession(now.AddDate(0, 0, -1), "", &store.Message{Role: "assistant", Text: "Seeds come next week."}),
		pastSession(now, "Asked about the library.", nil),
		pastSession(now, "", nil), // nothing to show
	}
	got, n := formatEarlier(results, now, maxEarlierChars)
	want := earlierHeader +
		"\n- " + lastWeek.Format("2006-01-02") + ` (7 days ago): Chose two raised beds for the garden. The user said: "how many beds for the garden?"` +
		"\n- " + now.AddDate(0, 0, -1).Format("2006-01-02") + ` (yesterday): You said: "Seeds come next week."` +
		"\n- " + now.Format("2006-01-02") + " (today): Asked about the library."
	if got != want || n != 3 {
		t.Errorf("formatEarlier =\n%s\n(%d lines)\nwant\n%s", got, n, want)
	}

	// The cap drops the lines that don't fit.
	limit := len([]rune(earlierHeader)) + 120
	got, n = formatEarlier(results, now, limit)
	if n != 1 || len([]rune(got)) > limit || strings.Contains(got, "Seeds") {
		t.Errorf("formatEarlier under a cap of %d = %q (%d lines), want the first line only", limit, got, n)
	}

	// Nothing to show gives no section at all.
	if got, n := formatEarlier(nil, now, maxEarlierChars); got != "" || n != 0 {
		t.Errorf("formatEarlier(nil) = %q, %d", got, n)
	}

	// A long summary is cut.
	long := strings.Repeat("garden ", 200)
	got, _ = formatEarlier([]retrieve.SessionResult{pastSession(now, long, nil)}, now, maxEarlierChars)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > len([]rune(earlierHeader))+maxEarlierSummary+40 {
		t.Errorf("long summary not cut: %d characters", len([]rune(got)))
	}
}

func TestEarlierConversationsInPrompt(t *testing.T) {
	past := []retrieve.SessionResult{pastSession(time.Now().AddDate(0, 0, -7),
		"Chose two raised beds for the garden.", nil)}
	tests := []struct {
		name     string
		route    string
		err      error
		searched bool // SearchSessions was called
		want     bool // the section is in the prompt
	}{
		{"search route", "search", nil, true, true},
		{"tools route", "tools", nil, true, true},
		{"direct route", "direct", nil, false, false},
		{"recall fails", "search", errors.New("store down"), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &fakeSearcher{sessions: past, sessionsErr: tt.err}
			eng := &fakeEngine{pieces: []string{"Two raised beds."}}
			a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.9, Outcome: "ok"}},
				search, nil, nil, nil, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "what did we decide about the garden beds?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := len(search.sessionQueries) > 0; got != tt.searched {
				t.Fatalf("SearchSessions called = %v, want %v", got, tt.searched)
			}
			if tt.searched && search.excluded[0] != evs[0].Session {
				t.Errorf("excluded session %q, want the turn's own %q", search.excluded[0], evs[0].Session)
			}
			system := eng.lastCall().msgs[0].Content
			if got := strings.Contains(system, "From earlier conversations:\n"); got != tt.want {
				t.Errorf("section in prompt = %v, want %v:\n%s", got, tt.want, system)
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventSources {
					t.Errorf("a past session made a sources event: %+v", ev)
				}
			}
		})
	}
}

// recallEngine is fakeEngine with a working Generate for the summarizer and
// Embed for search. Embedding the *fakeEngine gives it Stream and Info.
type recallEngine struct {
	*fakeEngine
}

// Generate plays the summarizer's fast model: it summarizes the garden
// conversation as a person would, and any other as one about the library.
func (e recallEngine) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	text := "The user asked when the library closes; it closes at 6 pm on Saturdays."
	if strings.Contains(msgs[len(msgs)-1].Content, "garden") {
		text = "The user chose two raised beds for the garden."
	}
	return engine.Completion{Text: text, DoneReason: "stop"}, nil
}

// Embed gives each text a bag-of-words vector: one count per word of four
// letters or more, in one of recallDims slots picked by a hash of the word.
// Texts that share words point the same way.
func (e recallEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	out := make([]engine.Vector, len(texts))
	for i, text := range texts {
		v := make(engine.Vector, recallDims)
		for _, w := range words(text) {
			if len(w) >= 4 {
				h := fnv.New32a()
				h.Write([]byte(w))
				v[h.Sum32()%recallDims]++
			}
		}
		out[i] = v
	}
	return out, nil
}

// recallDims is the vector size of recallEngine.
const recallDims = 32

// recallSearcher searches past sessions for real and finds no files.
type recallSearcher struct {
	st  *store.Store
	eng engine.Engine
}

func (r recallSearcher) Search(context.Context, string) ([]retrieve.Result, error) { return nil, nil }

func (r recallSearcher) SearchSessions(ctx context.Context, query, exclude string, n int) ([]retrieve.SessionResult, error) {
	return retrieve.SearchSessions(ctx, r.st, r.eng, query, exclude, n)
}

// TestRecallsLastWeekWithoutAReminder is the v0.4 "Done when" test. A
// session from seven days ago planned the garden beds. merud's
// summarizer finds it quiet and summarizes it; a week later, a new session
// asks what was decided, and its prompt holds that session.
func TestRecallsLastWeekWithoutAReminder(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	dir := filepath.Join(cfg.Dir, "sessions")
	eng := recallEngine{
		fakeEngine: &fakeEngine{pieces: []string{"You chose two raised beds."}},
	}
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(cfg.Dir, "meru.db"), EmbedModel: "fake", Dims: recallDims})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// Last week's sessions: the garden, and one about something else.
	week := time.Now().AddDate(0, 0, -7)
	for _, turn := range [][2]string{
		{"Let's plan the garden. How many raised beds should we build?", "Two raised beds fit the sunny strip, with room for a path."},
		{"When does the library close?", "6 pm on Saturdays."},
	} {
		sess, err := transcript.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range []transcript.Line{
			{TS: week, Type: transcript.TypeUser, Text: turn[0]},
			{TS: week.Add(time.Minute), Type: transcript.TypeAssistant, Text: turn[1]},
		} {
			if err := sess.Append(l); err != nil {
				t.Fatal(err)
			}
		}
		week = week.Add(time.Hour)
	}
	if _, err := st.ReplaySessions(ctx, dir); err != nil {
		t.Fatal(err)
	}
	// The summarizer's pass: both sessions have been quiet for a week.
	if n := summarize.New(st, eng, cfg.Models.Fast, 30*time.Minute, dir, quietLog()).Tick(ctx); n != 2 {
		t.Fatalf("summarizer wrote %d summaries, want 2", n)
	}

	// Today: a new session asks, with no reminder of what was said.
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "search", Confidence: 0.9, Outcome: "ok"}},
		recallSearcher{st: st, eng: eng}, nil, nil, nil, quietLog())
	if _, err := run(ctx, a, rpc.Request{Op: rpc.OpAsk, Text: "what did we decide about the garden beds?"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	system := eng.lastCall().msgs[0].Content
	_, section, found := strings.Cut(system, "From earlier conversations:")
	if !found {
		t.Fatalf("no earlier conversations in the prompt:\n%s", system)
	}
	first := strings.Split(strings.TrimSpace(section), "\n")[1] // line 0 is the header's second line
	for _, want := range []string{"(7 days ago)", "two raised beds for the garden"} {
		if !strings.Contains(first, want) {
			t.Errorf("first recalled session %q lacks %q", first, want)
		}
	}
}
