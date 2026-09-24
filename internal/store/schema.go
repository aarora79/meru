// This file holds the schema: the ordered list of migration steps that
// build the tables, and the check that keeps the vector index in step with
// the embedding model. ARCHITECTURE.md, "The database", lists every table.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// migrations returns the schema changes in order. Step i (from 0) takes the
// database from schema_version i to i+1. To change the schema, append a
// step; never edit one that has shipped, because existing databases have
// already run it. Later milestones add messages and memories this way, as
// v0.3 added tool_calls and turns.
//
// It is a function rather than a package-level variable so nothing can
// change the list at run time.
func migrations() []string {
	return []string{
		// 1: documents, chunks, their vectors and the keyword index.
		//
		// chunk_vec holds one vector per chunk as a blob of float32 values
		// (see encodeVector). It is a plain table with no search index:
		// SearchVector compares the query with every row. It sits apart
		// from chunks so a search reads only IDs and vectors, never text.
		//
		// chunk_fts is an FTS5 "external content" table: it indexes the
		// heading and text columns of chunks without keeping its own copy,
		// and its rowid is the chunk ID. FTS5 can't see changes to chunks by
		// itself, so ReplaceDocument and DeleteDocument update it in the
		// same transaction as the chunks.
		//
		// Times are RFC 3339 text rather than numbers, so a row reads well
		// in the sqlite3 shell.
		`CREATE TABLE documents (
			id    INTEGER PRIMARY KEY,
			path  TEXT NOT NULL UNIQUE,
			mtime TEXT NOT NULL,
			hash  TEXT NOT NULL,
			kind  TEXT NOT NULL
		);
		CREATE TABLE chunks (
			id         INTEGER PRIMARY KEY,
			doc_id     INTEGER NOT NULL REFERENCES documents(id),
			ordinal    INTEGER NOT NULL,
			heading    TEXT NOT NULL DEFAULT '',
			text       TEXT NOT NULL,
			start_line INTEGER NOT NULL DEFAULT 0,
			end_line   INTEGER NOT NULL DEFAULT 0,
			page       INTEGER NOT NULL DEFAULT 0,
			UNIQUE (doc_id, ordinal)
		);
		CREATE TABLE chunk_vec (
			chunk_id INTEGER PRIMARY KEY REFERENCES chunks(id),
			vector   BLOB NOT NULL
		);
		CREATE VIRTUAL TABLE chunk_fts USING fts5(
			heading, text, content='chunks', content_rowid='id'
		);`,

		// 2: tool_calls, the audit log of every tool call (v0.3).
		//
		// One row per call, written by dispatch and rebuilt from the
		// transcripts by ReplayToolCalls. call_id isn't unique: the model
		// may reuse an ID in a later turn. args holds JSON text and result
		// plain text, so a row reads well in the sqlite3 shell.
		`CREATE TABLE tool_calls (
			id          INTEGER PRIMARY KEY,
			call_id     TEXT NOT NULL DEFAULT '',
			session     TEXT NOT NULL DEFAULT '',
			ts          TEXT NOT NULL,
			kind        TEXT NOT NULL,
			server      TEXT NOT NULL DEFAULT '',
			tool        TEXT NOT NULL,
			args        TEXT NOT NULL DEFAULT '',
			result      TEXT NOT NULL DEFAULT '',
			outcome     TEXT NOT NULL,
			approval    TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			trace_id    TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX tool_calls_ts ON tool_calls (ts);
		CREATE INDEX tool_calls_session ON tool_calls (session);`,

		// 3: turns, one row per answered question (v0.3), for `meru usage`.
		//
		// The agent writes a row when a turn writes its answer, and
		// ReplayTurns rebuilds the table from the transcripts. ts is when
		// the question arrived. docs holds a JSON array of the absolute
		// paths whose excerpts went into the prompt, so SQLite's json_each
		// can count distinct files across rows.
		`CREATE TABLE turns (
			id          INTEGER PRIMARY KEY,
			session     TEXT NOT NULL DEFAULT '',
			ts          TEXT NOT NULL,
			source      TEXT NOT NULL DEFAULT '',
			route       TEXT NOT NULL DEFAULT '',
			tokens_in   INTEGER NOT NULL DEFAULT 0,
			tokens_out  INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			tool_calls  INTEGER NOT NULL DEFAULT 0,
			docs        TEXT NOT NULL DEFAULT '[]',
			trace_id    TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX turns_ts ON turns (ts);
		CREATE INDEX turns_session ON turns (session);`,

		// 4: sessions, messages and their search indexes (v0.4), so a turn
		// can recall past conversations. ReplaySessions fills them from
		// the transcripts; sessions.go explains how.
		//
		// sessions has one row per transcript file. summary is the newest
		// summary line's text, "" until merud writes one. path and bytes
		// are replay bookkeeping: the file, and how many of its bytes the
		// tables already hold, so a replay reads only the lines after them.
		//
		// messages holds the user and assistant lines. message_fts is an
		// external content table over its text, as chunk_fts is over chunks.
		// summary_fts keeps its own copy of each summary: one short line
		// per session, and sessions has a text key, which an external
		// content table can't use as its rowid.
		//
		// session_vec holds one vector per summary, stored like chunk_vec.
		`CREATE TABLE sessions (
			id         TEXT PRIMARY KEY,
			started    TEXT NOT NULL,
			last       TEXT NOT NULL,
			turns      INTEGER NOT NULL DEFAULT 0,
			summary    TEXT NOT NULL DEFAULT '',
			summary_ts TEXT NOT NULL DEFAULT '',
			path       TEXT NOT NULL DEFAULT '',
			bytes      INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX sessions_last ON sessions (last);
		CREATE TABLE messages (
			id       INTEGER PRIMARY KEY,
			session  TEXT NOT NULL,
			ts       TEXT NOT NULL,
			role     TEXT NOT NULL,
			text     TEXT NOT NULL,
			trace_id TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX messages_session ON messages (session);
		CREATE VIRTUAL TABLE message_fts USING fts5(
			text, content='messages', content_rowid='id'
		);
		CREATE VIRTUAL TABLE summary_fts USING fts5(session UNINDEXED, summary);
		CREATE TABLE session_vec (
			session TEXT PRIMARY KEY,
			vector  BLOB NOT NULL
		);`,
	}
}

// migrate brings the schema up to date. It creates the meta table if it's
// missing, reads schema_version from it (0 for a new file), and runs each
// step the database hasn't seen, one transaction per step. It fails when a
// step fails or the file comes from a newer Meru.
func (s *Store) migrate(ctx context.Context) error {
	// meta sits outside the steps because the steps' own bookkeeping lives
	// in it. IF NOT EXISTS makes this a no-op on every open but the first.
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
		return err
	}); err != nil {
		return fmt.Errorf("create meta: %w", err)
	}

	version, err := s.metaInt(ctx, "schema_version")
	if err != nil {
		return err
	}
	steps := migrations()
	if version > len(steps) {
		return fmt.Errorf("schema version %d is newer than this merud knows (%d); delete the file to rebuild it",
			version, len(steps))
	}
	// steps[version:] is a slice expression: the steps from index version
	// to the end. i counts from 0 within it.
	for i, step := range steps[version:] {
		next := version + i + 1
		err := s.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, step); err != nil {
				return err
			}
			return setMeta(ctx, tx, "schema_version", strconv.Itoa(next))
		})
		if err != nil {
			return fmt.Errorf("migrate to schema version %d: %w", next, err)
		}
	}
	return nil
}

// checkVectors makes sure the stored vectors came from model with dims
// numbers each. Vectors from two models don't compare, so when either
// changed since the last run, it deletes every vector. Documents and chunks
// stay, so keyword search still works, and NeedsReembed reports the gap.
func (s *Store) checkVectors(ctx context.Context, model string, dims int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		oldModel, err := getMeta(ctx, tx, "embed_model")
		if err != nil {
			return err
		}
		oldDims, err := getMeta(ctx, tx, "dims")
		if err != nil {
			return err
		}
		if oldModel == model && oldDims == strconv.Itoa(dims) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_vec`); err != nil {
			return fmt.Errorf("drop old vectors: %w", err)
		}
		// Session summaries lose their vectors too; the summarizer embeds
		// them again (store.SummariesWithoutVector).
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_vec`); err != nil {
			return fmt.Errorf("drop old session vectors: %w", err)
		}
		if err := setMeta(ctx, tx, "embed_model", model); err != nil {
			return err
		}
		return setMeta(ctx, tx, "dims", strconv.Itoa(dims))
	})
}

// NeedsReembed reports whether some chunks have no vector: after Open
// dropped the vectors for a new embedding model, until the indexer has
// stored every document again with ReplaceDocument. The indexer checks it
// at startup and, when it's true, re-embeds every file even if the file
// hasn't changed. Counting rows, rather than keeping a flag, keeps the
// answer right across a crash in the middle of re-embedding.
func (s *Store) NeedsReembed(ctx context.Context) (bool, error) {
	st, err := s.Stats(ctx)
	if err != nil {
		return false, err
	}
	return st.Vectors < st.Chunks, nil
}

// metaInt reads a meta value as an integer, or 0 when the key is missing.
func (s *Store) metaInt(ctx context.Context, key string) (int, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	// errors.Is compares against a known error value, even through wrapping.
	// sql.ErrNoRows means the query matched nothing.
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read meta %s: %w", key, err)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("read meta %s: %w", key, err)
	}
	return n, nil
}

// getMeta reads a meta value inside tx, or "" when the key is missing.
func getMeta(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read meta %s: %w", key, err)
	}
	return v, nil
}

// setMeta writes a meta value inside tx, replacing any old one. "ON
// CONFLICT … DO UPDATE" is SQLite's upsert: insert, or update the row that
// already has this key.
func setMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write meta %s: %w", key, err)
	}
	return nil
}
