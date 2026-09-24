// This file holds the past-conversation tables (v0.4): sessions, messages,
// message_fts, summary_fts and session_vec. It replays the transcripts into
// them, lists the sessions that need a summary or a summary vector, and
// runs the three searches that recall past sessions. ARCHITECTURE.md, "The
// database", lists the tables.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/transcript"
)

// Session is one row of sessions: one transcript file.
type Session struct {
	ID string
	// Started is the time of the file's first line, and Last the time of
	// its newest question or answer.
	Started time.Time
	Last    time.Time
	// Turns counts the answers in the session.
	Turns int
	// Summary is the newest summary line's text, "" when the session has
	// none yet, and SummaryTime that line's time.
	Summary     string
	SummaryTime time.Time
}

// Message is one row of messages: a question or an answer.
type Message struct {
	ID      int64
	Session string
	Time    time.Time
	Role    string // "user" or "assistant"
	Text    string
	TraceID string
}

// SessionSummary is a session's ID with its summary text, for embedding.
type SessionSummary struct {
	Session string
	Summary string
}

// SessionHit is one result of a session search: the session, its rank in
// that result list (0 is the best), and, for a message search, the message
// that matched.
type SessionHit struct {
	Session   string
	MessageID int64 // 0 unless the hit came from SearchMessageKeyword
	Rank      int
}

// ReplaySessions brings sessions, messages and their indexes up to date
// with every transcript under sessionsDir, and returns how many lines it
// added. merud calls it at startup. Each file costs only the lines added
// since its last replay (see replayFile), so a second call reads nothing
// but each file's size.
//
// A missing sessionsDir means no sessions yet and returns 0. A file that
// can't be read fails the replay; files replayed before it stay.
func (s *Store) ReplaySessions(ctx context.Context, sessionsDir string) (int, error) {
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
		n, err := s.replayFile(ctx, strings.TrimSuffix(d.Name(), ".jsonl"), path)
		total += n
		return err
	})
	if err != nil {
		return total, fmt.Errorf("replay sessions: %w", err)
	}
	return total, nil
}

// ReplaySession brings the tables up to date with one session's transcript
// under sessionsDir and returns how many lines it added. merud calls it
// after each turn and each summary. It fails when id isn't a valid session
// ID, the file is missing or can't be read, or the database fails.
func (s *Store) ReplaySession(ctx context.Context, sessionsDir, id string) (int, error) {
	// transcript.Open checks the ID's form, so an ID can't name a file
	// outside sessionsDir.
	sess, err := transcript.Open(sessionsDir, id)
	if err != nil {
		return 0, fmt.Errorf("replay session: %w", err)
	}
	n, err := s.replayFile(ctx, id, sess.Path())
	if err != nil {
		return n, fmt.Errorf("replay session: %w", err)
	}
	return n, nil
}

// sessionRow is a sessions row as replayFile reads and writes it. Times
// stay as RFC 3339 text, the form the table holds.
type sessionRow struct {
	started, last, summary, summaryTS string
	turns                             int
	bytes                             int64
}

// replayFile adds the lines of one transcript that the tables don't hold
// yet, and returns how many it added.
//
// The rule that keeps it cheap: a transcript only grows, one whole line at
// a time, so the byte count in sessions.bytes marks where the last replay
// stopped. replayFile reads from there to the last complete line and
// stores the new count. A file of the same size is skipped after one
// os.Stat. A file smaller than the count was replaced or cut by hand, so
// replayFile forgets the session and reads it again from the start.
//
// User and assistant lines become messages. A summary line replaces the
// session's summary and drops its vector, so the summarizer embeds the new
// text. Tool lines stay out: tool_calls holds them.
//
// The read and the writes share one write transaction, so two replays of
// one file, such as the one after a turn and the one after a summary,
// take turns and never add a line twice.
func (s *Store) replayFile(ctx context.Context, id, path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", id, err)
	}
	size := info.Size()
	if size == 0 {
		return 0, nil // a new session with no line yet
	}
	// A cheap check outside the write lock: most files haven't changed.
	var done int64
	err = s.db.QueryRowContext(ctx, `SELECT bytes FROM sessions WHERE id = ?`, id).Scan(&done)
	if err == nil && done == size {
		return 0, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%s: %w", id, err)
	}

	added := 0
	err = s.write(ctx, func(tx *sql.Tx) error {
		row, found, err := loadSessionRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if found && row.bytes > size {
			if err := forgetSession(ctx, tx, id); err != nil {
				return err
			}
			row = sessionRow{}
		}
		lines, next, err := transcript.ReadFrom(path, row.bytes)
		if err != nil {
			return err
		}
		if next == row.bytes {
			return nil // another replay got here first, or only a partial line is new
		}

		newSummary := false
		for _, l := range lines {
			ts := l.TS.UTC().Format(time.RFC3339)
			if row.started == "" {
				row.started, row.last = ts, ts
			}
			switch l.Type {
			case transcript.TypeUser, transcript.TypeAssistant:
				row.last = ts
				if l.Type == transcript.TypeAssistant {
					row.turns++
				}
				if strings.TrimSpace(l.Text) == "" {
					continue
				}
				if err := insertMessage(ctx, tx, id, ts, l); err != nil {
					return err
				}
				added++
			case transcript.TypeSummary:
				row.summary, row.summaryTS = strings.TrimSpace(l.Text), ts
				newSummary = true
				added++
			}
		}
		if newSummary {
			if err := replaceSummary(ctx, tx, id, row.summary); err != nil {
				return err
			}
		}
		row.bytes = next
		// The upsert covers a new row and an old one alike.
		_, err = tx.ExecContext(ctx,
			`INSERT INTO sessions (id, started, last, turns, summary, summary_ts, path, bytes)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (id) DO UPDATE SET last = excluded.last, turns = excluded.turns,
			   summary = excluded.summary, summary_ts = excluded.summary_ts,
			   path = excluded.path, bytes = excluded.bytes`,
			id, row.started, row.last, row.turns, row.summary, row.summaryTS, path, row.bytes)
		if err != nil {
			return fmt.Errorf("write session: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("%s: %w", id, err)
	}
	return added, nil
}

// loadSessionRow reads a session's row inside tx. found is false when the
// session has no row yet.
func loadSessionRow(ctx context.Context, tx *sql.Tx, id string) (row sessionRow, found bool, err error) {
	err = tx.QueryRowContext(ctx,
		`SELECT started, last, turns, summary, summary_ts, bytes FROM sessions WHERE id = ?`, id).
		Scan(&row.started, &row.last, &row.turns, &row.summary, &row.summaryTS, &row.bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, false, nil
	}
	if err != nil {
		return sessionRow{}, false, fmt.Errorf("read session: %w", err)
	}
	return row, true, nil
}

// insertMessage writes one question or answer to messages and to
// message_fts, which can't see changes to messages by itself.
func insertMessage(ctx context.Context, tx *sql.Tx, session, ts string, l transcript.Line) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages (session, ts, role, text, trace_id) VALUES (?, ?, ?, ?, ?)`,
		session, ts, l.Type, l.Text, l.TraceID)
	if err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	msgID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_fts (rowid, text) VALUES (?, ?)`, msgID, l.Text); err != nil {
		return fmt.Errorf("index message: %w", err)
	}
	return nil
}

// replaceSummary puts a session's new summary into summary_fts and drops
// the vector of the old one.
func replaceSummary(ctx context.Context, tx *sql.Tx, session, summary string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM summary_fts WHERE session = ?`, session); err != nil {
		return fmt.Errorf("index summary: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO summary_fts (session, summary) VALUES (?, ?)`, session, summary); err != nil {
		return fmt.Errorf("index summary: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_vec WHERE session = ?`, session); err != nil {
		return fmt.Errorf("drop summary vector: %w", err)
	}
	return nil
}

// forgetSession deletes everything the tables hold about one session.
// message_fts is an external content table, so each entry leaves through
// FTS5's special 'delete' insert, which needs the text it indexed.
func forgetSession(ctx context.Context, tx *sql.Tx, session string) error {
	stmts := []string{
		`INSERT INTO message_fts (message_fts, rowid, text)
		 SELECT 'delete', id, text FROM messages WHERE session = ?1`,
		`DELETE FROM messages WHERE session = ?1`,
		`DELETE FROM summary_fts WHERE session = ?1`,
		`DELETE FROM session_vec WHERE session = ?1`,
		`DELETE FROM sessions WHERE id = ?1`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, session); err != nil {
			return fmt.Errorf("forget session: %w", err)
		}
	}
	return nil
}

// DueSummaries returns the IDs of up to limit sessions that need a new
// summary, newest first: each has at least one answer, no question or
// answer after quietSince, and no summary newer than its last question or
// answer. A session that grew after its summary comes back here once it
// goes quiet again. It fails when the database does.
//
// Comparing times as text works because every time is UTC in one format.
// An empty summary_ts sorts before any time.
func (s *Store) DueSummaries(ctx context.Context, quietSince time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM sessions
		 WHERE turns > 0 AND last <= ? AND summary_ts < last
		 ORDER BY last DESC, id DESC LIMIT ?`,
		quietSince.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("sessions due a summary: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sessions due a summary: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions due a summary: %w", err)
	}
	return ids, nil
}

// SummariesWithoutVector returns up to limit sessions, newest first, whose
// summary has no vector yet: new summaries, and every summary after a
// change of embedding model cleared session_vec. It fails when the
// database does.
func (s *Store) SummariesWithoutVector(ctx context.Context, limit int) ([]SessionSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.summary FROM sessions s
		 LEFT JOIN session_vec v ON v.session = s.id
		 WHERE s.summary != '' AND v.session IS NULL
		 ORDER BY s.last DESC, s.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("summaries without a vector: %w", err)
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.Session, &ss.Summary); err != nil {
			return nil, fmt.Errorf("summaries without a vector: %w", err)
		}
		out = append(out, ss)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("summaries without a vector: %w", err)
	}
	return out, nil
}

// SetSessionVector stores v as the vector of the session's summary, which
// must still read summary. A newer summary may have replaced it while the
// embedding ran; then SetSessionVector stores nothing, and the next pass
// embeds the new text. It fails when v has the wrong size or the database
// fails.
func (s *Store) SetSessionVector(ctx context.Context, session, summary string, v engine.Vector) error {
	if len(v) != s.dims {
		return fmt.Errorf("store session vector: %d dimensions, want %d", len(v), s.dims)
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO session_vec (session, vector)
			 SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM sessions WHERE id = ?1 AND summary = ?3)
			 ON CONFLICT (session) DO UPDATE SET vector = excluded.vector`,
			session, encodeVector(v), summary)
		if err != nil {
			return fmt.Errorf("store session vector: %w", err)
		}
		return nil
	})
}

// SearchSessionVector returns the k sessions whose summary vectors are
// nearest to v, nearest first, leaving out the session exclude. It measures
// distance as SearchVector does. It fails when v has the wrong size or the
// database fails.
func (s *Store) SearchSessionVector(ctx context.Context, v engine.Vector, exclude string, k int) ([]SessionHit, error) {
	if len(v) != s.dims {
		return nil, fmt.Errorf("session vector search: query has %d dimensions, want %d", len(v), s.dims)
	}
	hits, err := s.sessionHits(ctx, false,
		`SELECT session, 0 FROM session_vec WHERE session != ?3
		 ORDER BY vec1_l2_distance(vector, ?1), session LIMIT ?2`, encodeVector(v), k, exclude)
	if err != nil {
		return nil, fmt.Errorf("session vector search: %w", err)
	}
	return hits, nil
}

// SearchSummaryKeyword returns the k sessions whose summaries best match
// query under BM25, best first, leaving out the session exclude. query is
// plain text, escaped as SearchKeyword escapes it.
func (s *Store) SearchSummaryKeyword(ctx context.Context, query, exclude string, k int) ([]SessionHit, error) {
	match := ftsQuery(query)
	if match == "" || k <= 0 {
		return nil, nil
	}
	hits, err := s.sessionHits(ctx, false,
		`SELECT session, 0 FROM summary_fts WHERE summary_fts MATCH ?1 AND session != ?3
		 ORDER BY rank, session LIMIT ?2`, match, k, exclude)
	if err != nil {
		return nil, fmt.Errorf("summary keyword search: %w", err)
	}
	return hits, nil
}

// SearchMessageKeyword returns the k messages that best match query under
// BM25, best first, each with its session, leaving out the messages of the
// session exclude. One session can appear many times; retrieval groups
// them.
func (s *Store) SearchMessageKeyword(ctx context.Context, query, exclude string, k int) ([]SessionHit, error) {
	match := ftsQuery(query)
	if match == "" || k <= 0 {
		return nil, nil
	}
	// bm25() is FTS5's ranking function; rank is its shorthand on a query
	// of message_fts alone, but this one joins messages for the session.
	hits, err := s.sessionHits(ctx, true,
		`SELECT m.session, m.id FROM message_fts f JOIN messages m ON m.id = f.rowid
		 WHERE message_fts MATCH ?1 AND m.session != ?3
		 ORDER BY bm25(message_fts), m.id LIMIT ?2`, match, k, exclude)
	if err != nil {
		return nil, fmt.Errorf("message keyword search: %w", err)
	}
	return hits, nil
}

// sessionHits runs a query that returns (session, message ID) rows, best
// first, and numbers them from rank 0. withMessage says whether the second
// column holds a message ID worth keeping.
func (s *Store) sessionHits(ctx context.Context, withMessage bool, query string, args ...any) ([]SessionHit, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []SessionHit
	for rows.Next() {
		h := SessionHit{Rank: len(hits)}
		var msgID int64
		if err := rows.Scan(&h.Session, &msgID); err != nil {
			return nil, err
		}
		if withMessage {
			h.MessageID = msgID
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// Sessions loads the sessions with the given IDs, in the order of ids.
// IDs with no row are left out.
func (s *Store) Sessions(ctx context.Context, ids []string) ([]Session, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	idJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, started, last, turns, summary, summary_ts FROM sessions
		 WHERE id IN (SELECT value FROM json_each(?))`, string(idJSON))
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	defer rows.Close()
	byID := map[string]Session{}
	for rows.Next() {
		var ss Session
		var started, last, summaryTS string
		if err := rows.Scan(&ss.ID, &started, &last, &ss.Turns, &ss.Summary, &summaryTS); err != nil {
			return nil, fmt.Errorf("load sessions: %w", err)
		}
		ss.Started, ss.Last, ss.SummaryTime = parseTime(started), parseTime(last), parseTime(summaryTS)
		byID[ss.ID] = ss
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	out := make([]Session, 0, len(byID))
	for _, id := range ids {
		if ss, ok := byID[id]; ok {
			out = append(out, ss)
		}
	}
	return out, nil
}

// Messages loads the messages with the given IDs, in the order of ids.
// IDs with no row are left out.
func (s *Store) Messages(ctx context.Context, ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	idJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session, ts, role, text, trace_id FROM messages
		 WHERE id IN (SELECT value FROM json_each(?))`, string(idJSON))
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()
	byID := map[int64]Message{}
	for rows.Next() {
		var m Message
		var ts string
		if err := rows.Scan(&m.ID, &m.Session, &ts, &m.Role, &m.Text, &m.TraceID); err != nil {
			return nil, fmt.Errorf("load messages: %w", err)
		}
		m.Time = parseTime(ts)
		byID[m.ID] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	out := make([]Message, 0, len(byID))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// parseTime reads an RFC 3339 time from the tables, or returns the zero
// time for "" or text that doesn't parse.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
