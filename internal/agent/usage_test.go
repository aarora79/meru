// This file tests what a turn keeps for `meru usage`: the route, duration
// and sources on the assistant line, and the row it hands the
// TurnRecorder.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// fakeTurns records the rows it gets, and fails every insert when err is
// set.
type fakeTurns struct {
	err error

	mu   sync.Mutex // guards rows
	rows []store.Turn
}

func (f *fakeTurns) InsertTurn(ctx context.Context, t store.Turn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, t)
	return f.err
}

// recorded returns the rows so far.
func (f *fakeTurns) recorded() []store.Turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Turn(nil), f.rows...)
}

func TestTurnRowAndAssistantLine(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	garden := filepath.Join(home, "notes", "garden.md")
	// Two excerpts from one file: the file counts once.
	search := &fakeSearcher{results: []retrieve.Result{
		result(garden, "Planting", "Sow tomatoes on 12 April.", 3, 5, 0.03),
		result("/srv/plan.md", "", "Plant tomatoes in May.", 1, 1, 0.02),
		result(garden, "Beds", "Six raised beds.", 9, 9, 0.01),
	}}

	tests := []struct {
		name      string
		route     string // the router's pick
		question  string
		folders   []string
		rounds    []fakeRound
		wantRoute string // after the override rules
		wantDocs  []string
		wantCalls int
	}{
		{
			name: "search and one tool call", route: "search+tools", question: "When does the garden project sow tomatoes?",
			rounds: []fakeRound{
				{calls: []engine.ToolCall{call("notes.search", `{"q":"garden"}`)}, usage: engine.Usage{PromptTokens: 10, OutputTokens: 2}},
				{pieces: []string{"12 April [1]."}, usage: engine.Usage{PromptTokens: 30, OutputTokens: 5}},
			},
			wantRoute: "search+tools", wantDocs: []string{garden, "/srv/plan.md"}, wantCalls: 1,
		},
		{
			name: "direct changed to search", route: "direct", question: "what is in my garden folder?",
			folders:   []string{filepath.Join(home, "garden")},
			rounds:    []fakeRound{{pieces: []string{"Beds."}, usage: engine.Usage{PromptTokens: 40, OutputTokens: 7}}},
			wantRoute: "search", wantDocs: []string{garden, "/srv/plan.md"},
		},
		{
			name: "direct with no search", route: "direct", question: "hello",
			rounds:    []fakeRound{{pieces: []string{"Hi."}, usage: engine.Usage{PromptTokens: 40, OutputTokens: 7}}},
			wantRoute: "direct",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Index.Folders = tt.folders
			eng := &fakeEngine{rounds: tt.rounds}
			tools := &fakeTools{
				specs:   []engine.ToolSpec{spec("notes.search")},
				results: map[string]fakeResult{"notes.search": {text: "garden.md"}},
			}
			turns := &fakeTurns{}
			router := &fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.9, Outcome: "ok"}}
			a := New(cfg, eng, router, search, tools, turns, nil, quietLog())

			evs, err := run(context.Background(), a, rpc.Request{Text: tt.question, Source: rpc.SourceTUI})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			id := evs[0].Session
			lines := readLines(t, cfg, id)
			user, answer := lines[0], lines[len(lines)-1]
			if answer.Type != transcript.TypeAssistant || answer.Route != tt.wantRoute ||
				!reflect.DeepEqual(answer.Sources, tt.wantDocs) || answer.Ms < 0 {
				t.Errorf("assistant line = %+v, want route %q, sources %v", answer, tt.wantRoute, tt.wantDocs)
			}

			rows := turns.recorded()
			if len(rows) != 1 {
				t.Fatalf("got %d turn rows, want 1", len(rows))
			}
			var tokensIn, tokensOut int64
			for _, r := range tt.rounds {
				tokensIn += int64(r.usage.PromptTokens)
				tokensOut += int64(r.usage.OutputTokens)
			}
			row := rows[0]
			want := store.Turn{
				Session: id, Time: row.Time, Source: "tui", Route: tt.wantRoute,
				TokensIn: tokensIn, TokensOut: tokensOut, DurationMillis: answer.Ms,
				ToolCalls: tt.wantCalls, Docs: tt.wantDocs, TraceID: answer.TraceID,
				Model: cfg.Models.Main, EvalMillis: answer.EvalMs, TTFTMillis: answer.TTFTMs,
			}
			if !reflect.DeepEqual(row, want) {
				t.Errorf("row = %+v\nwant %+v", row, want)
			}
			// The row's time is the question's, as the user line holds it,
			// so a row rebuilt from the transcript matches.
			if got := row.Time.UTC().Truncate(time.Second); !got.Equal(user.TS) {
				t.Errorf("row time = %v, want the user line's %v", got, user.TS)
			}
		})
	}
}

// TestTurnRowFailureKeepsTheAnswer checks that a failed insert leaves the
// turn and its transcript alone.
func TestTurnRowFailureKeepsTheAnswer(t *testing.T) {
	cfg := testConfig(t)
	turns := &fakeTurns{err: errors.New("disk full")}
	a := New(cfg, &fakeEngine{pieces: []string{"ok"}}, &fakeRouter{dec: Decision{Route: "direct"}}, nil, nil, turns, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Text: "q"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if last := evs[len(evs)-1]; last.Type != rpc.EventDone {
		t.Errorf("last event = %+v, want done", last)
	}
	if lines := readLines(t, cfg, evs[0].Session); len(lines) != 2 || lines[1].Type != transcript.TypeAssistant {
		t.Errorf("transcript = %+v, want the question and the answer", lines)
	}
}

// TestNoTurnRowWithoutAnAnswer checks that a failed turn writes no row.
func TestNoTurnRowWithoutAnAnswer(t *testing.T) {
	turns := &fakeTurns{}
	eng := &fakeEngine{streamErr: errors.New("model gone")}
	a := New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: "direct"}}, nil, nil, turns, nil, quietLog())
	if _, err := run(context.Background(), a, rpc.Request{Text: "q"}); err == nil {
		t.Fatal("Handle succeeded, want the model's error")
	}
	if rows := turns.recorded(); len(rows) != 0 {
		t.Errorf("got %d turn rows, want none", len(rows))
	}
}
