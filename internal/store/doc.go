// Package store is Meru's SQLite database, ~/.meru/meru.db: documents,
// chunks, a vector index (SQLite's vec1 extension) and a keyword index
// (FTS5), all in one file.
//
// The database is a projection: merud can rebuild all of it from your files,
// so deleting meru.db loses nothing. See ARCHITECTURE.md, "Storage". The
// driver is ncruces/go-sqlite3, which runs SQLite as WebAssembly translated
// to Go, so Meru needs no C compiler.
//
// What this package deliberately doesn't do: it doesn't read files, chunk
// text or call models. The indexer does those and hands the store finished
// chunks and vectors.
package store
