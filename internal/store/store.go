// This file defines the store's API: the Store type, the types the indexer,
// retrieval and the agent share, and Open and Close. The SQL behind the
// other methods lives in schema.go, documents.go and search.go.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	"github.com/ncruces/go-sqlite3/ext/vec1"
)

// maxConns caps how many SQLite connections the pool opens. Each connection
// is a separate copy of SQLite's WebAssembly memory, a few megabytes, so the
// cap stays small. Eight lets several readers run while one write is open.
const maxConns = 8

// Store is Meru's one SQLite file, ~/.meru/meru.db: an index over your files
// that merud can always rebuild from them (ARCHITECTURE.md, "Storage").
// Methods are safe to call from several goroutines.
//
// Readers run side by side: the database is in WAL (write-ahead log) mode,
// where a reader sees the last committed state and never waits for a writer.
// Writers take turns through writeMu, so two writes in this process never
// race for SQLite's single write lock.
type Store struct {
	// db is database/sql's pool of connections. *sql.DB is safe to share
	// between goroutines; it hands each query a free connection.
	db *sql.DB
	// dims is the vector size every stored and searched vector must have.
	dims int

	// writeMu serializes write transactions. A sync.Mutex is a lock: Lock
	// waits until no other goroutine holds it.
	writeMu sync.Mutex
}

// Options says how to open the store.
type Options struct {
	// Path is the database file, usually ~/.meru/meru.db.
	Path string
	// EmbedModel and Dims name the embedding model and its vector size. If
	// they differ from what the database was built with, Open drops every
	// vector so the indexer re-embeds all chunks. NeedsReembed reports it.
	EmbedModel string
	Dims       int
}

// Open opens or creates the database at opts.Path, creates or migrates the
// schema, and checks the embedding model (see Options). The database file
// and its -wal and -shm companions get mode 0600, so only you can read them.
//
// When the embedding model or vector size changed since the last run, Open
// drops the vector index and keeps documents and chunks, so keyword search
// still works. NeedsReembed then reports true until the indexer has stored
// a vector for every chunk again.
//
// It fails when opts is incomplete, the folder doesn't exist, or the file
// isn't a Meru database this version understands.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" || opts.EmbedModel == "" || opts.Dims <= 0 {
		return nil, fmt.Errorf("open store: need a path, an embedding model and a vector size, got %q, %q, %d",
			opts.Path, opts.EmbedModel, opts.Dims)
	}
	if err := createPrivate(opts.Path); err != nil {
		return nil, fmt.Errorf("open store %s: %w", opts.Path, err)
	}

	// driver.Open calls register on every new connection, so each one
	// knows vec1's distance functions and FTS5's table type before it runs
	// any SQL.
	db, err := driver.Open(dataSourceName(opts.Path), register)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", opts.Path, err)
	}
	// Keep every connection open once made (idle limit = open limit). When
	// the last connection closes, SQLite deletes the -wal and -shm files, and
	// the next one would recreate them with the default mode instead of 0600.
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)

	s := &Store{db: db, dims: opts.Dims}
	if err := s.migrate(ctx); err != nil {
		// errors.Join keeps both errors if Close fails too.
		return nil, errors.Join(fmt.Errorf("open store %s: %w", opts.Path, err), db.Close())
	}
	if err := s.checkVectors(ctx, opts.EmbedModel, opts.Dims); err != nil {
		return nil, errors.Join(fmt.Errorf("open store %s: %w", opts.Path, err), db.Close())
	}
	return s, nil
}

// Close closes every connection. Call it once, after the last query.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

// register loads the two SQLite extensions Meru uses into one connection:
// vec1, SQLite's vector extension, for its vec1_cos_distance function, and
// FTS5, its full-text search. The WebAssembly build of SQLite leaves both
// out until asked.
func register(conn *sqlite3.Conn) error {
	if err := vec1.Register(conn); err != nil {
		return fmt.Errorf("register vec1: %w", err)
	}
	if err := fts5.Register(conn); err != nil {
		return fmt.Errorf("register fts5: %w", err)
	}
	return nil
}

// dataSourceName builds the "file:" URI the driver opens. The query string
// sets what each connection needs:
//
//   - busy_timeout(10000): wait up to 10 seconds for a lock another process
//     holds, instead of failing at once. Set first, as the driver asks.
//   - journal_mode(wal): write-ahead log, so readers never wait for writers.
//   - synchronous(normal): with WAL, a crash can lose the last commits but
//     never corrupts the file. The files hold the truth, so that is enough.
//   - foreign_keys(on): SQLite checks REFERENCES clauses only when asked.
//   - _txlock=immediate: every transaction takes the write lock at BEGIN,
//     so it can't fail halfway with "database is locked".
//   - modeof: SQLite gives the -wal file the same mode as the database.
func dataSourceName(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(wal)")
	q.Add("_pragma", "synchronous(normal)")
	q.Add("_pragma", "foreign_keys(on)")
	q.Set("_txlock", "immediate")
	q.Set("modeof", path)
	// A url.URL with only Path set escapes characters such as spaces, "?"
	// and "#", which would otherwise end the file name early.
	u := url.URL{Path: filepath.ToSlash(path)}
	// Encode writes a space in a value as "+", which SQLite doesn't decode.
	// "%20" means a space to both SQLite and Go, and a real "+" in a value
	// is already "%2B" at this point, so the swap is safe.
	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	return "file:" + u.EscapedPath() + "?" + query
}

// createPrivate makes sure the database file and its -wal and -shm
// companions exist with mode 0600 before SQLite opens them. SQLite creates
// missing files with mode 0644 (after the umask), which lets other users on
// the machine read your index. Creating them first, empty, sets the mode;
// SQLite accepts an empty file as a new database, an empty log and an empty
// shared-memory file. For files that already exist, Chmod fixes the mode.
func createPrivate(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		// os.OpenFile with O_CREATE makes the file if it's missing and
		// leaves an existing one alone. The last argument is the mode.
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- the path comes from config, not from a client
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Chmod(p, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// write runs fn inside one write transaction. It commits when fn returns
// nil and rolls back when fn fails, so a failed write changes nothing.
// writeMu makes writers in this process take turns.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	s.writeMu.Lock()
	// defer runs Unlock when write returns, on every path out.
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		// The rollback error adds nothing: fn's error says what went wrong,
		// and SQLite undoes an unfinished transaction when it can't roll back.
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

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

// Hit is one search result: a chunk and its rank in one result list.
type Hit struct {
	ChunkID int64
	Rank    int     // 0 is the best match
	Score   float64 // BM25 score or vector distance; lower is better for both
}

// ChunkWithDoc is a chunk together with the document it came from, for
// showing results and citing them.
type ChunkWithDoc struct {
	Chunk
	Path string
	Kind string
}

// Stats reports how much the index holds.
type Stats struct {
	Documents int
	Chunks    int
	// Vectors counts stored vectors. It equals Chunks once every chunk is
	// embedded, and is lower after an embedding model change.
	Vectors int
}
