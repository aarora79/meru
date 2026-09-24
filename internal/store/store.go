// This file defines the store's API: the types and methods the indexer,
// retrieval and the agent share. store.go holds signatures only; the SQL
// lives in the other files of this package.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// ErrNotImplemented is what the skeleton's methods return until the store
// agent fills them in.
var ErrNotImplemented = errors.New("store: not implemented yet")

// Store is Meru's one SQLite file, ~/.meru/meru.db: an index over your files
// that merud can always rebuild from them (ARCHITECTURE.md, "Storage").
// Methods are safe to call from several goroutines.
type Store struct {
	// Fields are private and set by Open; see the implementation files.
	impl any
}

// Options says how to open the store.
type Options struct {
	// Path is the database file, usually ~/.meru/meru.db.
	Path string
	// EmbedModel and Dims name the embedding model and its vector size. If
	// they differ from what the database was built with, Open drops every
	// vector so the indexer re-embeds all chunks.
	EmbedModel string
	Dims       int
}

// Open opens or creates the database at opts.Path, creates or migrates the
// schema, and checks the embedding model (see Options). The file is created
// with mode 0600.
func Open(ctx context.Context, opts Options) (*Store, error) {
	return nil, ErrNotImplemented
}

// Close closes the database.
func (s *Store) Close() error { return ErrNotImplemented }

// Document is one indexed file.
type Document struct {
	ID    int64
	Path  string    // absolute path on disk
	MTime time.Time // modification time when it was indexed
	Hash  string    // hex SHA-256 of the file's bytes
	Kind  string    // "markdown", "text", "code", "pdf" or "html"
}

// Chunk is one piece of a document's text, the unit that search returns.
type Chunk struct {
	ID      int64
	DocID   int64
	Ordinal int    // position within the document, from 0
	Heading string // the heading or symbol the chunk sits under, if any
	Text    string
	// StartLine and EndLine locate the chunk in text files (1-based); Page
	// locates it in a PDF (1-based). Unused fields are 0.
	StartLine int
	EndLine   int
	Page      int
}

// Document looks up the document indexed at path. ok is false when the path
// isn't in the index.
func (s *Store) Document(ctx context.Context, path string) (doc Document, ok bool, err error) {
	return Document{}, false, ErrNotImplemented
}

// ReplaceDocument stores doc with its chunks and their vectors in one
// transaction, replacing whatever the index held for doc.Path. vecs has one
// vector per chunk, in the same order.
func (s *Store) ReplaceDocument(ctx context.Context, doc Document, chunks []Chunk, vecs []engine.Vector) error {
	return ErrNotImplemented
}

// DeleteDocument removes path and its chunks from the index. Deleting a path
// that isn't indexed is not an error.
func (s *Store) DeleteDocument(ctx context.Context, path string) error {
	return ErrNotImplemented
}

// Paths lists every indexed path under prefix (all paths when prefix is
// empty), so the indexer can find files that were deleted from disk.
func (s *Store) Paths(ctx context.Context, prefix string) ([]string, error) {
	return nil, ErrNotImplemented
}

// Hit is one search result: a chunk and its rank in one result list.
type Hit struct {
	ChunkID int64
	Rank    int     // 0 is the best match
	Score   float64 // BM25 score or vector distance; lower is better for both
}

// SearchVector returns the k chunks whose vectors are nearest to v.
func (s *Store) SearchVector(ctx context.Context, v engine.Vector, k int) ([]Hit, error) {
	return nil, ErrNotImplemented
}

// SearchKeyword returns the k chunks that best match query under BM25. query
// is plain text; the store escapes it for FTS5.
func (s *Store) SearchKeyword(ctx context.Context, query string, k int) ([]Hit, error) {
	return nil, ErrNotImplemented
}

// ChunkWithDoc is a chunk together with the document it came from, for
// showing results and citing them.
type ChunkWithDoc struct {
	Chunk
	Path string
	Kind string
}

// Chunks loads the chunks with the given IDs, in the order of ids.
func (s *Store) Chunks(ctx context.Context, ids []int64) ([]ChunkWithDoc, error) {
	return nil, ErrNotImplemented
}

// Stats reports how much the index holds.
type Stats struct {
	Documents int
	Chunks    int
}

// Stats counts documents and chunks.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	return Stats{}, ErrNotImplemented
}
