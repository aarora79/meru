// This file holds the tool_calls audit log: the row type, writing one row,
// reading the newest rows for `meru log`, and rebuilding the table from the
// session transcripts. ARCHITECTURE.md, "The database", lists the table.

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
	"unicode/utf8"

	"github.com/aarora79/meru/internal/transcript"
)

// MaxToolResult caps the result text one tool_calls row keeps, in
// characters. dispatch already cuts the transcript copy to this length; the
// store cuts again so a row stays readable whoever writes it.
const MaxToolResult = 4000

// ToolCall is one row of tool_calls: one tool call, however it ended.
type ToolCall struct {
	// ID is the row's own number; InsertToolCall ignores it.
	ID int64
	// CallID ties the row to the call's lines in the transcript.
	CallID  string
	Session string
	// Time is when dispatch received the call.
	Time time.Time
	// Kind is "mcp", "a2a" or "builtin". Server is the MCP server or A2A
	// agent ("meru" for a built-in), and Tool the name without the server.
	Kind   string
	Server string
	Tool   string
	// Args are the call's arguments as JSON, secrets redacted. Result is
	// what the tool returned, secrets redacted, cut to MaxToolResult.
	Args   json.RawMessage
	Result string
	// Outcome is "ok", "error", "denied", "declined", "cancelled" or
	// "timeout". Approval is the user's choice ("once", "session" or
	// "deny"), or "" when nobody was asked.
	Outcome        string
	Approval       string
	DurationMillis int64
	TraceID        string
	// Caller is "meru" for a call merud made on its own before the
	// model's first round, and "" for a call the model asked for.
	Caller string
}

// InsertToolCall writes one row to tool_calls through the store's one
// write path. It fails when the database does.
func (s *Store) InsertToolCall(ctx context.Context, c ToolCall) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		return insertToolCall(ctx, tx, c)
	})
}

// insertToolCall writes c inside tx. Times go in as RFC 3339 text in UTC,
// to the second, as the transcript writes them, so live rows and replayed
// rows look the same.
func insertToolCall(ctx context.Context, tx *sql.Tx, c ToolCall) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO tool_calls
		 (call_id, session, ts, kind, server, tool, args, result, outcome, approval, duration_ms, trace_id, caller)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CallID, c.Session, c.Time.UTC().Format(time.RFC3339), c.Kind, c.Server, c.Tool,
		string(c.Args), capText(c.Result, MaxToolResult), c.Outcome, c.Approval, c.DurationMillis, c.TraceID, c.Caller)
	if err != nil {
		return fmt.Errorf("write tool call %s: %w", c.CallID, err)
	}
	return nil
}

// ToolCalls returns the newest limit rows of tool_calls, newest first. A
// limit of zero or less returns every row. Rows from the same second come
// back in reverse order of writing.
func (s *Store) ToolCalls(ctx context.Context, limit int) ([]ToolCall, error) {
	if limit <= 0 {
		limit = -1 // SQLite reads a negative LIMIT as "no limit"
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, call_id, session, ts, kind, server, tool, args, result, outcome, approval, duration_ms, trace_id, caller
		 FROM tool_calls ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read tool calls: %w", err)
	}
	defer rows.Close()

	var out []ToolCall
	for rows.Next() {
		var c ToolCall
		var ts, args string
		if err := rows.Scan(&c.ID, &c.CallID, &c.Session, &ts, &c.Kind, &c.Server, &c.Tool,
			&args, &c.Result, &c.Outcome, &c.Approval, &c.DurationMillis, &c.TraceID, &c.Caller); err != nil {
			return nil, fmt.Errorf("read tool calls: %w", err)
		}
		c.Time, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("read tool call %d: time: %w", c.ID, err)
		}
		if args != "" {
			c.Args = json.RawMessage(args)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tool calls: %w", err)
	}
	return out, nil
}

// ReplayToolCalls rebuilds tool_calls from the session transcripts under
// sessionsDir (sessions/YYYY/MM/<id>.jsonl) and returns how many rows it
// wrote. It does nothing when tool_calls already holds rows, so merud can
// call it on every start: it fills the table only after meru.db was deleted
// or came from a Meru without tool_calls.
//
// A missing sessionsDir means no sessions yet and returns 0. A file that
// can't be read fails the replay; rows from files before it stay.
func (s *Store) ReplayToolCalls(ctx context.Context, sessionsDir string) (int, error) {
	var hasRows bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tool_calls)`).Scan(&hasRows); err != nil {
		return 0, fmt.Errorf("replay tool calls: %w", err)
	}
	if hasRows {
		return 0, nil
	}

	total := 0
	// WalkDir calls the function once for each file and folder under
	// sessionsDir, in lexical order, so older sessions go in first.
	err := filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == sessionsDir && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		// Only regular .jsonl files. d.Type() is the kind of entry without
		// following a symlink, so a link never leads the walk elsewhere.
		if !d.Type().IsRegular() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		lines, err := transcript.ReadLines(path)
		if err != nil {
			return err
		}
		calls := pairToolLines(strings.TrimSuffix(d.Name(), ".jsonl"), lines)
		if len(calls) == 0 {
			return nil
		}
		// One transaction per session file keeps each write short.
		err = s.write(ctx, func(tx *sql.Tx) error {
			for _, c := range calls {
				if err := insertToolCall(ctx, tx, c); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		total += len(calls)
		return nil
	})
	if err != nil {
		return total, fmt.Errorf("replay tool calls: %w", err)
	}
	return total, nil
}

// pairToolLines turns one session's tool lines into rows. A call writes a
// tool_call line, maybe an approval line, then a tool_result line, all with
// the same call_id; the lines of calls that ran side by side interleave.
//
// A call ID may come back in a later turn, so pairing tracks open calls: a
// tool_call line opens one, and its tool_result line closes it. A call that
// never closed (merud stopped mid-call) gets the outcome "cancelled". Rows
// come back in the order their calls opened.
func pairToolLines(session string, lines []transcript.Line) []ToolCall {
	var calls []ToolCall
	open := map[string]int{} // call_id → index in calls, while the call is open
	for _, l := range lines {
		if l.CallID == "" {
			continue
		}
		switch l.Type {
		case transcript.TypeToolCall:
			calls = append(calls, ToolCall{
				CallID:  l.CallID,
				Session: session,
				Time:    l.TS,
				Kind:    l.Kind,
				Server:  l.Server,
				Tool:    l.Tool,
				Args:    l.Args,
				Outcome: "cancelled", // until a tool_result line says otherwise
				TraceID: l.TraceID,
				Caller:  l.Caller,
			})
			open[l.CallID] = len(calls) - 1
		case transcript.TypeApproval:
			if i, ok := open[l.CallID]; ok {
				calls[i].Approval = l.Choice
			}
		case transcript.TypeToolResult:
			i, ok := open[l.CallID]
			if !ok {
				continue // a result with no call line: nothing to pair it with
			}
			calls[i].Outcome = l.Outcome
			calls[i].DurationMillis = l.Ms
			calls[i].Result = l.Result
			if calls[i].TraceID == "" {
				calls[i].TraceID = l.TraceID
			}
			delete(open, l.CallID)
		}
	}
	return calls
}

// capText cuts s to at most n characters (runes, so a multi-byte character
// is never split).
func capText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
