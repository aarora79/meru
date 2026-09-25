// This file tests retrieval: the RRF arithmetic against the worked examples
// in the architecture docs, deterministic ties, Search end to end with a
// fake engine and a real temporary store, and the citation format.

package retrieve

import (
	"context"
	"errors"
	"iter"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
)

// near reports whether a and b agree to within 0.00005, the precision the
// docs print scores to.
func near(a, b float64) bool {
	return math.Abs(a-b) < 0.00005
}

// TestRRF checks the scores rrf gives. The "200.md" case is the "Kestrel
// launch" table in docs/architecture/200.md, with chunk IDs 1 standup.md,
// 2 launch-plan.md, 3 todo.md and 4 release-checklist.md.
func TestRRF(t *testing.T) {
	tests := []struct {
		name  string
		lists [][]int64
		want  map[int64]float64
	}{
		{"no lists", nil, map[int64]float64{}},
		{"one list", [][]int64{{5, 6}}, map[int64]float64{5: 1.0 / 61, 6: 1.0 / 62}},
		{
			"200.md: Kestrel launch",
			[][]int64{{1, 2, 3}, {2, 4, 1}}, // keyword list, then meaning list
			map[int64]float64{
				2: 1.0/62 + 1.0/61, // 0.0325
				1: 1.0/61 + 1.0/63, // 0.0323
				4: 1.0 / 62,        // 0.0161
				3: 1.0 / 63,        // 0.0159
			},
		},
		{"both lists first", [][]int64{{9}, {9}}, map[int64]float64{9: 2.0 / 61}},
		{"three lists", [][]int64{{1}, {2}, {1}}, map[int64]float64{1: 2.0 / 61, 2: 1.0 / 61}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rrf(tt.lists...)
			if len(got) != len(tt.want) {
				t.Fatalf("rrf = %v, want %v", got, tt.want)
			}
			for id, w := range tt.want {
				if !near(got[id], w) {
					t.Errorf("score[%d] = %.4f, want %.4f", id, got[id], w)
				}
			}
		})
	}

	// The printed values in 200.md, to four places.
	got := rrf([]int64{1, 2, 3}, []int64{2, 4, 1})
	for id, printed := range map[int64]float64{2: 0.0325, 1: 0.0323, 4: 0.0161, 3: 0.0159} {
		if math.Round(got[id]*10000)/10000 != printed {
			t.Errorf("chunk %d scores %.4f, 200.md prints %.4f", id, got[id], printed)
		}
	}
}

// TestTop checks the order: score high to low, then chunk ID low to high,
// cut to n.
func TestTop(t *testing.T) {
	tests := []struct {
		name   string
		scores map[int64]float64
		n      int
		want   []int64
	}{
		{"200.md order", rrf([]int64{1, 2, 3}, []int64{2, 4, 1}), 10, []int64{2, 1, 4, 3}},
		{"cut to n", rrf([]int64{1, 2, 3}, []int64{2, 4, 1}), 2, []int64{2, 1}},
		{"tie goes to lower ID", rrf([]int64{7}, []int64{3}), 10, []int64{3, 7}},
		{"swapped lists tie", rrf([]int64{8, 5}, []int64{5, 8}), 10, []int64{5, 8}},
		{"n zero means all", map[int64]float64{1: 0.1, 2: 0.2}, 0, []int64{2, 1}},
		{"empty", map[int64]float64{}, 5, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run it several times: map order changes from run to run, and
			// the result must not.
			for range 20 {
				var got []int64
				for _, s := range top(tt.scores, tt.n) {
					got = append(got, s.id)
				}
				if !slices.Equal(got, tt.want) {
					t.Fatalf("top = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// fakeEngine is an engine.Engine whose Embed looks each text up in vectors.
// The other three methods fail; Search must not call them.
type fakeEngine struct {
	vectors map[string]engine.Vector
	err     error // returned by Embed when set
	calls   int   // Embed calls so far
}

// Embed returns the vector for each text, or an error for an unknown one.
func (f *fakeEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]engine.Vector, len(texts))
	for i, text := range texts {
		v, ok := f.vectors[text]
		if !ok {
			return nil, errors.New("fake: no vector for " + text)
		}
		out[i] = v
	}
	return out, nil
}

// Generate fails: retrieval never generates text.
func (f *fakeEngine) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	return engine.Completion{}, errors.New("fake: Generate not supported")
}

// Stream fails: retrieval never generates text.
func (f *fakeEngine) Stream(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (iter.Seq2[engine.Delta, error], error) {
	return nil, errors.New("fake: Stream not supported")
}

// Info fails: retrieval doesn't need it.
func (f *fakeEngine) Info(ctx context.Context) (engine.ModelInfo, error) {
	return engine.ModelInfo{}, errors.New("fake: Info not supported")
}

// testStore opens a store in a temporary folder with three documents. The
// vectors are hand-placed: "launch" questions point along the first axis.
//
//	chunk 1  /n/launch.md   "Q3 launch"  near the launch axis, says "Q3 launch"
//	chunk 2  /n/launch.md   "Travel"     off the axis, says "travel dates"
//	chunk 3  /n/roadmap.md  "Roadmap"    on the launch axis, never says "launch"
//	chunk 4  /n/rocket.pdf  page 3       far from the axis, says "launch" once
func testStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "fake", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	docs := []struct {
		doc    store.Document
		chunks []store.Chunk
		vecs   []engine.Vector
	}{
		{
			store.Document{Path: "/n/launch.md", Kind: "markdown", MTime: time.Now()},
			[]store.Chunk{
				{Heading: "Q3 launch", Text: "The Q3 launch is on 12 May.", StartLine: 12, EndLine: 40},
				{Heading: "Travel", Text: "Travel dates moved.", StartLine: 41, EndLine: 41},
			},
			[]engine.Vector{{0.9, 0.3, 0}, {0.2, 1, 0}},
		},
		{
			store.Document{Path: "/n/roadmap.md", Kind: "markdown", MTime: time.Now()},
			[]store.Chunk{{Heading: "Roadmap", Text: "We plan to ship the app this quarter.", StartLine: 1, EndLine: 9}},
			[]engine.Vector{{1, 0, 0}},
		},
		{
			store.Document{Path: "/n/rocket.pdf", Kind: "pdf", MTime: time.Now()},
			[]store.Chunk{{Text: "Model rocket launch worksheet.", Page: 3}},
			[]engine.Vector{{0, 0, 1}},
		},
	}
	for _, d := range docs {
		if err := st.ReplaceDocument(ctx, d.doc, d.chunks, d.vecs); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// useRecorder installs a tracer provider that keeps finished spans, for one
// test, and puts the no-op provider back afterwards.
func useRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(tracenoop.NewTracerProvider()) })
	return rec
}

// TestSearch runs the whole pipeline on a real store with a fake engine.
func TestSearch(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	eng := &fakeEngine{vectors: map[string]engine.Vector{
		"Q3 launch": {1, 0.1, 0},
		"zebra":     {0, 0, 1},
	}}

	tests := []struct {
		name  string
		query string
		opts  Options
		want  []string // Cite line of each result, in order
	}{
		{
			// Vector order: 3, 1, 2, 4. Keyword order: 1, 4 ("Q3" and
			// "launch" in chunk 1; "launch" in chunk 4). Chunk 1 is high in
			// both lists: 1/62 + 1/61. Chunk 4 is last by meaning but second
			// by keyword: 1/64 + 1/62 = 0.0318. That beats chunk 3, first by
			// meaning only: 1/61 = 0.0164.
			"both lists", "Q3 launch", Options{},
			[]string{
				`[1] /n/launch.md, "Q3 launch", lines 12–40`,
				`[2] /n/rocket.pdf, page 3`,
				`[3] /n/roadmap.md, "Roadmap", lines 1–9`,
				`[4] /n/launch.md, "Travel", line 41`,
			},
		},
		{
			"top 2", "Q3 launch", Options{TopN: 2},
			[]string{`[1] /n/launch.md, "Q3 launch", lines 12–40`, `[2] /n/rocket.pdf, page 3`},
		},
		{
			// Each list holds one chunk, 3 by meaning and 1 by keyword. Both
			// score 1/61, and the lower ID goes first.
			"lists of 1", "Q3 launch", Options{VectorK: 1, KeywordK: 1},
			[]string{`[1] /n/launch.md, "Q3 launch", lines 12–40`, `[2] /n/roadmap.md, "Roadmap", lines 1–9`},
		},
		{
			"meaning only", "zebra", Options{VectorK: 1},
			[]string{`[1] /n/rocket.pdf, page 3`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := Search(ctx, st, eng, tt.query, tt.opts)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			var got []string
			for i, r := range results {
				got = append(got, Cite(i+1, r))
				if i > 0 && r.Score > results[i-1].Score {
					t.Errorf("scores not descending at %d", i)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("results:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

// TestSearchSpan checks the meru.retrieve span carries counts and no text.
func TestSearchSpan(t *testing.T) {
	rec := useRecorder(t)
	st := testStore(t)
	eng := &fakeEngine{vectors: map[string]engine.Vector{"Q3 launch": {1, 0.1, 0}}}
	if _, err := Search(context.Background(), st, eng, "Q3 launch", Options{TopN: 2}); err != nil {
		t.Fatal(err)
	}

	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "meru.retrieve" {
		t.Fatalf("spans = %v, want one meru.retrieve", spans)
	}
	have := map[attribute.Key]attribute.Value{}
	for _, kv := range spans[0].Attributes() {
		have[kv.Key] = kv.Value
		if kv.Value.Type() == attribute.STRING {
			t.Errorf("attribute %s is text (%q); the span must carry counts only", kv.Key, kv.Value.AsString())
		}
	}
	for key, want := range map[attribute.Key]int64{
		"meru.retrieve.vector_hits": 4,
		"meru.retrieve.fts_hits":    2,
		"meru.retrieve.fused":       4,
		"meru.retrieve.results":     2,
	} {
		if got := have[key].AsInt64(); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	for _, key := range []attribute.Key{"meru.retrieve.vector_ms", "meru.retrieve.fts_ms", "meru.retrieve.fusion_ms"} {
		if _, ok := have[key]; !ok {
			t.Errorf("span has no %s", key)
		}
	}
}

// TestSearchFailures checks a blank query skips the model, and an embedding
// failure comes back as an error that marks the span.
func TestSearchFailures(t *testing.T) {
	rec := useRecorder(t)
	st := testStore(t)
	ctx := context.Background()

	eng := &fakeEngine{}
	if results, err := Search(ctx, st, eng, "  \n ", Options{}); err != nil || results != nil || eng.calls != 0 {
		t.Errorf("blank query: %v, %v, %d Embed calls; want nothing", results, err, eng.calls)
	}

	boom := errors.New("ollama down")
	eng = &fakeEngine{err: boom}
	if _, err := Search(ctx, st, eng, "launch", Options{}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Status().Code.String() != "Error" {
		t.Errorf("want one failed span, got %v", spans)
	}
}

func TestCite(t *testing.T) {
	result := func(heading string, start, end, page int) Result {
		return Result{ChunkWithDoc: store.ChunkWithDoc{
			Path:  "notes/launch.md",
			Chunk: store.Chunk{Heading: heading, StartLine: start, EndLine: end, Page: page},
		}}
	}
	tests := []struct {
		name string
		n    int
		r    Result
		want string
	}{
		{"lines", 1, result("Q3 launch", 12, 40, 0), `[1] notes/launch.md, "Q3 launch", lines 12–40`},
		{"one line", 2, result("Q3 launch", 7, 7, 0), `[2] notes/launch.md, "Q3 launch", line 7`},
		{"page", 3, result("", 0, 0, 4), `[3] notes/launch.md, page 4`},
		{"nothing to locate", 4, result("", 0, 0, 0), `[4] notes/launch.md`},
		{"quote in heading", 5, result(`The "plan"`, 1, 2, 0), `[5] notes/launch.md, "The \"plan\"", lines 1–2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Cite(tt.n, tt.r); got != tt.want {
				t.Errorf("Cite = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	if got := Format(nil); got != "" {
		t.Errorf("Format(nil) = %q, want empty", got)
	}
	results := []Result{
		{ChunkWithDoc: store.ChunkWithDoc{Path: "notes/launch.md",
			Chunk: store.Chunk{Heading: "Q3 launch", Text: "The Q3 launch is on 12 May.\n", StartLine: 12, EndLine: 40}}},
		{ChunkWithDoc: store.ChunkWithDoc{Path: "docs/rocket.pdf",
			Chunk: store.Chunk{Text: "Model rocket launch worksheet.", Page: 3}}},
	}
	want := `Excerpts from the user's files. Cite the ones you use by number, like [1].

[1] notes/launch.md, "Q3 launch", lines 12–40
The Q3 launch is on 12 May.

[2] docs/rocket.pdf, page 3
Model rocket launch worksheet.
`
	if got := Format(results); got != want {
		t.Errorf("Format =\n%s\nwant:\n%s", got, want)
	}
}
