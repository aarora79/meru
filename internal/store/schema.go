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
		// 1: documents, chunks and the keyword index over chunks.
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

// vectorModel is the vec1 setup chunk_vec gets when it's created: an exact
// "flat" index, which compares the query with every stored vector, and
// cosine distance, which measures the angle between two vectors (0 means
// the same direction, 2 the opposite). Embedding models are trained for
// cosine comparison. vec1 also offers approximate indexes; a personal index
// doesn't need one yet (ARCHITECTURE.md, "Why this driver and this vector
// store").
const vectorModel = `{index:"flat", distance:"cos"}`

// checkVectors makes sure chunk_vec exists and matches the embedding model
// and vector size. Vectors from two models don't compare, so when either
// changed, it drops chunk_vec with every vector in it and creates it empty.
// Documents and chunks stay, and NeedsReembed reports the gap.
func (s *Store) checkVectors(ctx context.Context, model string, dims int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var tables int
		err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'chunk_vec'`).Scan(&tables)
		if err != nil {
			return fmt.Errorf("look for chunk_vec: %w", err)
		}
		oldModel, err := getMeta(ctx, tx, "embed_model")
		if err != nil {
			return err
		}
		oldDims, err := getMeta(ctx, tx, "dims")
		if err != nil {
			return err
		}
		if tables == 1 && oldModel == model && oldDims == strconv.Itoa(dims) {
			return nil
		}

		// vec1 learns the vector size from the first row it stores; the
		// store checks sizes in Go before any vector reaches it.
		for _, stmt := range []string{
			`DROP TABLE IF EXISTS chunk_vec`,
			`CREATE VIRTUAL TABLE chunk_vec USING vec1(vector)`,
			`INSERT INTO chunk_vec(cmd, arg) VALUES ('rebuild', '` + vectorModel + `')`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("recreate chunk_vec: %w", err)
			}
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
