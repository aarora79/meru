// Package applenotes reads Apple Notes from the Notes app's own database,
// NoteStore.sqlite, for the built-in tools search_notes, read_note and
// list_notes (ARCHITECTURE.md, "Approving a tool call", the built-in tools
// table).
//
// It opens the database read-only on each call and closes it before the
// call returns, so every answer reflects the notes as they are now,
// including changes the Notes app has only written to its -wal file. It
// keeps no index and no copy of any note. It uses the same SQLite driver
// as internal/store, so it needs no C compiler.
//
// The database is Apple's private Core Data store, with no published
// schema. The package finds what it needs instead of assuming it: the
// entity numbers for notes and folders come from Z_PRIMARYKEY, and the
// column names from PRAGMA table_info, since they differ between macOS
// releases. A note's text is a gzipped protobuf; body.go decodes the one
// field that holds the text, with google.golang.org/protobuf's low-level
// wire reader.
//
// The files:
//
//   - store.go: Open, the queries, and the Note and Folder types.
//   - body.go: decoding a note's text.
//
// What it doesn't do: it never writes to the database. A row written
// behind the Notes app's back can break its iCloud sync, so writes, when
// Meru adds them, must go through the Notes app. It doesn't read a locked
// note's text, attachments or drawings. Reading the database needs Full
// Disk Access for merud; without it, Open fails with ErrNoAccess.
package applenotes
