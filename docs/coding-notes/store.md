# store

**Code:** `internal/store/` (`doc.go`, `store.go`, `schema.go`, `documents.go`, `search.go`)
**Milestone:** v0.2
**Architecture:** [Storage](../../ARCHITECTURE.md#storage) and
[How hybrid search works](../../ARCHITECTURE.md#how-hybrid-search-works)

## What it does

The store is Meru's one SQLite file, `~/.meru/meru.db`. It holds every indexed
file (a **document**), the pieces of text the indexer cut each file into
(**chunks**), one vector per chunk, and a keyword index over the chunk text.
The indexer writes to it; retrieval reads from it.

Everything in the file can be rebuilt from your files, so the store never
holds the only copy of anything. Delete `meru.db` and `merud` builds it again.

The driver is `ncruces/go-sqlite3`. It runs SQLite compiled to WebAssembly
and then translated to Go, so Meru builds with no C compiler. Two SQLite
extensions come with it:

- **vec1**, SQLite's own vector search, holds the vectors in `chunk_vec`.
- **FTS5**, SQLite's full-text search, holds the keyword index in `chunk_fts`.

## The picture

```mermaid
flowchart LR
    IDX["indexer"] -- "ReplaceDocument / DeleteDocument" --> W["write()<br/>one transaction,<br/>one writer at a time"]
    W --> D[("documents")]
    W --> C[("chunks")]
    W --> F[("chunk_fts<br/>FTS5, over chunks")]
    W --> V[("chunk_vec<br/>vec1, flat, cosine")]
    RET["retrieve.Search"] -- "SearchVector" --> V
    RET -- "SearchKeyword" --> F
    RET -- "Chunks" --> C
    C --- D
```

All three of `chunks`, `chunk_fts` and `chunk_vec` use the chunk ID as their
key, so a hit from either search names a chunk the same way.

## Walk through the code

### store.go: opening the file

`Open` does four things in order:

1. Checks the options: a path, an embedding model name, and a vector size.
2. Calls `createPrivate`, which creates `meru.db`, `meru.db-wal` and
   `meru.db-shm` with mode `0600` (owner read and write only) if they don't
   exist, and sets that mode if they do. SQLite would otherwise create them
   with mode `0644`, readable by every user on the machine.
3. Opens a pool of connections with `driver.Open`. The `register` callback
   loads vec1 and FTS5 into each new connection.
4. Runs `migrate` and then `checkVectors` (both in `schema.go`).

The connection string sets write-ahead-log (WAL) mode:

```go
q.Add("_pragma", "journal_mode(wal)")
q.Set("_txlock", "immediate")
```

In WAL mode a reader sees the last committed state and never waits for a
writer. `_txlock=immediate` makes every transaction take SQLite's write lock
at `BEGIN`, so a write can't fail halfway through with "database is locked".

Writes go through one helper:

```go
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
    s.writeMu.Lock()
    defer s.writeMu.Unlock()
    tx, err := s.db.BeginTx(ctx, nil)
    ...
    if err := fn(tx); err != nil {
        _ = tx.Rollback()
        return err
    }
    return tx.Commit()
}
```

`writeMu` is a `sync.Mutex`, a lock. Writers in `merud` take turns, and
readers skip the lock. `fn` gets the transaction and does its SQL; if it
returns an error, `write` rolls back and nothing changes.

### schema.go: migrations and the vector check

`migrations()` returns a list of SQL steps. Step `i` takes the database from
`schema_version` `i` to `i+1`, and `meta` records the version. `migrate` runs
each step the file hasn't seen, one transaction per step. Later milestones
add `messages`, `tool_calls` and `memories` by appending a step. A shipped
step never changes, because existing files have already run it.

The first step creates `documents`, `chunks` and `chunk_fts`:

```sql
CREATE VIRTUAL TABLE chunk_fts USING fts5(
    heading, text, content='chunks', content_rowid='id'
);
```

`content='chunks'` makes `chunk_fts` an **external content** table. It indexes
the `heading` and `text` columns of `chunks` without keeping its own copy of
the text, and its `rowid` is the chunk ID.

`chunk_vec` is not a migration step, because its shape depends on the
embedding model. `checkVectors` compares the model name and vector size in
`meta` with the ones `Open` got. When either differs, it drops `chunk_vec` and
creates it again, empty:

```sql
CREATE VIRTUAL TABLE chunk_vec USING vec1(vector);
INSERT INTO chunk_vec(cmd, arg) VALUES ('rebuild', '{index:"flat", distance:"cos"}');
```

The `rebuild` command sets up an exact `flat` index with cosine distance.
Vectors from two models can't be compared, so all of them go. Documents and
chunks stay, so keyword search keeps working while the indexer re-embeds.

`NeedsReembed` reports that gap. It counts rows instead of keeping a flag:
some chunks lack a vector exactly when the model changed and the indexer
hasn't finished. The count stays right if `merud` crashes halfway through
re-embedding.

### documents.go: writing documents

`ReplaceDocument` stores one file and its chunks in one transaction:

1. `deleteChunks` removes the document's old keyword rows, vectors and chunks.
2. An upsert (`INSERT … ON CONFLICT (path) DO UPDATE … RETURNING id`) writes
   the document row and keeps its ID.
3. `insertChunk` writes each chunk, then its keyword row and its vector under
   the chunk's new ID.

The order in `deleteChunks` matters. FTS5 forgets a row of an external
content table only when told the exact text it indexed:

```sql
INSERT INTO chunk_fts (chunk_fts, rowid, heading, text)
SELECT 'delete', c.id, c.heading, c.text FROM chunks c ...
```

So the keyword rows go first, while `chunks` still holds the text. Skip this
and FTS5 keeps matching words from text that no longer exists.

vec1 stores each vector as a blob of 32-bit floats, 4 bytes each, in the
machine's byte order. SQLite's WebAssembly machine is little-endian on every
host, so `encodeVector` always writes little-endian.

`Paths(prefix)` treats the prefix as a folder: `/notes` matches `/notes/a.md`
and not `/notes2/b.md`. It compares with `substr` rather than `LIKE`, so a `%`
or `_` in a folder name has no special meaning.

### search.go: the two searches

`SearchVector` asks vec1 for the nearest `k` vectors:

```sql
SELECT rowid, distance FROM chunk_vec(?, ?) ORDER BY distance, rowid
```

Calling `chunk_vec(?, ?)` like a function passes the query vector and `k`.
vec1 adds a hidden `distance` column: the cosine distance, 0 for the same
direction and 2 for the opposite. On an empty table vec1 rejects every query
as the wrong size, so `SearchVector` checks for an empty table first.

`SearchKeyword` runs BM25 over `chunk_fts`:

```sql
SELECT rowid, rank FROM chunk_fts WHERE chunk_fts MATCH ? ORDER BY rank, rowid LIMIT ?
```

The query text comes from the user, and FTS5 has its own query language
(`AND`, `OR`, `NEAR`, `*`, `column:`, parentheses). `ftsQuery` makes any input
safe. It splits the text into words at every character that isn't a letter,
digit or combining mark, wraps each word in double quotes, and joins them
with `OR`:

```text
what's NEAR(x*)?   →   "what" OR "s" OR "NEAR" OR "x"
```

Inside double quotes FTS5 reads every word as plain text. `OR` lets a long
question match a chunk that holds only its key words, and BM25 scores common
words such as "what" low.

`Chunks(ids)` loads chunk text and document paths for a list of IDs. It passes
the IDs as one JSON array and lets SQLite's `json_each` turn it into rows, so
the SQL text never changes with the number of IDs. It then puts the rows back
in the caller's order.

## Go ideas used here

- **`database/sql`** — Go's standard interface to SQL databases: a pool, queries,
  rows and scanning. More in [go-basics/sql.md](go-basics/sql.md).
- **Transactions** — `BeginTx`, `Commit`, `Rollback`, and why every write here
  runs inside one. More in [go-basics/transactions.md](go-basics/transactions.md).
- **`sync.Mutex`** — a lock; `writeMu` makes writers take turns.
- **Function values** — `write` takes `fn func(tx *sql.Tx) error`, a function
  the caller writes inline.
- **`defer`** — `defer rows.Close()` hands the connection back to the pool on
  every path out. More in [go-basics/defer.md](go-basics/defer.md).
- **`errors.Is` and `sql.ErrNoRows`** — how `Document` tells "not indexed" from
  a real failure. More in [go-basics/errors.md](go-basics/errors.md).
- **Struct embedding** — `ChunkWithDoc` embeds `Chunk`, so `c.Text` works
  without writing `c.Chunk.Text`.

## Try it

```sh
go test -race ./internal/store/
go test -run '^$' -bench . -benchtime 20x ./internal/store/
```

`TestSearchKeyword` feeds FTS5 syntax, quotes and SQL fragments as queries.
`TestConcurrentReadsDuringWrites` runs four readers against a writer and
checks no reader ever sees half a write. The benchmark indexes 10,000 chunks
with 768-number vectors.

Measured on an Apple M4 Max (v0.35.6 of the driver):

| Operation, 10,000 chunks, 768 dimensions | Time |
| --- | --- |
| index all 10,000 chunks (100 documents) | 6.4–7.6 s |
| `SearchVector`, k = 50 | 15–18 ms |
| `SearchKeyword`, k = 50, 8 common words | 10–14 ms |
| `ReplaceDocument`, one 100-chunk document | 186 ms |

Adding a vector to the flat index gets slower as the index grows: a
100-chunk document took 6 ms to store into an empty index and 120 ms into
one holding 9,000 chunks.

## Why it's built this way

- **One pool, one lock for writers.** A second connection pool just for writes
  would also work, but one mutex around `write` gives the same "one writer at a
  time" with less to follow.
- **Explicit SQL instead of triggers.** SQLite triggers could keep `chunk_fts`
  and `chunk_vec` in step with `chunks` by themselves. `deleteChunks` and
  `insertChunk` do it in Go instead, so the whole write reads top to bottom in
  one file.
- **An exact index.** vec1 also offers approximate indexes that need training.
  A personal index of tens of thousands of chunks doesn't need one yet, and
  `meru.retrieval.duration` will show when it does.
- **Counting instead of a flag for re-embedding.** A flag in `meta` would need
  someone to clear it at the right moment. Comparing the chunk and vector
  counts can't go stale.
