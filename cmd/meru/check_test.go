// This file tests `meru check`: parsing the check file, grading one turn,
// the session groups, the refused approvals, the output and the exit code.
// A fake merud over a real socket plays each turn.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

func TestParseChecks(t *testing.T) {
	good := `# my checks
{"id": "a", "category": "direct", "question": "Capital of Australia?", "want": {"route": ["direct"], "answer_any": ["Canberra"]}}

  # an indented comment
{"id": "b", "category": "session", "session": "g", "question": "q2", "want": {"tools": ["grep"], "no_tools": false, "answer_all": ["x"], "sources_any": ["n"], "sources_none": ["m"], "max_seconds": 30}}
{"id": "c", "category": "direct", "question": "no want"}
`
	qs, err := parseChecks(strings.NewReader(good), "checks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 3 {
		t.Fatalf("got %d questions, want 3", len(qs))
	}
	b := qs[1]
	if b.Session != "g" || b.Want.MaxSeconds != 30 || !slices.Equal(b.Want.SourcesNone, []string{"m"}) {
		t.Errorf("second question = %+v", b)
	}

	bad := []struct {
		name, text, want string
	}{
		{"bad JSON", `{"id": "a",`, "checks.jsonl line 1: unexpected EOF"},
		{"unknown field", `{"id": "a", "category": "c", "question": "q", "want": {"answr_any": ["x"]}}`, `line 1: unknown field "answr_any"`},
		{"unknown top field", `{"id": "a", "category": "c", "question": "q", "extra": 1}`, `line 1: unknown field "extra"`},
		{"missing id", `{"category": "c", "question": "q"}`, `line 1: missing "id"`},
		{"missing category", `{"id": "a", "question": "q"}`, `line 1: missing "category"`},
		{"missing question", `{"id": "a", "category": "c", "question": "  "}`, `line 1: missing "question"`},
		{"negative time", `{"id": "a", "category": "c", "question": "q", "want": {"max_seconds": -1}}`, `"max_seconds" is negative`},
		{"two objects", `{"id": "a", "category": "c", "question": "q"} {}`, "line 1: more than one JSON object"},
		{"duplicate id", "{\"id\": \"a\", \"category\": \"c\", \"question\": \"q\"}\n# x\n{\"id\": \"a\", \"category\": \"c\", \"question\": \"q\"}",
			`line 3: id "a" is already used on line 1`},
		{"wrong type", `{"id": "a", "category": "c", "question": "q", "want": {"route": "direct"}}`, "line 1: cannot unmarshal"},
		{"empty", "# only a comment\n\n", "checks.jsonl has no questions"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseChecks(strings.NewReader(tt.text), "checks.jsonl")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestSelectChecks(t *testing.T) {
	qs := []checkQuestion{{ID: "a", Category: "web"}, {ID: "b", Category: "direct"}, {ID: "c", Category: "web"}}
	tests := []struct {
		only    string
		wantIDs []string
		wantErr string
	}{
		{"", []string{"a", "b", "c"}, ""},
		{"web", []string{"a", "c"}, ""},
		{"b, c", []string{"b", "c"}, ""},
		{"web,nope", nil, "--only nope"},
	}
	for _, tt := range tests {
		t.Run(tt.only, func(t *testing.T) {
			got, err := selectChecks(qs, tt.only)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			var ids []string
			for _, q := range got {
				ids = append(ids, q.ID)
			}
			if err != nil || !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("ids = %v, %v; want %v", ids, err, tt.wantIDs)
			}
		})
	}
}

func TestGrade(t *testing.T) {
	rec := turnRecord{
		Route:   "search+tools",
		Tools:   []string{"obsidian.obsidian_search_vault", "read_file", "cmd.git-log", "write_file"},
		Ran:     []string{"obsidian.obsidian_search_vault", "read_file", "cmd.git-log"},
		Sources: []string{"~/notes/Coase-Firm.md", "~/vault/lisbon-trip.md"},
		Answer:  "Transaction COSTS explain the firm. See go.dev.",
		Seconds: 12.5,
	}
	tests := []struct {
		name string
		want checkWant
		rec  turnRecord
		out  []string // nil: pass
	}{
		{"empty want passes", checkWant{}, rec, nil},
		{"route pass", checkWant{Route: []string{"search", "search+tools"}}, rec, nil},
		{"route fail", checkWant{Route: []string{"direct"}}, rec, []string{"route search+tools, want direct"}},
		{"no route", checkWant{Route: []string{"direct", "tools"}}, turnRecord{}, []string{"route none, want direct or tools"}},
		{"tool full name", checkWant{Tools: []string{"obsidian.obsidian_search_vault"}}, rec, nil},
		{"tool server prefix", checkWant{Tools: []string{"obsidian"}}, rec, nil},
		{"tool built-in", checkWant{Tools: []string{"read_file"}}, rec, nil},
		{"tool short name", checkWant{Tools: []string{"git-log", "obsidian_search_vault"}}, rec, nil},
		{"tool half a name", checkWant{Tools: []string{"obsid"}}, rec, []string{"tool obsid not called"}},
		{"tool fail", checkWant{Tools: []string{"web_fetch", "grep"}}, rec, []string{"tool web_fetch not called", "tool grep not called"}},
		{"tool refused", checkWant{Tools: []string{"write_file"}}, rec, []string{"tool write_file not called"}},
		{"no tools pass", checkWant{NoTools: true}, turnRecord{}, nil},
		{"no tools fail", checkWant{NoTools: true}, rec,
			[]string{"called obsidian_search_vault, read_file, git-log, write_file, want no tools"}},
		{"answer any pass", checkWant{AnswerAny: []string{"nope", "transaction costs"}}, rec, nil},
		{"answer any fail", checkWant{AnswerAny: []string{"1.27.1", "price"}}, rec, []string{"answer lacks all of: 1.27.1, price"}},
		{"answer all pass", checkWant{AnswerAll: []string{"FIRM", "go.dev"}}, rec, nil},
		{"answer all fail", checkWant{AnswerAll: []string{"firm", "http", "Amit"}}, rec, []string{"answer lacks: http, Amit"}},
		{"answer none pass", checkWant{AnswerNone: []string{"i've sent", "has been sent"}}, rec, nil},
		{"answer none fail", checkWant{AnswerNone: []string{"nope", "Transaction costs", "GO.DEV"}}, rec,
			[]string{"answer holds: Transaction costs, GO.DEV"}},
		{"sources any pass", checkWant{SourcesAny: []string{"coase"}}, rec, nil},
		{"sources any fail", checkWant{SourcesAny: []string{"naur", "theory"}}, rec, []string{"no source matches any of: naur, theory"}},
		{"sources any, no sources", checkWant{SourcesAny: []string{"x"}}, turnRecord{}, []string{"no source matches any of: x"}},
		{"sources none pass", checkWant{SourcesNone: []string{"naur"}}, rec, nil},
		{"sources none fail", checkWant{SourcesNone: []string{"Lisbon-trip"}}, rec, []string{"source ~/vault/lisbon-trip.md matches Lisbon-trip"}},
		{"time pass", checkWant{MaxSeconds: 13}, rec, nil},
		{"time fail", checkWant{MaxSeconds: 10}, rec, []string{"took 12.5s, want under 10s"}},
		{"merud error", checkWant{}, turnRecord{Error: "model not found"}, []string{"merud error: model not found"}},
		{"two fields fail", checkWant{Route: []string{"direct"}, AnswerAll: []string{"http"}}, rec,
			[]string{"route search+tools, want direct", "answer lacks: http"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := grade(tt.want, tt.rec); !slices.Equal(got, tt.out) {
				t.Errorf("grade = %q, want %q", got, tt.out)
			}
		})
	}
}

// fakeTurns plays merud for `meru check`. Each question in answers maps to
// the events its turn sends. It records every request, and gives a new
// session ID to each request that names none.
type fakeTurns struct {
	answers  map[string][]rpc.Event
	requests []rpc.Request
	approved []rpc.Choice // what the client answered to each approval
	sessions int
}

func (f *fakeTurns) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
	// meru check asks once which model set is in use; that isn't a question.
	if req.Op == rpc.OpModels {
		return emit(rpc.Event{Type: rpc.EventModels, Models: &rpc.ModelsInfo{Active: "test-set", Main: "test-model"}})
	}
	f.requests = append(f.requests, req)
	session := req.Session
	if session == "" {
		f.sessions++
		session = "s" + string(rune('0'+f.sessions))
	}
	if err := emit(rpc.Event{Type: rpc.EventSession, Session: session}); err != nil {
		return err
	}
	for _, ev := range f.answers[req.Text] {
		if ev.Type == rpc.EventApproval {
			choice, err := approve(ctx, *ev.Approval)
			if err != nil {
				return err
			}
			f.approved = append(f.approved, choice)
			continue
		}
		if err := emit(ev); err != nil {
			return err
		}
	}
	return nil
}

// writeChecks writes lines to a checks.jsonl in a new folder and returns
// its path.
func writeChecks(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "checks.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckSessions(t *testing.T) {
	f := &fakeTurns{}
	sock := startServer(t, f.handle)
	path := writeChecks(t,
		`{"id": "a1", "category": "c", "session": "A", "question": "a1"}`,
		`{"id": "solo", "category": "c", "question": "solo"}`,
		`{"id": "a2", "category": "c", "session": "A", "question": "a2"}`,
		`{"id": "b1", "category": "c", "session": "B", "question": "b1"}`,
		`{"id": "b2", "category": "c", "session": "B", "question": "b2"}`,
		`{"id": "solo2", "category": "c", "question": "solo2"}`,
	)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "check", path}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, errOut.String())
	}
	var got []string
	for _, r := range f.requests {
		if r.Source != rpc.SourceCLI || r.Op != rpc.OpAsk {
			t.Errorf("request = %+v, want an ask from the cli", r)
		}
		got = append(got, r.Text+"="+r.Session)
	}
	// a1 starts s1 and a2 continues it; b1 starts s3 and b2 continues it;
	// the two solo questions each start their own.
	want := []string{"a1=", "solo=", "a2=s1", "b1=", "b2=s3", "solo2="}
	if !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestCheckRun(t *testing.T) {
	tool := func(typ rpc.EventType, name, outcome string) rpc.Event {
		return rpc.Event{Type: typ, Tool: &rpc.ToolEvent{ID: "1", Name: name, Kind: "builtin", Outcome: outcome}}
	}
	f := &fakeTurns{answers: map[string][]rpc.Event{
		"Capital of Australia?": {
			{Type: rpc.EventRoute, Route: "direct"},
			{Type: rpc.EventToken, Text: "Canberra"},
			{Type: rpc.EventDone, DurationMillis: 1200},
		},
		"Latest Go?": {
			{Type: rpc.EventRoute, Route: "tools"},
			tool(rpc.EventToolCall, "web_search", ""),
			tool(rpc.EventToolResult, "web_search", "ok"),
			tool(rpc.EventToolCall, "obsidian.obsidian_read_note", ""),
			{Type: rpc.EventApproval, Approval: &rpc.Approval{ID: "p1", Name: "obsidian.obsidian_read_note", Kind: "mcp",
				Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}}},
			tool(rpc.EventToolResult, "obsidian.obsidian_read_note", "declined"),
			{Type: rpc.EventToken, Text: "Go 1.26"},
			{Type: rpc.EventDone, DurationMillis: 31000},
		},
		"Coase?": {
			{Type: rpc.EventRoute, Route: "search"},
			{Type: rpc.EventSources, Sources: []rpc.Citation{{N: 1, Path: "~/notes/coase.md"}, {N: 2, Path: "~/notes/coase.md"}}},
			{Type: rpc.EventToken, Text: "Transaction costs [1]."},
			{Type: rpc.EventDone, DurationMillis: 4000},
		},
	}}
	sock := startServer(t, f.handle)
	path := writeChecks(t,
		`{"id": "cap", "category": "direct", "question": "Capital of Australia?", "want": {"route": ["direct"], "no_tools": true, "answer_any": ["canberra"]}}`,
		`{"id": "web-go", "category": "web", "question": "Latest Go?", "want": {"tools": ["web_search", "obsidian"], "answer_any": ["1.27.1"]}}`,
		`{"id": "coase", "category": "retrieval", "question": "Coase?", "want": {"sources_any": ["coase"]}}`,
	)

	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"-socket", sock, "check", "--save", path}, &out, &errOut)
	if code != exitError {
		t.Errorf("exit code = %d, want %d (stderr %q)", code, exitError, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing: the summary reports the failure", errOut.String())
	}
	if !slices.Equal(f.approved, []rpc.Choice{rpc.ChoiceDeny}) {
		t.Errorf("approvals answered %v, want one deny", f.approved)
	}
	// The results go beside the socket, in checks-results.
	saved := filepath.Join(filepath.Dir(sock), "checks-results", time.Now().Format(time.DateOnly)+".jsonl")
	wantOut := `PASS  cap     direct     direct          1.2s  -
FAIL  web-go  web        tools          31.0s  web_search, obsidian_read_note
      tool obsidian not called
      answer lacks all of: 1.27.1
      approval denied: obsidian.obsidian_read_note
PASS  coase   retrieval  search          4.0s  -

direct     1/1
web        0/1
retrieval  1/1

2 of 3 passed in 0s
Results saved to ` + saved + "\n"
	if out.String() != wantOut {
		t.Errorf("stdout =\n%s\nwant\n%s", out.String(), wantOut)
	}

	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("saved %d lines, want 3", len(lines))
	}
	var r checkResult
	if err := json.Unmarshal([]byte(lines[1]), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != "web-go" || r.Pass || r.Answer != "Go 1.26" || r.Route != "tools" || r.Seconds != 31 ||
		len(r.Reasons) != 2 || r.Run == "" || r.Session == "" ||
		!slices.Equal(r.Denied, []string{"approval denied: obsidian.obsidian_read_note"}) {
		t.Errorf("saved record = %+v", r)
	}
	var coase checkResult
	if err := json.Unmarshal([]byte(lines[2]), &coase); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(coase.Sources, []string{"~/notes/coase.md"}) || coase.Run != r.Run {
		t.Errorf("coase record = %+v, want one source and the same run", coase)
	}
}

// TestCheckReadsToolSources checks that sources_any and sources_none read
// every "sources" event of a turn: the prompt's, and the later one merud
// sends after search_files returns excerpts, as an agentic turn does.
func TestCheckReadsToolSources(t *testing.T) {
	found := []rpc.Citation{{N: 3, Path: "~/notes/naur.md"}, {N: 4, Path: "~/notes/lisbon.md"}}
	f := &fakeTurns{answers: map[string][]rpc.Event{
		"Naur?": {
			{Type: rpc.EventRoute, Route: "search"},
			{Type: rpc.EventSources, Sources: []rpc.Citation{{N: 1, Path: "~/a.md"}, {N: 2, Path: "~/b.md"}}},
			{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: "1", Name: "search_files", Kind: "builtin"}},
			{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: "1", Name: "search_files", Kind: "builtin", Outcome: "ok", Sources: found}},
			{Type: rpc.EventSources, Sources: append([]rpc.Citation{{N: 1, Path: "~/a.md"}, {N: 2, Path: "~/b.md"}}, found...)},
			{Type: rpc.EventToken, Text: "A theory [3]."},
			{Type: rpc.EventDone, DurationMillis: 5000},
		},
	}}
	sock := startServer(t, f.handle)
	path := writeChecks(t,
		`{"id": "naur", "category": "retrieval", "question": "Naur?", "want": {"sources_any": ["naur"]}}`,
		`{"id": "naur-no-lisbon", "category": "retrieval", "question": "Naur?", "want": {"sources_none": ["lisbon"]}}`,
	)
	var out, errOut bytes.Buffer
	run(context.Background(), []string{"-socket", sock, "check", "--json", path}, &out, &errOut)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d results, want 2:\n%s", len(lines), out.String())
	}
	var any, none checkResult
	if err := json.Unmarshal([]byte(lines[0]), &any); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &none); err != nil {
		t.Fatal(err)
	}
	if !any.Pass || !slices.Equal(any.Sources, []string{"~/a.md", "~/b.md", "~/notes/naur.md", "~/notes/lisbon.md"}) {
		t.Errorf("sources_any result = %+v, want a pass with all four paths", any)
	}
	if none.Pass {
		t.Errorf("sources_none result passed, want a fail on ~/notes/lisbon.md from the tool's excerpts")
	}
}

func TestCheckJSONAndOnly(t *testing.T) {
	f := &fakeTurns{answers: map[string][]rpc.Event{
		"q1": {{Type: rpc.EventRoute, Route: "direct"}, {Type: rpc.EventToken, Text: "yes"},
			{Type: rpc.EventDone, DurationMillis: 1500, TTFTMillis: 400, TTLTMillis: 1450, TPOTMillis: 12.5, TokensIn: 900, TokensOut: 3}},
	}}
	sock := startServer(t, f.handle)
	path := writeChecks(t,
		`{"id": "one", "category": "direct", "question": "q1", "want": {"answer_all": ["yes"]}}`,
		`{"id": "two", "category": "web", "question": "q2"}`,
	)
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"-socket", sock, "check", "--json", "--only", "direct", path}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, errOut.String())
	}
	if len(f.requests) != 1 {
		t.Errorf("merud got %d questions, want 1", len(f.requests))
	}
	var r checkResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("stdout %q isn't one JSON record: %v", out.String(), err)
	}
	if r.ID != "one" || !r.Pass || r.Answer != "yes" || r.Seconds != 1.5 {
		t.Errorf("record = %+v", r)
	}
	// The record carries the model set in use and the done event's stats.
	if r.ModelSet != "test-set" || r.Model != "test-model" || r.TTFTMillis != 400 || r.TTLTMillis != 1450 ||
		r.TPOTMillis != 12.5 || r.TokensIn != 900 || r.TokensOut != 3 {
		t.Errorf("record stats = %+v, want test-set, test-model and the done event's numbers", r)
	}
}

func TestCheckErrors(t *testing.T) {
	sock := startServer(t, (&fakeTurns{}).handle)
	bad := writeChecks(t, `{"id": "a", "category": "c", "question": "q"}`, `{"id": "b", "question": "q"}`)
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"bad line", []string{"check", bad}, `line 2: missing "category"`},
		{"question, not a file", []string{"check", "my", "email"}, "usage: meru check"},
		{"missing file", []string{"check", "my"}, `put it in quotes`},
		{"default file missing", []string{"check"}, "no check file at " + filepath.Join(filepath.Dir(sock), "checks.jsonl")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != exitError || !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("exit %d, stderr %q; want 1 and %q", code, errOut.String(), tt.wantErr)
			}
		})
	}

	t.Run("no merud", func(t *testing.T) {
		path := writeChecks(t, `{"id": "a", "category": "c", "question": "q"}`)
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"-socket", filepath.Join(t.TempDir(), "none.sock"), "check", path}, &out, &errOut)
		if code != exitError || !strings.Contains(errOut.String(), "a: connect to merud") {
			t.Errorf("exit %d, stderr %q", code, errOut.String())
		}
	})
}
