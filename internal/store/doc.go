// Package store is Meru's SQLite database, ~/.meru/meru.db: documents,
// chunks, one vector per chunk, and a keyword index (FTS5), all in one
// file. Vector search compares the query with every stored vector. Vectors
// are stored at length 1, so half of vec1's squared L2 distance equals the
// cosine distance, and the L2 function is the cheaper of the two.
//
// The database is a projection: merud can rebuild all of it from your files,
// so deleting meru.db loses nothing. See ARCHITECTURE.md, "Storage". The
// driver is ncruces/go-sqlite3, which runs SQLite as WebAssembly translated
// to Go, so Meru needs no C compiler.
//
// The files:
//
//   - store.go: the types, Open and Close, and the one write path.
//   - schema.go: the migration steps and the embedding-model check.
//   - documents.go: storing, replacing and deleting documents.
//   - search.go: vector search, keyword search and loading chunks.
//   - toolcalls.go: the tool_calls audit log, and rebuilding it from the
//     session transcripts (v0.3).
//   - turns.go: the turns table, one row per answered question, its
//     rebuild from the transcripts, and the usage windows (v0.3).
//   - memories.go: one row, one vector and one keyword entry per memory
//     file, and the three searches recall merges (v0.4).
//
// What this package deliberately doesn't do: it doesn't read files, chunk
// text or call models. The indexer does those and hands the store finished
// chunks and vectors. It doesn't merge the two searches either; that is
// internal/retrieve.
package store
