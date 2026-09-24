// This file holds the two searches retrieval runs, SearchVector over
// chunk_vec and SearchKeyword over chunk_fts, and Chunks, which loads the
// winners' text. How the two result lists get merged is in
// internal/retrieve and ARCHITECTURE.md, "How hybrid search works".

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/aarora79/meru/internal/engine"
)

// maxQueryTerms caps how many words of a query reach the keyword index. A
// question is a sentence or two; a pasted page would otherwise turn into a
// query with thousands of terms.
const maxQueryTerms = 32

// SearchVector returns the k chunks whose vectors are nearest to v, nearest
// first. Score is the cosine distance, from 0 (same direction) to 2. It
// fails when v doesn't have the store's vector size.
func (s *Store) SearchVector(ctx context.Context, v engine.Vector, k int) ([]Hit, error) {
	if len(v) != s.dims {
		return nil, fmt.Errorf("vector search: query has %d dimensions, want %d", len(v), s.dims)
	}
	if k <= 0 {
		return nil, nil
	}
	// Embedding models are trained for cosine distance, which measures the
	// angle between two vectors: 0 for the same direction, 2 for the
	// opposite. encodeVector stores every vector at length 1, and for two
	// such vectors the squared L2 distance, which vec1_l2_distance returns,
	// is exactly twice the cosine distance. Halving it gives the cosine
	// distance itself, faster than vec1_cos_distance, which measures both
	// lengths on every row: 147 ms against 198 ms over 100,000 vectors of
	// 768 numbers (bench_test.go).
	//
	// There is no index. SQLite computes the distance for every row and
	// keeps the k smallest; the chunk ID breaks ties.
	hits, err := s.hits(ctx,
		`SELECT chunk_id, vec1_l2_distance(vector, ?1) / 2 AS distance
		 FROM chunk_vec ORDER BY distance, chunk_id LIMIT ?2`, encodeVector(v), k)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return hits, nil
}

// SearchKeyword returns the k chunks that best match query under BM25, best
// first. query is plain text; the store escapes it for FTS5 (see
// ftsQuery), so any input is safe. A chunk matches when it holds any word
// of the query, and chunks holding more of the rarer words rank higher.
// Score is FTS5's BM25 value, which is negative: lower is better.
func (s *Store) SearchKeyword(ctx context.Context, query string, k int) ([]Hit, error) {
	match := ftsQuery(query)
	if match == "" || k <= 0 {
		return nil, nil
	}
	// rank is FTS5's name for the BM25 score of each matching row.
	hits, err := s.hits(ctx,
		`SELECT rowid, rank FROM chunk_fts WHERE chunk_fts MATCH ? ORDER BY rank, rowid LIMIT ?`, match, k)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	return hits, nil
}

// hits runs a query that returns (chunk ID, score) rows, best first, and
// numbers them from rank 0.
func (s *Store) hits(ctx context.Context, query string, args ...any) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		h := Hit{Rank: len(hits)}
		if err := rows.Scan(&h.ChunkID, &h.Score); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// ftsQuery turns plain text into an FTS5 query that can't be read as FTS5
// syntax. It splits the text into words at every character that isn't a
// letter, digit or combining mark, wraps each word in double quotes, and
// joins them with OR. Inside double quotes FTS5 treats AND, OR, NEAR, *, :,
// -, ^ and parentheses as ordinary text, and the split has already dropped
// every quote character. Repeated words count once, and only the first
// maxQueryTerms words count. It returns "" when the text holds no words.
//
// OR rather than FTS5's default AND: a question such as "what did I note
// about the Q3 budget" should find a chunk that says "Q3 budget" without
// the words "what", "did" and "note". BM25 already scores common words low.
func ftsQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
	seen := map[string]bool{}
	var terms []string
	for _, w := range words {
		key := strings.ToLower(w)
		if seen[key] {
			continue
		}
		seen[key] = true
		// Doubling any quote is how FTS5 escapes one inside a string. The
		// split above leaves none, but this keeps the quoting right even if
		// the split rule changes.
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
		if len(terms) == maxQueryTerms {
			break
		}
	}
	return strings.Join(terms, " OR ")
}

// Chunks loads the chunks with the given IDs, in the order of ids, each with
// the path and kind of its document. IDs that aren't in the index are left
// out, so the result can be shorter than ids.
func (s *Store) Chunks(ctx context.Context, ids []int64) ([]ChunkWithDoc, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// Pass the IDs as one JSON array; json_each turns it into rows. This
	// keeps the SQL text fixed, with no placeholder list built at run time.
	idJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("load chunks: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.doc_id, c.ordinal, c.heading, c.text, c.start_line, c.end_line, c.page, d.path, d.kind
		 FROM chunks c JOIN documents d ON d.id = c.doc_id
		 WHERE c.id IN (SELECT value FROM json_each(?))`, string(idJSON))
	if err != nil {
		return nil, fmt.Errorf("load chunks: %w", err)
	}
	defer rows.Close()

	byID := map[int64]ChunkWithDoc{}
	for rows.Next() {
		var c ChunkWithDoc
		if err := rows.Scan(&c.ID, &c.DocID, &c.Ordinal, &c.Heading, &c.Text,
			&c.StartLine, &c.EndLine, &c.Page, &c.Path, &c.Kind); err != nil {
			return nil, fmt.Errorf("load chunks: %w", err)
		}
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load chunks: %w", err)
	}

	// SQL returns rows in no promised order, so put them back in the
	// caller's order.
	out := make([]ChunkWithDoc, 0, len(byID))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
