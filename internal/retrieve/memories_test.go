// This file tests memory recall: how the three lists (meaning, keyword,
// recency) rank memories, that excluded kinds never come back, and the
// meru.recall span.

package retrieve

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
)

// memoryStore opens a store with four memories, stored in this order (so
// with row IDs 1 to 4), their vectors on the three axes:
//
//	1  me/name.md          "Name is Amit"               day 23  axis 0  (a profile kind)
//	2  people/sam.md       "Sam is the user's manager"  day 10  axis 0
//	3  projects/garden.md  "Plans a vegetable garden"   day 20  axis 1
//	4  other/tea.md        "Drinks green tea"           day 22  axis 2
func memoryStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "fake", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mems := []struct {
		id, text string
		day      int
		vec      engine.Vector
	}{
		{"me/name.md", "Name is Amit", 23, engine.Vector{1, 0, 0}},
		{"people/sam.md", "Sam is the user's manager", 10, engine.Vector{1, 0, 0}},
		{"projects/garden.md", "Plans a vegetable garden", 20, engine.Vector{0, 1, 0}},
		{"other/tea.md", "Drinks green tea", 22, engine.Vector{0, 0, 1}},
	}
	for _, m := range mems {
		created := time.Date(2026, 9, m.day, 0, 0, 0, 0, time.UTC)
		row := store.Memory{MemID: m.id, Kind: filepath.Dir(m.id), Text: m.text, Created: created, MTime: created, Hash: "h"}
		if err := st.ReplaceMemory(ctx, row, m.vec); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestSearchMemories(t *testing.T) {
	ctx := context.Background()
	st := memoryStore(t)
	eng := &fakeEngine{vectors: map[string]engine.Vector{
		"who is my boss?":         {1, 0, 0},
		"any vegetable plans?":    {0, 0, 1},
		"what should I cook now?": {0.1, 0.1, 0.1},
	}}
	profile := []string{"me", "preferences"}
	tests := []struct {
		name    string
		query   string
		exclude []string
		n       int
		want    []string
	}{
		{
			// Sam is first by meaning, the only keyword hit ("is") once
			// name.md is left out, and last by recency: 1/61 + 1/61 + 1/63.
			// Tea is last by meaning but first by recency: 1/63 + 1/61,
			// just ahead of garden's 1/62 + 1/62.
			"meaning and keyword", "who is my boss?", profile, 5,
			[]string{"people/sam.md", "other/tea.md", "projects/garden.md"},
		},
		{"cut to n", "who is my boss?", profile, 1, []string{"people/sam.md"}},
		{
			// With nothing left out, name.md is first by meaning, a
			// keyword hit, and the newest, so it wins.
			"profile not excluded", "who is my boss?", nil, 2,
			[]string{"me/name.md", "people/sam.md"},
		},
		{
			// Tea is nearest by meaning, but garden holds the word
			// "vegetable" and is second newest: 1/63 + 1/61 + 1/62 beats
			// tea's 1/61 + 1/61.
			"keyword beats meaning", "any vegetable plans?", profile, 5,
			[]string{"projects/garden.md", "other/tea.md", "people/sam.md"},
		},
		{
			// No shared word and the same distance to every memory: the
			// vector list falls back to row order (sam, garden, tea), and
			// recency puts tea first. Sam and tea tie at 1/61 + 1/63, and the
			// lower row ID goes first; garden's 2/62 comes last.
			"recency list", "what should I cook now?", profile, 5,
			[]string{"people/sam.md", "other/tea.md", "projects/garden.md"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SearchMemories(ctx, st, eng, tt.query, tt.exclude, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for i, m := range got {
				ids = append(ids, m.MemID)
				if i > 0 && m.Score > got[i-1].Score {
					t.Errorf("scores not descending at %d", i)
				}
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("recalled %v, want %v", ids, tt.want)
			}
		})
	}
}

// TestSearchMemoriesRecencyAlone checks that a memory the question shares
// nothing with still comes back through the recency list when it is the
// only memory outside the profile.
func TestSearchMemoriesRecencyAlone(t *testing.T) {
	ctx := context.Background()
	st := memoryStore(t)
	eng := &fakeEngine{vectors: map[string]engine.Vector{"hello": {1, 0, 0}}}
	got, err := SearchMemories(ctx, st, eng, "hello", []string{"me", "people", "projects"}, 5)
	if err != nil || len(got) != 1 || got[0].MemID != "other/tea.md" {
		t.Errorf("SearchMemories = %+v, %v; want other/tea.md alone", got, err)
	}
}

// TestSearchMemoriesBlankAndSpan checks a blank query calls no model, and
// that the meru.recall span carries counts, never text.
func TestSearchMemoriesBlankAndSpan(t *testing.T) {
	rec := useRecorder(t)
	ctx := context.Background()
	st := memoryStore(t)
	eng := &fakeEngine{vectors: map[string]engine.Vector{"who is my boss?": {1, 0, 0}}}
	if got, err := SearchMemories(ctx, st, eng, "  ", nil, 5); err != nil || got != nil || eng.calls != 0 {
		t.Errorf("blank query: %v, %v, %d Embed calls; want nothing", got, err, eng.calls)
	}
	if _, err := SearchMemories(ctx, st, eng, "who is my boss?", []string{"me"}, 5); err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "meru.recall" {
		t.Fatalf("spans = %v, want one meru.recall", spans)
	}
	have := map[attribute.Key]int64{}
	for _, kv := range spans[0].Attributes() {
		if kv.Value.Type() == attribute.STRING {
			t.Errorf("attribute %s is text; the span must carry counts only", kv.Key)
		}
		have[kv.Key] = kv.Value.AsInt64()
	}
	for key, want := range map[attribute.Key]int64{
		"meru.recall.vector_hits": 3,
		"meru.recall.fts_hits":    1,
		"meru.recall.recent_hits": 3,
		"meru.recall.results":     3,
	} {
		if have[key] != want {
			t.Errorf("%s = %d, want %d", key, have[key], want)
		}
	}
}
