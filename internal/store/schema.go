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
// already run it. Later milestones add messages, tool_calls and memories
// this way.
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
