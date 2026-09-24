// This file tests the recalled-memories section: its format and cap, where
// it sits in the system prompt on each route, what a failed recall does,
// and, end to end, the v0.4 "Done when": a memory saved with remember in
// one session comes back in a later session's prompt.

package agent

import (
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// fakeRecall is a Profile that hands back fixed profile memories and fixed
// recalled ones, and keeps the queries it was asked.
type fakeRecall struct {
	profile  []memory.Memory
	recalled []retrieve.Memory
	err      error

	mu      sync.Mutex // guards queries
	queries []string
}

func (f *fakeRecall) Profile() ([]memory.Memory, error) { return f.profile, nil }

func (f *fakeRecall) Recall(ctx context.Context, query string) ([]retrieve.Memory, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	return f.recalled, f.err
}

// recalled builds a recalled memory of kind with text.
func recalled(kind, text string) retrieve.Memory {
	return retrieve.Memory{Memory: store.Memory{MemID: kind + "/x.md", Kind: kind, Text: text}}
}

func TestFormatMemories(t *testing.T) {
	tests := []struct {
		name        string
		mems        []retrieve.Memory
		limit       int
		want        string
		wantDropped int
	}{
		{name: "none", limit: 2400, want: ""},
		{name: "only blank text", mems: []retrieve.Memory{recalled("people", " \n")}, limit: 2400, want: ""},
		{
			name: "best first, with the kind, on one line each",
			mems: []retrieve.Memory{
				recalled("people", "Sam is the user's\nmanager"),
				recalled("projects", "Plans a vegetable garden"),
			},
			limit: 2400,
			want: "Things you remember that may matter here:\n" +
				"- (people) Sam is the user's manager\n" +
				"- (projects) Plans a vegetable garden",
		},
		{
			// The header is 41 characters; "- (other) short" adds 1 + 15.
			// A limit of 60 has no room for the long line but fits the
			// short one after it.
			name: "a line over the cap is left out",
			mems: []retrieve.Memory{
				recalled("other", strings.Repeat("x", 30)),
				recalled("other", "short"),
			},
			limit:       60,
			want:        "Things you remember that may matter here:\n- (other) short",
			wantDropped: 1,
		},
		{
			name:        "nothing fits",
			mems:        []retrieve.Memory{recalled("other", strings.Repeat("x", 30))},
			limit:       50,
			want:        "",
			wantDropped: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped := formatMemories(tt.mems, tt.limit)
			if got != tt.want || dropped != tt.wantDropped {
				t.Errorf("formatMemories = %q, %d\nwant %q, %d", got, dropped, tt.want, tt.wantDropped)
			}
			if n := len([]rune(got)); n > tt.limit {
				t.Errorf("section is %d characters, over the %d limit", n, tt.limit)
			}
		})
	}
}

// TestFormatMemoriesCap checks the real cap, 600 tokens, holds however
// many long memories come back.
func TestFormatMemoriesCap(t *testing.T) {
	var mems []retrieve.Memory
	for range 5 {
		mems = append(mems, recalled("other", strings.Repeat("word ", 200)))
	}
	got, dropped := formatMemories(mems, maxMemoryChars)
	if n := len([]rune(got)); n > maxMemoryChars || n == 0 {
		t.Errorf("section is %d characters, want 1 to %d", n, maxMemoryChars)
	}
	if dropped != 3 {
		t.Errorf("dropped %d, want 3: each line is about 1,010 characters", dropped)
	}
}

// TestMemorySectionInPrompt checks the section's place in the system
// prompt: after the profile, before filesNote, and apart from the file
// excerpts. It runs on every route, direct included, and a failed recall
// leaves it out without failing the turn.
func TestMemorySectionInPrompt(t *testing.T) {
	section := "Things you remember that may matter here:\n- (people) Sam is the user's manager"
	profile := []memory.Memory{mem("me", "Name is Amit Arora", 1, 0)}
	sam := []retrieve.Memory{recalled("people", "Sam is the user's manager")}
	tests := []struct {
		name  string
		route string
		mems  *fakeRecall
		want  bool // the section is in the prompt
	}{
		{"direct", "direct", &fakeRecall{profile: profile, recalled: sam}, true},
		{"search", "search", &fakeRecall{profile: profile, recalled: sam}, true},
		{"tools", "tools", &fakeRecall{profile: profile, recalled: sam}, true},
		{"nothing recalled", "direct", &fakeRecall{profile: profile}, false},
		{"recall fails", "direct", &fakeRecall{profile: profile, err: errors.New("ollama down")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			eng := &fakeEngine{pieces: []string{"ok"}}
			search := &fakeSearcher{results: []retrieve.Result{result("/n/budget.md", "Q3 budget", "The Q3 budget is 40k.", 1, 9, 0.03)}}
			tools := &fakeTools{specs: []engine.ToolSpec{spec("notes.search")}}
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.9, Outcome: "ok"}},
				search, tools, nil, tt.mems, quietLog())
			if _, err := run(context.Background(), a, rpc.Request{Text: "should I tell Sam about the budget?"}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if got := tt.mems.queries; !slices.Equal(got, []string{"should I tell Sam about the budget?"}) {
				t.Errorf("recall queries = %q, want the question once", got)
			}
			system := eng.lastCall().msgs[0].Content
			prof := strings.Index(system, "- Name is Amit Arora")
			mems := strings.Index(system, section)
			files := strings.Index(system, filesNote(nil))
			if !tt.want {
				if strings.Contains(system, memoryHeader) {
					t.Errorf("system prompt holds the memory section:\n%s", system)
				}
				return
			}
			if prof < 0 || mems < 0 || files < 0 || !(prof < mems && mems < files) {
				t.Errorf("system prompt = %q\nwant the profile, then %q, then filesNote", system, section)
			}
			// The excerpts, when the route searched, come after and apart.
			if excerpts := strings.Index(system, "From your files"); excerpts >= 0 && excerpts < mems {
				t.Errorf("the memory section sits inside or after the excerpts:\n%s", system)
			}
		})
	}
}

// TestNoProfileNoRecall checks an agent with no Profile runs no recall and
// adds no section.
func TestNoProfileNoRecall(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{pieces: []string{"ok"}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, nil, nil, quietLog())
	if _, err := run(context.Background(), a, rpc.Request{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(eng.lastCall().msgs[0].Content, memoryHeader) {
		t.Error("memory section without a Profile")
	}
}

// meaningEngine embeds text by a crude notion of meaning: a text about a
// manager or a boss points along the first axis, anything else along the
// second. It stands in for the embedding model in the cross-session test,
// where it makes "my boss" match "manager" with no shared word.
type meaningEngine struct{}

func (meaningEngine) Embed(ctx context.Context, texts []string) ([]engine.Vector, error) {
	out := make([]engine.Vector, len(texts))
	for i, t := range texts {
		l := strings.ToLower(t)
		if strings.Contains(l, "manager") || strings.Contains(l, "boss") {
			out[i] = engine.Vector{1, 0, 0}
		} else {
			out[i] = engine.Vector{0, 1, 0}
		}
	}
	return out, nil
}

func (meaningEngine) Generate(context.Context, []engine.Message, []engine.ToolSpec, engine.Options) (engine.Completion, error) {
	return engine.Completion{}, errors.New("not used")
}

func (meaningEngine) Stream(context.Context, []engine.Message, []engine.ToolSpec, engine.Options) (iter.Seq2[engine.Delta, error], error) {
	return nil, errors.New("not used")
}

func (meaningEngine) Info(context.Context) (engine.ModelInfo, error) {
	return engine.ModelInfo{}, errors.New("not used")
}

// storeMemories is the Profile merud builds, rebuilt here from the real
// parts: the profile from the memory folder, recall from the store.
type storeMemories struct {
	mem *memory.Store
	st  *store.Store
	eng engine.Engine
}

func (s storeMemories) Profile() ([]memory.Memory, error) {
	var out []memory.Memory
	for _, kind := range rpc.ProfileKinds() {
		mems, err := s.mem.ListKind(kind)
		if err != nil {
			return nil, err
		}
		out = append(out, mems...)
	}
	return out, nil
}

func (s storeMemories) Recall(ctx context.Context, query string) ([]retrieve.Memory, error) {
	return retrieve.SearchMemories(ctx, s.st, s.eng, query, rpc.ProfileKinds(), 5)
}

// TestRecallAcrossSessions is the v0.4 "Done when" for recall: it recalls
// something from last week without a reminder. Meru holds six unrelated
// memories, more than recall returns. In one session the model saves "Sam
// Lee is the user's manager" with the real remember tool, through the real
// dispatcher. A week later, in a new session, a question about "my boss",
// which shares no word with the memory, brings Sam back into the prompt by
// meaning; the profile stays in its own section.
func TestRecallAcrossSessions(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	mem, err := memory.Open(filepath.Join(cfg.Dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(cfg.Dir, "meru.db"), EmbedModel: "fake", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	embed := meaningEngine{}
	syncer := index.NewMemories(mem, st, embed, nil)
	syncNow := func(ctx context.Context) {
		if _, err := syncer.Sync(ctx); err != nil {
			t.Errorf("Sync: %v", err)
		}
	}
	if _, err := mem.Add("me", "Name is Amit Arora", "meru"); err != nil {
		t.Fatal(err)
	}
	// Six things Meru already knows, none about a boss: more than the five
	// that recall returns.
	for _, text := range []string{
		"Plays chess on Sundays", "Keeps bees", "Drinks green tea",
		"Runs five kilometres twice weekly", "Owns two cats", "Studies Spanish",
	} {
		if _, err := mem.Add("other", text, "meru"); err != nil {
			t.Fatal(err)
		}
	}
	syncNow(ctx)
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{}, mem, "", nil, syncNow)
	disp := dispatch.New([]dispatch.Backend{tools}, st, dispatch.Options{})

	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("remember", `{"kind":"people","text":"Sam Lee is the user's manager"}`)}},
		{pieces: []string{"Saved."}},
		{pieces: []string{"Here is a draft."}},
	}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, disp, nil, storeMemories{mem: mem, st: st, eng: embed}, quietLog())

	// Session one: the model saves the fact.
	evs, err := run(ctx, a, rpc.Request{Text: "remember that Sam Lee is my manager"})
	if err != nil {
		t.Fatalf("session one: %v", err)
	}
	first := evs[0].Session
	saved := false
	for _, ev := range evs {
		if ev.Type == rpc.EventToolResult && ev.Tool != nil && ev.Tool.Outcome == dispatch.OutcomeOK {
			saved = true
		}
	}
	if !saved {
		t.Fatalf("remember didn't succeed: %+v", evs)
	}

	// A week passes. Rather than wait, move the memory's created date back
	// seven days, as a hand edit would, and sync. Sam is now the oldest
	// memory, last in the recency list, and its row the newest, last in any
	// tie. Only its vector can bring it back.
	people, err := mem.ListKind("people")
	if err != nil || len(people) != 1 || !strings.HasPrefix(people[0].Source, "session "+first) {
		t.Fatalf("people memories = %+v, %v; want one from session one", people, err)
	}
	raw, err := os.ReadFile(people[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	today := "created: " + time.Now().Format("2006-01-02")
	lastWeek := "created: " + time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	if err := os.WriteFile(people[0].Path, []byte(strings.Replace(string(raw), today, lastWeek, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	syncNow(ctx)

	// Session two, a new session on the direct route, with no reminder.
	router.dec = Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}
	evs, err = run(ctx, a, rpc.Request{Text: "Draft a note to my boss about launch slipping"})
	if err != nil {
		t.Fatalf("session two: %v", err)
	}
	if evs[0].Session == first {
		t.Fatal("session two reused session one")
	}
	system := eng.lastCall().msgs[0].Content
	start := strings.Index(system, memoryHeader)
	if start < 0 {
		t.Fatalf("no memory section in the prompt:\n%s", system)
	}
	section := system[start:]
	section = section[:strings.Index(section, "\n\n")]
	lines := strings.Split(section, "\n")[1:]
	// Sam's memory is the oldest of seven, so recency alone would leave it
	// out of the five, and the question shares no word with it. Its vector,
	// nearest to "my boss", brings it back.
	if len(lines) != 5 || !slices.Contains(lines, "- (people) Sam Lee is the user's manager") {
		t.Errorf("recalled lines = %q\nwant 5, Sam's among them", lines)
	}
	if strings.Contains(section, "Amit Arora") || !strings.Contains(system, "What you know about the user:\n- Name is Amit Arora") {
		t.Errorf("the profile belongs in its own section:\n%s", system)
	}
}
