# database/sql

**In one line:** `database/sql` is Go's standard way to talk to a SQL
database: one pool of connections, and methods to run queries and read rows.

## Why Go has it

Every database speaks its own wire protocol, but programs want the same
things from all of them: run a query, bind parameters, read rows. The
standard library defines those operations once, and a **driver** package
plugs in a specific database. Meru's driver is `ncruces/go-sqlite3/driver`.

A `*sql.DB` is not one connection. It is a pool: each query borrows a free
connection and returns it when done, so many goroutines can share one
`*sql.DB`.

## Smallest example

```go
db, err := sql.Open("sqlite3", "file:demo.db")  // no connection yet
if err != nil { return err }
defer db.Close()

var n int
err = db.QueryRowContext(ctx, `SELECT count(*) FROM chunks WHERE doc_id = ?`, 7).Scan(&n)

rows, err := db.QueryContext(ctx, `SELECT path FROM documents`)
if err != nil { return err }
defer rows.Close()
for rows.Next() {
    var p string
    if err := rows.Scan(&p); err != nil { return err }
}
return rows.Err()
```

- `?` is a placeholder. The driver sends the value separately from the SQL
  text, so a value can never change what the SQL means. Never build SQL by
  gluing user input into the string.
- `QueryRowContext(...).Scan(&n)` reads one row into variables. `&n` passes
  the address of `n`, so `Scan` can write into it.
- `ExecContext` runs a statement that returns no rows (`INSERT`, `DELETE`).
- A query that finds nothing makes `Scan` return `sql.ErrNoRows`. Check it
  with `errors.Is(err, sql.ErrNoRows)`.

## Where Meru uses it

- `internal/store/store.go` — `driver.Open` builds the pool; `SetMaxOpenConns`
  and `SetMaxIdleConns` keep up to eight connections open.
- `internal/store/documents.go` — `Document` uses `sql.ErrNoRows` to mean
  "not indexed"; `Paths` loops over `rows`.
- `internal/store/search.go` — `hits` reads (chunk ID, score) rows.

## Mistakes to avoid

- Forgetting `rows.Close()`. Until it runs, the row set holds its connection,
  and a pool that runs out of connections makes the next query wait.
- Skipping `rows.Err()` after the loop. `rows.Next()` returns false both at
  the end and on an error; only `rows.Err()` tells them apart.
