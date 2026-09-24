// This file holds the memories table and its two indexes, memory_vec and
// memory_fts: storing and deleting one memory, listing what the table holds
// so the syncer can compare it with the files, and the three searches recall
// merges (by meaning, by keyword and by recency). ARCHITECTURE.md, "Memory",
// explains how Meru uses them.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// sortableTime is how memories store created and mtime: UTC, always nine
// digits of fraction. Every value has the same width, so sorting the text
// sorts by time. RFC 3339 with its fraction trimmed would not: "…05Z" sorts
// after "…05.5Z", because "Z" comes after "." in ASCII.
const sortableTime = "2006-01-02T15:04:05.000000000Z"

// Memory is one row of memories: one memory file, as the syncer last read
// it. The file is the truth; this row is a copy that search can use.
type Memory struct {
	// ID is the row's own number, which memory_vec and memory_fts share.
	// ReplaceMemory ignores it and assigns a new one.
	ID int64
	// MemID is the memory's ID in the memory folder, such as
	// "people/sam-is-my-manager.md".
	MemID string
	// Kind is the memory's folder, such as "people".
	Kind string
	// Text is the fact.
	Text string
	// Created is the date in the file's frontmatter; the zero time when the
	// file has none.
	Created time.Time
	// Source says where the memory came from, such as "session …".
	Source string
	// MTime is the file's modification time when the syncer read it.
	MTime time.Time
	// Hash is the syncer's hash of the memory's content; see
	// index.Memories.
	Hash string
}

// MemoryStamp is what the syncer needs to know about a stored memory to
// decide whether its file changed.
type MemoryStamp struct {
	MTime time.Time
	Hash  string
	// HasVector is false after an embedding model change dropped the
	// memory's vector.
	HasVector bool
}

// ReplaceMemory stores m and its vector v in one transaction, replacing
// whatever the table held for m.MemID. The row gets a new ID. It fails, and
// changes nothing, when m has no MemID or v has the wrong size.
func (s *Store) ReplaceMemory(ctx context.Context, m Memory, v engine.Vector) error {
	if m.MemID == "" {
		return errors.New("replace memory: empty memory ID")
	}
	if len(v) != s.dims {
		return fmt.Errorf("replace memory %s: vector has %d dimensions, want %d", m.MemID, len(v), s.dims)
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := deleteMemory(ctx, tx, m.MemID); err != nil {
			return err
		}
		created := ""
		if !m.Created.IsZero() {
			created = m.Created.UTC().Format(sortableTime)
		}
		var id int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO memories (mem_id, kind, text, created, source, mtime, hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			m.MemID, m.Kind, m.Text, created, m.Source, m.MTime.UTC().Format(sortableTime), m.Hash).Scan(&id)
		if err != nil {
			return fmt.Errorf("write memory: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts (rowid, text) VALUES (?, ?)`, id, m.Text); err != nil {
			return fmt.Errorf("write memory keywords: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_vec (memory_id, vector) VALUES (?, ?)`, id, encodeVector(v)); err != nil {
			return fmt.Errorf("write memory vector: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace memory %s: %w", m.MemID, err)
	}
	return nil
}

// DeleteMemory removes the memory memID and its index rows. Deleting a
// memory the table doesn't hold is not an error.
func (s *Store) DeleteMemory(ctx context.Context, memID string) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		return deleteMemory(ctx, tx, memID)
	})
	if err != nil {
		return fmt.Errorf("delete memory %s: %w", memID, err)
	}
	return nil
}

// deleteMemory removes the memory memID inside tx. As in deleteChunks, the
// keyword index forgets the row first, while memories still holds the text
// FTS5 needs to find the words it indexed.
func deleteMemory(ctx context.Context, tx *sql.Tx, memID string) error {
	stmts := []string{
		`INSERT INTO memory_fts (memory_fts, rowid, text)
		 SELECT 'delete', id, text FROM memories WHERE mem_id = ?`,
		`DELETE FROM memory_vec WHERE memory_id IN (SELECT id FROM memories WHERE mem_id = ?)`,
		`DELETE FROM memories WHERE mem_id = ?`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt, memID); err != nil {
			return fmt.Errorf("delete old memory: %w", err)
		}
	}
	return nil
}

// MemoryIDs returns every stored memory's ID with its stamp, so the syncer
// can tell which files changed, which are new and which are gone.
func (s *Store) MemoryIDs(ctx context.Context) (map[string]MemoryStamp, error) {
	// A LEFT JOIN keeps memories with no vector; for those, v.memory_id is
	// NULL.
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.mem_id, m.mtime, m.hash, v.memory_id IS NOT NULL
		 FROM memories m LEFT JOIN memory_vec v ON v.memory_id = m.id`)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	// rows holds a connection until closed; defer hands it back to the pool
	// when MemoryIDs returns.
	defer rows.Close()
	out := map[string]MemoryStamp{}
	for rows.Next() {
		var id, mtime string
		var st MemoryStamp
		if err := rows.Scan(&id, &mtime, &st.Hash, &st.HasVector); err != nil {
			return nil, fmt.Errorf("list memories: %w", err)
		}
		if st.MTime, err = time.Parse(sortableTime, mtime); err != nil {
			return nil, fmt.Errorf("list memories: %s mtime: %w", id, err)
		}
		out[id] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	return out, nil
}

// SearchMemoryVector returns the k memories whose vectors are nearest to v,
// nearest first, leaving out the kinds in exclude. It uses the same cosine
// distance as SearchVector. It fails when v doesn't have the store's vector
// size.
func (s *Store) SearchMemoryVector(ctx context.Context, v engine.Vector, k int, exclude []string) ([]Memory, error) {
	if len(v) != s.dims {
		return nil, fmt.Errorf("memory vector search: query has %d dimensions, want %d", len(v), s.dims)
	}
	if k <= 0 {
		return nil, nil
	}
	mems, err := s.memories(ctx,
		`SELECT `+memoryColumns+` FROM memory_vec v JOIN memories m ON m.id = v.memory_id
		 WHERE m.kind NOT IN (SELECT value FROM json_each(?2))
		 ORDER BY vec1_l2_distance(v.vector, ?1) / 2, m.id LIMIT ?3`,
		encodeVector(v), jsonList(exclude), k)
	if err != nil {
		return nil, fmt.Errorf("memory vector search: %w", err)
	}
	return mems, nil
}

// SearchMemoryKeyword returns the k memories that best match query under
// BM25, best first, leaving out the kinds in exclude. query is plain text,
// escaped the way SearchKeyword escapes it.
func (s *Store) SearchMemoryKeyword(ctx context.Context, query string, k int, exclude []string) ([]Memory, error) {
	match := ftsQuery(query)
	if match == "" || k <= 0 {
		return nil, nil
	}
	mems, err := s.memories(ctx,
		`SELECT `+memoryColumns+` FROM memory_fts f JOIN memories m ON m.id = f.rowid
		 WHERE memory_fts MATCH ?1 AND m.kind NOT IN (SELECT value FROM json_each(?2))
		 ORDER BY f.rank, m.id LIMIT ?3`,
		match, jsonList(exclude), k)
	if err != nil {
		return nil, fmt.Errorf("memory keyword search: %w", err)
	}
	return mems, nil
}

// RecentMemories returns the k newest memories, leaving out the kinds in
// exclude: by created date, newest first, then by the file's modification
// time, since many memories share a created date. A memory with no created
// date sorts by its modification time alone, after every dated one.
func (s *Store) RecentMemories(ctx context.Context, k int, exclude []string) ([]Memory, error) {
	if k <= 0 {
		return nil, nil
	}
	mems, err := s.memories(ctx,
		`SELECT `+memoryColumns+` FROM memories m
		 WHERE m.kind NOT IN (SELECT value FROM json_each(?1))
		 ORDER BY m.created DESC, m.mtime DESC, m.id LIMIT ?2`,
		jsonList(exclude), k)
	if err != nil {
		return nil, fmt.Errorf("recent memories: %w", err)
	}
	return mems, nil
}

// memoryColumns lists the columns memories reads, in the order it scans
// them.
const memoryColumns = `m.id, m.mem_id, m.kind, m.text, m.created, m.source, m.mtime, m.hash`

// memories runs a query that selects memoryColumns and returns the rows in
// the query's order.
func (s *Store) memories(ctx context.Context, query string, args ...any) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var created, mtime string
		if err := rows.Scan(&m.ID, &m.MemID, &m.Kind, &m.Text, &created, &m.Source, &mtime, &m.Hash); err != nil {
			return nil, err
		}
		if created != "" {
			if m.Created, err = time.Parse(sortableTime, created); err != nil {
				return nil, fmt.Errorf("%s created: %w", m.MemID, err)
			}
		}
		if m.MTime, err = time.Parse(sortableTime, mtime); err != nil {
			return nil, fmt.Errorf("%s mtime: %w", m.MemID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// jsonList writes list as a JSON array, which the queries above turn into
// rows with json_each, as Chunks does with its IDs. A nil list becomes
// "[]", so "NOT IN" then leaves nothing out.
func jsonList(list []string) string {
	if list == nil {
		list = []string{}
	}
	// Marshal can't fail on a slice of strings.
	b, _ := json.Marshal(list)
	return string(b)
}
