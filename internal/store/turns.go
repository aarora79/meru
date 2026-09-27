// This file holds the turns table: the row type, writing one row,
// rebuilding the table from the session transcripts, and adding up the rows
// over the usage windows that `meru usage` and the chat header show.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// Turn is one row of turns: one answered question. A turn that failed or
// was cancelled wrote no answer line, so it has no row.
type Turn struct {
	// ID is the row's own number; InsertTurn ignores it.
	ID      int64
	Session string
	// Time is when the question arrived.
	Time time.Time
	// Source is "cli", "tui", "job" or "desktop", and "" on a row rebuilt
	// from a transcript, which doesn't record it. Route is the route the
	// turn took, "" on a row from a transcript older than the field.
	Source string
	Route  string
	// TokensIn and TokensOut sum the main model's tokens over the turn's
	// model calls.
	TokensIn       int64
	TokensOut      int64
	DurationMillis int64
	// ToolCalls counts the tool calls the turn made, however they ended.
	ToolCalls int
	// Docs lists the absolute paths of the files whose excerpts went into
	// the prompt, each once.
	Docs    []string
	TraceID string
	// Model is the main model that wrote the answer, "" when the
	// transcript doesn't say. TTFTMillis is its time to first token on the
	// round that first wrote text, EvalMillis the time Ollama spent writing
	// tokens over the turn, BadCalls the tool calls it wrote that Meru
	// couldn't run as written, and Capped whether the turn used every
	// round and wrote no answer.
	Model      string
	TTFTMillis int64
	EvalMillis int64
	BadCalls   int
	Capped     bool
}

// InsertTurn writes one row to turns through the store's one write path.
// It fails when the database does.
func (s *Store) InsertTurn(ctx context.Context, t Turn) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		return insertTurn(ctx, tx, t)
	})
}

// insertTurn writes t inside tx. The time goes in as RFC 3339 text in UTC,
// to the second, as the transcript writes it, so live rows and replayed
// rows look the same and sort as text.
func insertTurn(ctx context.Context, tx *sql.Tx, t Turn) error {
	docs := t.Docs
	if docs == nil {
		docs = []string{} // "[]" rather than "null", so json_each reads it
	}
	b, err := json.Marshal(docs)
	if err != nil {
		return fmt.Errorf("write turn: docs: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO turns
		 (session, ts, source, route, tokens_in, tokens_out, duration_ms, tool_calls, docs, trace_id,
		  model, ttft_ms, eval_ms, bad_calls, capped)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Session, t.Time.UTC().Format(time.RFC3339), t.Source, t.Route, t.TokensIn, t.TokensOut,
		t.DurationMillis, t.ToolCalls, string(b), t.TraceID,
		t.Model, t.TTFTMillis, t.EvalMillis, t.BadCalls, t.Capped)
	if err != nil {
		return fmt.Errorf("write turn: %w", err)
	}
	return nil
}

// ReplayTurns rebuilds turns from the session transcripts under
// sessionsDir and returns how many rows it wrote. Like ReplayToolCalls, it
// does nothing when the table already holds rows, so merud can call it on
// every start.
//
// A missing sessionsDir means no sessions yet and returns 0. A file that
// can't be read fails the replay; rows from files before it stay.
func (s *Store) ReplayTurns(ctx context.Context, sessionsDir string) (int, error) {
	var hasRows bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM turns)`).Scan(&hasRows); err != nil {
		return 0, fmt.Errorf("replay turns: %w", err)
	}
	if hasRows {
		return 0, nil
	}

	total := 0
	err := filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == sessionsDir && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		// Only regular .jsonl files, and never through a symlink.
		if !d.Type().IsRegular() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		lines, err := transcript.ReadLines(path)
		if err != nil {
			return err
		}
		turns := turnsOf(strings.TrimSuffix(d.Name(), ".jsonl"), lines)
		if len(turns) == 0 {
			return nil
		}
		// One transaction per session file keeps each write short.
		err = s.write(ctx, func(tx *sql.Tx) error {
			for _, t := range turns {
				if err := insertTurn(ctx, tx, t); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		total += len(turns)
		return nil
	})
	if err != nil {
		return total, fmt.Errorf("replay turns: %w", err)
	}
	return total, nil
}

// turnsOf turns one session's lines into rows, one per assistant line. A
// row's time is the user line before the answer, and its tool calls are
// the tool_call lines between the two. Its model is the To of the last
// main-tier model_switch line before the answer, so a session that
// switched models splits at the switch. The rest comes from the assistant
// line itself. Lines written before v0.3 have no route, duration or
// sources, and lines written before model_switch have no model or model
// numbers, so those fields stay empty.
func turnsOf(session string, lines []transcript.Line) []Turn {
	var turns []Turn
	var asked time.Time // the open question's time; zero when none is open
	calls := 0
	model := ""
	for _, l := range lines {
		switch l.Type {
		case transcript.TypeUser:
			// A later question replaces one that got no answer.
			asked, calls = l.TS, 0
		case transcript.TypeToolCall:
			calls++
		case transcript.TypeModelSwitch:
			if l.Tier == "main" {
				model = l.To
			}
		case transcript.TypeAssistant:
			ts := asked
			if ts.IsZero() {
				ts = l.TS // an answer with no question line before it
			}
			turns = append(turns, Turn{
				Session: session, Time: ts, Route: l.Route,
				TokensIn: int64(l.TokensIn), TokensOut: int64(l.TokensOut),
				DurationMillis: l.Ms, ToolCalls: calls, Docs: l.Sources, TraceID: l.TraceID,
				Model: model, TTFTMillis: l.TTFTMs, EvalMillis: l.EvalMs, BadCalls: l.BadCalls, Capped: l.Capped,
			})
			asked, calls = time.Time{}, 0
		}
	}
	return turns
}

// windowStarts returns the start of each usage window, in the order of the
// rpc.Usage* constants, measured in now's location. The zero time.Time
// stands for "all", which has no start.
//
// time.Date normalizes out-of-range days, so day-6 on the 3rd lands in the
// month before. It also keeps midnight at midnight across a daylight-saving
// change, which subtracting 24-hour days would not.
func windowStarts(now time.Time) []time.Time {
	y, m, d := now.Date()
	loc := now.Location()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	// Weekday counts from Sunday = 0; (w+6)%7 counts days since Monday.
	sinceMonday := (int(now.Weekday()) + 6) % 7
	return []time.Time{
		now.Add(-time.Hour),
		today,
		time.Date(y, m, d-sinceMonday, 0, 0, 0, 0, loc),
		time.Date(y, m, 1, 0, 0, 0, 0, loc),
		now.Add(-30 * 24 * time.Hour),
		{},
	}
}

// windowNames lists the usage windows in the order a "usage" event gives
// them, matching windowStarts.
func windowNames() []string {
	return []string{rpc.Usage1h, rpc.UsageToday, rpc.UsageWeek, rpc.UsageMonth, rpc.Usage30d, rpc.UsageLifetime}
}

// usageSince adds up the turns from one start time on, and usageAll adds
// up every turn. The two differ in where the session count sits, so that
// SQLite reads the fewest rows for each (see BenchmarkUsage):
//
//   - In usageSince, every count reads only the rows the ts index finds.
//     As a subquery of its own, COUNT(DISTINCT session) would read the
//     whole session index instead, even for the last hour.
//   - In usageAll, every row counts. A plain read of the table sums the
//     numbers fastest, and COUNT(DISTINCT session) fastest from the
//     session index on its own, so it moves into a subquery.
//
// The docs count reads each row's JSON array with json_each, which turns
// the array into one row per path. ts compares as text, which works
// because every ts is UTC in the same format.
const (
	usageSince = `SELECT
		COUNT(DISTINCT session), COUNT(*),
		COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0),
		COALESCE(SUM(duration_ms), 0), COALESCE(SUM(tool_calls), 0),
		(SELECT COUNT(DISTINCT j.value) FROM turns AS t, json_each(t.docs) AS j WHERE t.ts >= ?1)
		FROM turns WHERE ts >= ?1`
	usageAll = `SELECT
		(SELECT COUNT(DISTINCT session) FROM turns),
		COUNT(*), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0),
		COALESCE(SUM(duration_ms), 0), COALESCE(SUM(tool_calls), 0),
		(SELECT COUNT(DISTINCT j.value) FROM turns AS t, json_each(t.docs) AS j)
		FROM turns`
)

// Usage adds up the turns table over the six usage windows and returns
// them in the order of the rpc.Usage* constants. Today, week and month
// follow now's location: today starts at midnight, the week on Monday and
// the month on the 1st. 1h and 30d roll back from now. It runs one query
// per window and fails when the database does. Over 50,000 turns it takes
// about 46 ms on an Apple M4 Max, most of it in the "all" window's count of
// distinct files (BenchmarkUsage).
func (s *Store) Usage(ctx context.Context, now time.Time) ([]rpc.UsageWindow, error) {
	names := windowNames()
	out := make([]rpc.UsageWindow, len(names))
	for i, start := range windowStarts(now) {
		w := rpc.UsageWindow{Name: names[i]}
		// The zero start stands for "all", which has no start.
		var row *sql.Row
		if start.IsZero() {
			row = s.db.QueryRowContext(ctx, usageAll)
		} else {
			w.Since = start.Format(time.RFC3339)
			row = s.db.QueryRowContext(ctx, usageSince, start.UTC().Format(time.RFC3339))
		}
		err := row.Scan(&w.Sessions, &w.Turns,
			&w.TokensIn, &w.TokensOut, &w.ActiveMillis, &w.ToolCalls, &w.Docs)
		if err != nil {
			return nil, fmt.Errorf("usage %s: %w", w.Name, err)
		}
		out[i] = w
	}
	return out, nil
}

// usageByModel adds up every turn per answer model. The median time to
// first token needs each turn's value, which SQL has no function for, so
// UsageByModel reads those with ttftByModel and works it out in Go.
const (
	usageByModel = `SELECT model, COUNT(DISTINCT session), COUNT(*),
		COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0),
		COALESCE(SUM(duration_ms), 0), COALESCE(SUM(tool_calls), 0),
		COALESCE(SUM(eval_ms), 0), COALESCE(SUM(bad_calls), 0), COALESCE(SUM(capped), 0)
		FROM turns GROUP BY model ORDER BY COUNT(*) DESC, model`
	ttftByModel = `SELECT model, ttft_ms FROM turns WHERE ttft_ms > 0 ORDER BY model, ttft_ms`
)

// UsageByModel adds up the turns table per answer model, over all time,
// and returns one window per model, the model with the most turns first.
// Each window's Name is rpc.UsageLifetime and its Model the model; turns
// from before the transcripts named the model come under Model "". Docs
// stays 0: the files a model read say nothing about the model.
//
// It fails when the database does.
func (s *Store) UsageByModel(ctx context.Context) ([]rpc.UsageWindow, error) {
	rows, err := s.db.QueryContext(ctx, usageByModel)
	if err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}
	defer rows.Close()
	var out []rpc.UsageWindow
	for rows.Next() {
		w := rpc.UsageWindow{Name: rpc.UsageLifetime}
		if err := rows.Scan(&w.Model, &w.Sessions, &w.Turns, &w.TokensIn, &w.TokensOut,
			&w.ActiveMillis, &w.ToolCalls, &w.EvalMillis, &w.BadCalls, &w.Capped); err != nil {
			return nil, fmt.Errorf("usage by model: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}

	medians, err := s.ttftMedians(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].TTFTp50Millis = medians[out[i].Model]
	}
	return out, nil
}

// ttftMedians returns the median time to first token of each model's
// turns, leaving out turns that wrote no text. With an even count it takes
// the lower of the two middle values, so the number is always one a turn
// really took.
func (s *Store) ttftMedians(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, ttftByModel)
	if err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}
	defer rows.Close()
	// byModel holds each model's values in rising order, as the query
	// sorts them.
	byModel := map[string][]int64{}
	for rows.Next() {
		var model string
		var ms int64
		if err := rows.Scan(&model, &ms); err != nil {
			return nil, fmt.Errorf("usage by model: %w", err)
		}
		byModel[model] = append(byModel[model], ms)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage by model: %w", err)
	}
	medians := map[string]int64{}
	for model, v := range byModel {
		medians[model] = v[(len(v)-1)/2]
	}
	return medians, nil
}
