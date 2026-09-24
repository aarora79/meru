// This file tests the skills part of a turn: the pick call, reading its
// answer, the two prompt sections, the cap on loaded instructions, and the
// skills on the "route" event.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/skills"
)

// pickEngine is a fakeEngine whose Generate plays the fast model's pick
// call: answer maps the question to the model's reply. Embedding *fakeEngine
// gives pickEngine its Stream, Embed and Info methods.
type pickEngine struct {
	*fakeEngine
	answer func(question string) string
	err    error

	mu   sync.Mutex // guards gens; Generate runs beside the router
	gens []streamCall
}

// Generate records the call and answers with p.answer, or p.err.
func (p *pickEngine) Generate(_ context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	p.mu.Lock()
	p.gens = append(p.gens, streamCall{msgs: slices.Clone(msgs), tools: tools, opts: opts})
	p.mu.Unlock()
	if p.err != nil {
		return engine.Completion{}, p.err
	}
	return engine.Completion{Text: p.answer(msgs[len(msgs)-1].Content), DoneReason: "stop"}, nil
}

// generates returns the Generate calls made so far.
func (p *pickEngine) generates() []streamCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.gens)
}

// fixedSkills serves one registry for every turn.
type fixedSkills struct{ reg *skills.Registry }

func (f fixedSkills) Registry(context.Context) *skills.Registry { return f.reg }

// builtinRegistry installs the built-in skills in a temp folder and loads
// them.
func builtinRegistry(t *testing.T) *skills.Registry {
	t.Helper()
	dir := t.TempDir()
	if _, err := skills.InstallBuiltins(dir); err != nil {
		t.Fatal(err)
	}
	reg, err := skills.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// landlordPicker answers the way the fast model should: writing for a
// message to write, none for anything else.
func landlordPicker(q string) string {
	if strings.Contains(q, "landlord") {
		return "writing"
	}
	return "none"
}

// TestPickWritingForEmail is the milestone example: "write a short email
// to my landlord" picks writing, the route event names it, and its
// instructions land in the system prompt after the list of skills.
func TestPickWritingForEmail(t *testing.T) {
	cfg := testConfig(t)
	eng := &pickEngine{fakeEngine: &fakeEngine{pieces: []string{"Dear landlord"}}, answer: landlordPicker}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, nil, nil, quietLog())
	reg := builtinRegistry(t)
	a.UseSkills(fixedSkills{reg: reg})

	evs, err := run(context.Background(), a, rpc.Request{Text: "write a short email to my landlord"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// The pick call: the fast model, thinking off, a short cap, and every
	// skill named in its prompt.
	gens := eng.generates()
	if len(gens) != 1 {
		t.Fatalf("Generate calls = %d, want 1 pick call", len(gens))
	}
	opts := gens[0].opts
	if opts.Model != cfg.Models.Fast || !opts.NoThink || opts.MaxTokens != pickMaxTokens {
		t.Errorf("pick options = %+v, want the fast model, NoThink and MaxTokens %d", opts, pickMaxTokens)
	}
	if sys := gens[0].msgs[0].Content; !strings.Contains(sys, "- writing: ") || !strings.Contains(sys, "- explainer: ") {
		t.Errorf("pick prompt doesn't list both skills:\n%s", sys)
	}

	var route rpc.Event
	for _, ev := range evs {
		if ev.Type == rpc.EventRoute {
			route = ev
		}
	}
	if len(route.Skills) != 1 || route.Skills[0].Name != "writing" {
		t.Errorf("route event skills = %+v, want [writing]", route.Skills)
	}

	system := eng.lastCall().msgs[0].Content
	body, err := reg.Body("writing")
	if err != nil {
		t.Fatal(err)
	}
	list := strings.Index(system, skillsListHeader)
	head := strings.Index(system, skillsBodyHeader+"\n\nSkill: writing\n\n")
	at := strings.Index(system, body)
	if list < 0 || head < 0 || at < 0 || !(list < head && head < at) {
		t.Errorf("system prompt lacks the list, then the header, then the writing body:\n%.600s", system)
	}
	if strings.Contains(system, "Skill: explainer") {
		t.Error("the explainer body loaded, but the pick chose writing alone")
	}
}

// TestPickNone checks a turn whose pick answers "none": the list of skills
// is in the prompt, no instructions are, and the route event names none.
func TestPickNone(t *testing.T) {
	cfg := testConfig(t)
	eng := &pickEngine{fakeEngine: &fakeEngine{pieces: []string{"4"}}, answer: landlordPicker}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, nil, nil, quietLog())
	a.UseSkills(fixedSkills{reg: builtinRegistry(t)})

	evs, err := run(context.Background(), a, rpc.Request{Text: "what is 2 + 2?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	system := eng.lastCall().msgs[0].Content
	if !strings.Contains(system, skillsListHeader+"\n- explainer: ") {
		t.Errorf("system prompt lacks the skill list:\n%.400s", system)
	}
	if strings.Contains(system, skillsBodyHeader) {
		t.Error("instructions loaded though the pick said none")
	}
	for _, ev := range evs {
		if ev.Type == rpc.EventRoute && len(ev.Skills) != 0 {
			t.Errorf("route event skills = %+v, want none", ev.Skills)
		}
	}
}

// TestPickSkipped checks that no skills, or an empty skills folder, makes
// no pick call and leaves the prompt as it was, and that a failed pick
// call still answers.
func TestPickSkipped(t *testing.T) {
	empty, err := skills.Load(filepath.Join(t.TempDir(), "none"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		skills   Skills
		err      error
		wantGens int
		wantList bool
	}{
		{name: "no skills", skills: nil},
		{name: "empty folder", skills: fixedSkills{reg: empty}},
		{name: "nil registry", skills: fixedSkills{}},
		{name: "pick fails", skills: fixedSkills{reg: builtinRegistry(t)}, err: errors.New("ollama down"), wantGens: 1, wantList: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			eng := &pickEngine{fakeEngine: &fakeEngine{pieces: []string{"ok"}}, answer: landlordPicker, err: tt.err}
			a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "direct", Confidence: 0.9, Outcome: "ok"}}, nil, nil, nil, nil, quietLog())
			if tt.skills != nil {
				a.UseSkills(tt.skills)
			}
			if _, err := run(context.Background(), a, rpc.Request{Text: "write a short email to my landlord"}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if n := len(eng.generates()); n != tt.wantGens {
				t.Errorf("Generate calls = %d, want %d", n, tt.wantGens)
			}
			system := eng.lastCall().msgs[0].Content
			if got := strings.Contains(system, skillsListHeader); got != tt.wantList {
				t.Errorf("skill list in prompt = %v, want %v", got, tt.wantList)
			}
			if strings.Contains(system, skillsBodyHeader) {
				t.Error("instructions loaded with no pick")
			}
		})
	}
}

// TestRouteErrorStopsPick checks that a failed route fails the turn even
// while the pick runs beside it.
func TestRouteErrorStopsPick(t *testing.T) {
	cfg := testConfig(t)
	eng := &pickEngine{fakeEngine: &fakeEngine{pieces: []string{"ok"}}, answer: landlordPicker}
	a := New(cfg, eng, &fakeRouter{err: errors.New("router broke")}, nil, nil, nil, nil, quietLog())
	a.UseSkills(fixedSkills{reg: builtinRegistry(t)})
	if _, err := run(context.Background(), a, rpc.Request{Text: "hello"}); err == nil || !strings.Contains(err.Error(), "router broke") {
		t.Errorf("Handle error = %v, want the router's", err)
	}
}

func TestParsePick(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"writing", "explainer", "portfolio-review"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		text := "---\nname: " + name + "\ndescription: d\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := skills.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		in   string
		want []string
	}{
		{"writing", []string{"writing"}},
		{" Writing.", []string{"writing"}},
		{"explainer, writing", []string{"explainer", "writing"}},
		{"- writing\n- explainer", []string{"writing", "explainer"}},
		{"portfolio-review", []string{"portfolio-review"}},
		{"writing, writing", []string{"writing"}},
		{"writing, explainer, portfolio-review", []string{"writing", "explainer"}},
		{"none", nil},
		{"None.", nil},
		{"", nil},
		{"poetry", nil},
		{"`writing`", []string{"writing"}},
	}
	for _, tt := range tests {
		if got := parsePick(tt.in, reg); !slices.Equal(got, tt.want) {
			t.Errorf("parsePick(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatBodies(t *testing.T) {
	long := strings.Repeat("line of text\n", 10) // 130 characters
	tests := []struct {
		name    string
		bodies  []namedBody
		limit   int
		want    []string // pieces the text must hold
		notWant []string
		wantCut bool
	}{
		{name: "none", bodies: nil, limit: 100},
		{name: "one under the cap", bodies: []namedBody{{"a", "short"}}, limit: 100,
			want: []string{skillsBodyHeader, "Skill: a\n\nshort"}},
		{name: "first kept whole past the cap", bodies: []namedBody{{"a", long}}, limit: 50,
			want: []string{"Skill: a\n\n" + long}, notWant: []string{cutNote}},
		{name: "second cut", bodies: []namedBody{{"a", "x"}, {"b", long}}, limit: 40,
			want: []string{"Skill: b\n\n" + strings.Repeat("line of text\n", 2) + "line of text\n\n" + cutNote}, wantCut: true},
		{name: "second gets nothing", bodies: []namedBody{{"a", long}, {"b", "more"}}, limit: 50,
			want: []string{"Skill: b\n\n" + cutNote}, notWant: []string{"more"}, wantCut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := formatBodies(tt.bodies, tt.limit)
			if cut != tt.wantCut {
				t.Errorf("cut = %v, want %v", cut, tt.wantCut)
			}
			if len(tt.bodies) == 0 && got != "" {
				t.Errorf("formatBodies(nil) = %q, want empty", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("text lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("text holds %q:\n%s", w, got)
				}
			}
		})
	}
}
