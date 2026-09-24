// Package retrieve is Meru's hybrid search over your files. It embeds the
// question, asks the store for the nearest chunks by meaning (vector
// search) and by words (BM25 keyword search), merges the two ranked lists
// with reciprocal-rank fusion, and loads the winning chunks' text. It also
// turns the results into a prompt section with numbered citations.
//
// See ARCHITECTURE.md, "Retrieval" and "How hybrid search works". The files:
//
//   - rrf.go: reciprocal-rank fusion and the sorted top N.
//   - search.go: Search, which runs the stages and times each one.
//   - format.go: citations and the prompt section.
//   - memories.go: SearchMemories, memory recall, which merges three lists
//     (by meaning, by keyword and by recency) with the same rrf.
//
// What this package deliberately doesn't do: it doesn't index files, run
// SQL or decide when to search. The indexer fills the store, the store runs
// the queries, and the agent decides from the route whether a turn searches.
// Past sessions (v0.4) will reuse rrf with lists of their own.
package retrieve
