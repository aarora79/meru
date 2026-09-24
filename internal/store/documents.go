// This file holds the methods that read and write documents and their
// chunks: Document, ReplaceDocument, DeleteDocument, Paths and Stats. Every
// write keeps chunks, the keyword index (chunk_fts) and the vector index
// (chunk_vec) in step inside one transaction.

package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// Document looks up the document indexed at path. ok is false when the path
// isn't in the index.
func (s *Store) Document(ctx context.Context, path string) (doc Document, ok bool, err error) {
	var mtime string
	err = s.db.QueryRowContext(ctx,
		`SELECT id, path, mtime, hash, kind FROM documents WHERE path = ?`, path).
		Scan(&doc.ID, &doc.Path, &mtime, &doc.Hash, &doc.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, fmt.Errorf("look up document %s: %w", path, err)
	}
	doc.MTime, err = time.Parse(time.RFC3339Nano, mtime)
	if err != nil {
		return Document{}, false, fmt.Errorf("look up document %s: mtime: %w", path, err)
	}
	return doc, true, nil
}

// ReplaceDocument stores doc with its chunks and their vectors in one
// transaction, replacing whatever the index held for doc.Path. vecs has one
// vector per chunk, in the same order.
//
// The store assigns IDs: it ignores doc.ID and each chunk's ID and DocID,
// and sets each chunk's Ordinal to its position in chunks. The document
// keeps its ID across replacements; its chunks get new ones. It fails, and
// changes nothing, when the counts or a vector's size are wrong.
func (s *Store) ReplaceDocument(ctx context.Context, doc Document, chunks []Chunk, vecs []engine.Vector) error {
	if doc.Path == "" {
		return errors.New("replace document: empty path")
	}
	if len(vecs) != len(chunks) {
		return fmt.Errorf("replace document %s: %d chunks but %d vectors", doc.Path, len(chunks), len(vecs))
	}
	for i, v := range vecs {
		if len(v) != s.dims {
			return fmt.Errorf("replace document %s: vector %d has %d dimensions, want %d", doc.Path, i, len(v), s.dims)
		}
	}

	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := deleteChunks(ctx, tx, doc.Path); err != nil {
			return err
		}
		// Upsert the document and get its ID back. RETURNING hands back
		// columns of the row the statement wrote.
		var docID int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO documents (path, mtime, hash, kind) VALUES (?, ?, ?, ?)
			 ON CONFLICT (path) DO UPDATE SET mtime = excluded.mtime, hash = excluded.hash, kind = excluded.kind
			 RETURNING id`,
			doc.Path, doc.MTime.UTC().Format(time.RFC3339Nano), doc.Hash, doc.Kind).Scan(&docID)
		if err != nil {
			return fmt.Errorf("write document: %w", err)
		}
		for i, c := range chunks {
			if err := insertChunk(ctx, tx, docID, i, c, vecs[i]); err != nil {
				return fmt.Errorf("write chunk %d: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace document %s: %w", doc.Path, err)
	}
	return nil
}

// insertChunk writes one chunk, its keyword index row and its vector, all
// under the chunk's new ID.
func insertChunk(ctx context.Context, tx *sql.Tx, docID int64, ordinal int, c Chunk, v engine.Vector) error {
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO chunks (doc_id, ordinal, heading, text, start_line, end_line, page)
		 VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		docID, ordinal, c.Heading, c.Text, c.StartLine, c.EndLine, c.Page).Scan(&id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunk_fts (rowid, heading, text) VALUES (?, ?, ?)`, id, c.Heading, c.Text); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO chunk_vec (chunk_id, vector) VALUES (?, ?)`, id, encodeVector(v))
	return err
}

// DeleteDocument removes path and its chunks from the index. Deleting a path
// that isn't indexed is not an error.
func (s *Store) DeleteDocument(ctx context.Context, path string) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := deleteChunks(ctx, tx, path); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE path = ?`, path)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete document %s: %w", path, err)
	}
	return nil
}

// deleteChunks removes every chunk of the document at path, with its
// keyword index rows and vectors. It leaves the documents row alone.
//
// The order matters. An external-content FTS5 table forgets a row only
// when told the exact text it indexed, through the special 'delete'
// command, so those rows go first, while chunks still holds the text.
func deleteChunks(ctx context.Context, tx *sql.Tx, path string) error {
	const ofDoc = `SELECT c.id FROM chunks c JOIN documents d ON d.id = c.doc_id WHERE d.path = ?`
	stmts := []string{
		`INSERT INTO chunk_fts (chunk_fts, rowid, heading, text)
		 SELECT 'delete', c.id, c.heading, c.text
		 FROM chunks c JOIN documents d ON d.id = c.doc_id WHERE d.path = ?`,
		`DELETE FROM chunk_vec WHERE chunk_id IN (` + ofDoc + `)`,
		`DELETE FROM chunks WHERE id IN (` + ofDoc + `)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt, path); err != nil {
			return fmt.Errorf("delete old chunks: %w", err)
		}
	}
	return nil
}

// Paths lists every indexed path under prefix, sorted, so the indexer can
// find files that were deleted from disk. prefix names a folder: "/notes"
// matches "/notes/a.md" but not "/notes2/b.md". An empty prefix lists every
// path.
func (s *Store) Paths(ctx context.Context, prefix string) ([]string, error) {
	if prefix != "" && !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	// substr and length count characters on both sides, so this compares
	// the first len(prefix) characters of path with prefix. Unlike LIKE, it
	// gives no special meaning to "%" or "_" in a folder name.
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM documents WHERE substr(path, 1, length(?1)) = ?1 ORDER BY path`, prefix)
	if err != nil {
		return nil, fmt.Errorf("list paths under %q: %w", prefix, err)
	}
	// rows holds a connection until closed; defer returns it to the pool.
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("list paths under %q: %w", prefix, err)
		}
		paths = append(paths, p)
	}
	// rows.Err reports an error that ended the loop early.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list paths under %q: %w", prefix, err)
	}
	return paths, nil
}

// Stats counts documents, chunks and vectors.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM documents), (SELECT count(*) FROM chunks), (SELECT count(*) FROM chunk_vec)`).
		Scan(&st.Documents, &st.Chunks, &st.Vectors)
	if err != nil {
		return Stats{}, fmt.Errorf("count index: %w", err)
	}
	return st, nil
}

// encodeVector scales v to length 1 and packs it into the blob chunk_vec
// stores and vec1's distance functions read: 4 bytes per number, each an
// IEEE 754 float32, least significant byte first. vec1 wants the machine's
// byte order, and the machine here is SQLite's WebAssembly, which is
// little-endian on every host.
//
// Scaling to length 1 keeps the direction, which is all cosine distance
// looks at, and lets SearchVector use the cheaper L2 distance to get the
// same answer. A vector of all zeros has no direction and stays as it is.
func encodeVector(v engine.Vector) []byte {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	scale := 1.0
	if sum > 0 {
		scale = 1 / math.Sqrt(sum)
	}
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(float32(float64(f)*scale)))
	}
	return b
}
