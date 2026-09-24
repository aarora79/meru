# Transactions

**In one line:** a transaction groups several SQL statements so that either
all of them take effect or none do.

## Why Go has it

Replacing a document in Meru's store takes a dozen statements: delete the
old keyword rows, vectors and chunks, then write the new ones. If `merud`
stopped halfway, the index would hold a document with no chunks, or vectors
for chunks that no longer exist. A transaction makes the dozen statements
one step. SQLite writes them all at `COMMIT`, or none of them.

`database/sql` gives a transaction its own type, `*sql.Tx`. It holds one
connection for its whole life, so every statement in it runs on the same
connection.

## Smallest example

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil { return err }

if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE doc_id = ?`, id); err != nil {
    _ = tx.Rollback()   // undo everything since BeginTx
    return err
}
if _, err := tx.ExecContext(ctx, `INSERT INTO chunks (doc_id, text) VALUES (?, ?)`, id, text); err != nil {
    _ = tx.Rollback()
    return err
}
return tx.Commit()      // make both changes visible at once
```

- Run every statement on `tx`, not on `db`. A statement on `db` borrows a
  different connection and falls outside the transaction.
- Readers on other connections see the old state until `Commit` returns.

## Where Meru uses it

- `internal/store/store.go` — `write` wraps every write: it takes the writer
  lock, begins a transaction, runs a function the caller passes, and commits
  or rolls back. Callers can't forget either step.
- `internal/store/documents.go` — `ReplaceDocument` and `DeleteDocument` keep
  `chunks`, `chunk_fts` and `chunk_vec` in step inside one `write`.
- `internal/store/schema.go` — each migration step and its version bump
  commit together, so a failed step leaves the old version in `meta`.

## Mistakes to avoid

- Returning early without `Rollback` or `Commit`. The transaction then holds
  its connection, and SQLite's write lock, until the program exits.
- Long work inside a transaction, such as calling a model. SQLite allows one
  writer at a time, so every other write waits. Meru embeds chunks before it
  opens the transaction.
