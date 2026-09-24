// This file tests the memories table: storing, replacing and deleting a
// memory with its index rows, the stamps the syncer reads, the three
// searches with their kind filter, and an embedding model change.

package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// day returns midnight UTC on the given day of September 2026.
func day(d int) time.Time {
	return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC)
}

// putMemory stores a memory with vector v, created on day created and
// modified at minute past midnight that day. It fails the test on error.
func putMemory(t *testing.T, s *Store, memID, kind, text string, created, minute int, v engine.Vector) {
	t.Helper()
	m := Memory{
		MemID: memID, Kind: kind, Text: text, Source: "session x",
		MTime: day(created).Add(time.Duration(minute) * time.Minute), Hash: "h-" + text,
	}
	if created > 0 {
		m.Created = day(created)
	}
	if err := s.ReplaceMemory(context.Background(), m, v); err != nil {
		t.Fatalf("ReplaceMemory(%s): %v", memID, err)
	}
}

// memIDs returns the MemID of each memory, in order.
func memIDs(mems []Memory) []string {
	var out []string
	for _, m := range mems {
		out = append(out, m.MemID)
	}
	return out
}

// checkMemoryIndexes fails the test if memory_fts or memory_vec disagree
// with memories.
func checkMemoryIndexes(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO memory_fts (memory_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Errorf("memory_fts integrity check: %v", err)
	}
	var orphans int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM memory_vec WHERE memory_id NOT IN (SELECT id FROM memories)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d memory vectors have no memory", orphans)
	}
}

func TestReplaceAndDeleteMemory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	putMemory(t, s, "people/sam.md", "people", "Sam is the user's manager", 20, 0, unit(0))
	putMemory(t, s, "projects/garden.md", "projects", "Plans a vegetable garden", 21, 0, unit(1))

	// Replace one: the old words leave the keyword index, the new ones join.
	putMemory(t, s, "people/sam.md", "people", "Sam moved to the Denver office", 22, 5, unit(2))
	checkMemoryIndexes(t, s)
	if got, _ := s.SearchMemoryKeyword(ctx, "manager", 5, nil); len(got) != 0 {
		t.Errorf("old text still found: %v", memIDs(got))
	}
	got, err := s.SearchMemoryKeyword(ctx, "Denver", 5, nil)
	if err != nil || !slices.Equal(memIDs(got), []string{"people/sam.md"}) {
		t.Fatalf("SearchMemoryKeyword = %v, %v", memIDs(got), err)
	}
	m := got[0]
	if m.Kind != "people" || m.Source != "session x" || !m.Created.Equal(day(22)) ||
		!m.MTime.Equal(day(22).Add(5*time.Minute)) || m.Hash != "h-Sam moved to the Denver office" {
		t.Errorf("row = %+v", m)
	}

	stamps, err := s.MemoryIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stamps) != 2 || !stamps["people/sam.md"].HasVector || !stamps["projects/garden.md"].MTime.Equal(day(21)) {
		t.Errorf("MemoryIDs = %+v", stamps)
	}

	if err := s.DeleteMemory(ctx, "people/sam.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMemory(ctx, "people/nobody.md"); err != nil {
		t.Errorf("deleting a missing memory: %v", err)
	}
	checkMemoryIndexes(t, s)
	if stamps, _ := s.MemoryIDs(ctx); len(stamps) != 1 {
		t.Errorf("after delete: %+v", stamps)
	}
}

func TestReplaceMemoryRejects(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.ReplaceMemory(ctx, Memory{Kind: "people", Text: "x"}, unit(0)); err == nil {
		t.Error("no memory ID: want an error")
	}
	if err := s.ReplaceMemory(ctx, Memory{MemID: "people/a.md", Text: "x"}, engine.Vector{1, 0}); err == nil {
		t.Error("short vector: want an error")
	}
	if stamps, _ := s.MemoryIDs(ctx); len(stamps) != 0 {
		t.Errorf("a refused write left rows: %+v", stamps)
	}
}

// TestMemorySearches checks the order of each search and that exclude
// leaves out whole kinds.
func TestMemorySearches(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	putMemory(t, s, "me/name.md", "me", "Name is Dana, manager of the registry team", 25, 0, unit(0))
	putMemory(t, s, "people/sam.md", "people", "Sam is the user's manager", 10, 0, engine.Vector{0.9, 0.1, 0})
	putMemory(t, s, "projects/garden.md", "projects", "Plans a vegetable garden", 20, 0, unit(1))
	putMemory(t, s, "other/tea.md", "other", "Drinks green tea", 20, 9, unit(2))
	putMemory(t, s, "other/undated.md", "other", "Has a bike", 0, 0, unit(2))
	profile := []string{"me", "preferences"}

	tests := []struct {
		name string
		run  func() ([]Memory, error)
		want []string
	}{
		{"vector, all kinds", func() ([]Memory, error) { return s.SearchMemoryVector(ctx, unit(0), 2, nil) },
			[]string{"me/name.md", "people/sam.md"}},
		{"vector, profile left out", func() ([]Memory, error) { return s.SearchMemoryVector(ctx, unit(0), 2, profile) },
			[]string{"people/sam.md", "projects/garden.md"}},
		{"keyword, all kinds", func() ([]Memory, error) { return s.SearchMemoryKeyword(ctx, "manager", 5, nil) },
			[]string{"people/sam.md", "me/name.md"}},
		{"keyword, profile left out", func() ([]Memory, error) { return s.SearchMemoryKeyword(ctx, "manager", 5, profile) },
			[]string{"people/sam.md"}},
		{"keyword, no words", func() ([]Memory, error) { return s.SearchMemoryKeyword(ctx, " ?! ", 5, nil) }, nil},
		// Created first; the two memories of day 20 go by modification
		// time; the undated one comes last.
		{"recent", func() ([]Memory, error) { return s.RecentMemories(ctx, 10, profile) },
			[]string{"other/tea.md", "projects/garden.md", "people/sam.md", "other/undated.md"}},
		{"recent, cut", func() ([]Memory, error) { return s.RecentMemories(ctx, 1, nil) }, []string{"me/name.md"}},
		{"recent, k zero", func() ([]Memory, error) { return s.RecentMemories(ctx, 0, nil) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.run()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(memIDs(got), tt.want) {
				t.Errorf("got %v, want %v", memIDs(got), tt.want)
			}
		})
	}
	if _, err := s.SearchMemoryVector(ctx, engine.Vector{1}, 2, nil); err == nil {
		t.Error("short query vector: want an error")
	}
}

// TestEmbedModelChangeDropsMemoryVectors checks a new embedding model
// clears memory_vec, keeps the memories searchable by keyword, and marks
// each memory as needing a vector.
func TestEmbedModelChangeDropsMemoryVectors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meru.db")
	s, err := Open(ctx, Options{Path: path, EmbedModel: "m1", Dims: testDims})
	if err != nil {
		t.Fatal(err)
	}
	putMemory(t, s, "people/sam.md", "people", "Sam is the user's manager", 10, 0, unit(0))
	s.Close()

	s = openAt(t, path, "m2", testDims)
	stamps, err := s.MemoryIDs(ctx)
	if err != nil || len(stamps) != 1 || stamps["people/sam.md"].HasVector {
		t.Errorf("MemoryIDs = %+v, %v; want one memory with no vector", stamps, err)
	}
	if got, _ := s.SearchMemoryVector(ctx, unit(0), 5, nil); len(got) != 0 {
		t.Errorf("vector search found %v after the model change", memIDs(got))
	}
	if got, _ := s.SearchMemoryKeyword(ctx, "manager", 5, nil); len(got) != 1 {
		t.Error("keyword search lost the memory")
	}
}
