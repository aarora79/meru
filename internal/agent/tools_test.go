// This file tests the tool rounds with a fake engine that scripts tool calls
// round by round and a fake ToolRunner that stands in for dispatch: which
// routes offer tools, the event order, parallel calls, the round cap,
// outcomes, approvals, sources, cancellation and the transcript.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// fakeResult scripts how the fake ToolRunner answers one tool, by name.
type fakeResult struct {
	text    string
	outcome string // "" means "ok"
	// wait, when set, holds the call until the channel closes; done, when
	// set, is closed as the call returns. Tests pair them to prove two
	// calls run at the same time.
	wait chan struct{}
	done chan struct{}
	// block holds the call until ctx ends, then returns "cancelled".
	block bool
	// ask makes the call ask the user through Call.Approve first.
	ask bool
}

// fakeTools stands in for dispatch. It offers specs, answers each call from
// results, writes a tool_call and a tool_result line through Call.Append as
// dispatch would, and records every Call.
type fakeTools struct {
	specs   []engine.ToolSpec
	results map[string]fakeResult
	asks    map[string]bool // tools that ask first; missing means no
	// late holds the tools of a server that isn't connected yet; the
	// first Refresh adds them to specs, as a server the user just
	// started would.
	late []engine.ToolSpec

	mu       sync.Mutex // guards calls, choices and connects
	calls    []dispatch.Call
	choices  []rpc.Choice
	connects int // how many times Refresh ran
}

func (f *fakeTools) Tools() []engine.ToolSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.specs)
}

// Refresh counts the call and brings in the late tools.
func (f *fakeTools) Refresh(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	f.specs = append(f.specs, f.late...)
	f.late = nil
}

func (f *fakeTools) Asks(name string) bool { return f.asks[name] }

func (f *fakeTools) Dispatch(ctx context.Context, c dispatch.Call) (dispatch.Result, dispatch.Outcome) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	r := f.results[c.Name]
	if r.done != nil {
		defer close(r.done)
	}
	_ = c.Append(transcript.Line{Type: transcript.TypeToolCall, CallID: c.ID, Tool: c.Name, Args: c.Args, Caller: c.Caller})
	// A call to a tool the fake doesn't offer, with a hint, gets the hint
	// back as a denied call, as dispatch gives it.
	if c.Hint != "" && !slices.ContainsFunc(f.Tools(), func(s engine.ToolSpec) bool { return s.Name == c.Name }) {
		_ = c.Append(transcript.Line{Type: transcript.TypeToolResult, CallID: c.ID, Outcome: dispatch.OutcomeDenied, Result: c.Hint})
		return dispatch.Result{Text: c.Hint, IsError: true}, dispatch.Outcome{Outcome: dispatch.OutcomeDenied}
	}
	outcome := r.outcome
	if outcome == "" {
		outcome = dispatch.OutcomeOK
	}
	if r.ask {
		choice, err := c.Approve(ctx, rpc.Approval{ID: "a-" + c.ID, Name: c.Name, Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}})
		if err != nil {
			choice = rpc.ChoiceDeny
		}
		f.mu.Lock()
		f.choices = append(f.choices, choice)
		f.mu.Unlock()
	}
	if r.wait != nil {
		<-r.wait
	}
	if r.block {
		<-ctx.Done()
		outcome = dispatch.OutcomeCancelled
	}
	_ = c.Append(transcript.Line{Type: transcript.TypeToolResult, CallID: c.ID, Outcome: outcome, Result: r.text})
	return dispatch.Result{Text: r.text}, dispatch.Outcome{Outcome: outcome, Duration: 5 * time.Millisecond}
}

// recorded returns a copy of the calls so far.
func (f *fakeTools) recorded() []dispatch.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// spec builds a tool schema.
func spec(name string) engine.ToolSpec {
	return engine.ToolSpec{Name: name, Description: "does " + name, Parameters: json.RawMessage(`{"type":"object"}`)}
}

// call builds a tool call with JSON arguments.
func call(name, args string) engine.ToolCall {
	return engine.ToolCall{Name: name, Arguments: json.RawMessage(args)}
}

// types lists the events' types.
func types(evs []rpc.Event) []rpc.EventType {
	out := make([]rpc.EventType, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// toolsAgent builds an agent on route over eng and tools, with no search.
func toolsAgent(t *testing.T, route string, eng *fakeEngine, tools ToolRunner) *Agent {
	t.Helper()
	return New(testConfig(t), eng, &fakeRouter{dec: Decision{Route: route, Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())
}

func TestToolsOfferedByRoute(t *testing.T) {
	all := &fakeTools{specs: []engine.ToolSpec{spec("notes.search"), spec("read_file"), spec("remember"), spec("list_folder"), spec("grep")}}
	noFiles := &fakeTools{specs: []engine.ToolSpec{spec("notes.search")}}
	// Two commands: git-log reads, git-push asks first.
	cmds := &fakeTools{
		specs: []engine.ToolSpec{spec("notes.search"), spec("read_file"), spec("cmd.git-log"), spec("cmd.git-push"), spec("grep")},
		asks:  map[string]bool{"cmd.git-push": true},
	}
	onlyCmds := &fakeTools{specs: []engine.ToolSpec{spec("configure"), spec("cmd.git-log")}}
	// datetime and about_meru ride along on every route, direct included.
	everyRoute := &fakeTools{specs: []engine.ToolSpec{spec("notes.search"), spec("datetime"), spec("about_meru"), spec("read_file")}}
	tests := []struct {
		name  string
		route string
		tools ToolRunner
		want  []string // the tools offered, in order
		note  string   // the tools note in the system prompt; "" for none
	}{
		{"direct offers none", "direct", all, nil, ""},
		{"direct offers datetime and about_meru", "direct", everyRoute, []string{"datetime", "about_meru"}, toolsNote},
		{"search offers datetime and about_meru", "search", everyRoute, []string{"datetime", "about_meru", "read_file"}, toolsNote + " " + fileToolsNote},
		{"search offers the file tools", "search", all, []string{"read_file", "list_folder", "grep"}, fileToolsNote},
		{"search with no file tools", "search", noFiles, nil, ""},
		{"search offers commands that don't ask", "search", cmds, []string{"read_file", "cmd.git-log", "grep"}, fileToolsNote + " " + commandsNote},
		{"search with commands and no file tools", "search", onlyCmds, []string{"cmd.git-log"}, commandsNote},
		{"tools offers every command", "tools", cmds, []string{"notes.search", "read_file", "cmd.git-log", "cmd.git-push", "grep"}, toolsNote + " " + fileToolsNote},
		{"tools offers all", "tools", all, []string{"notes.search", "read_file", "remember", "list_folder", "grep"}, toolsNote + " " + fileToolsNote},
		{"search+tools offers all", "search+tools", all, []string{"notes.search", "read_file", "remember", "list_folder", "grep"}, toolsNote + " " + fileToolsNote},
		{"no runner", "tools", nil, nil, ""},
		{"no allowed tools", "search+tools", &fakeTools{}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"ok"}}
			if _, err := run(context.Background(), toolsAgent(t, tt.route, eng, tt.tools), rpc.Request{Text: "hi"}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			c := eng.lastCall()
			var got []string
			for _, s := range c.tools {
				got = append(got, s.Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("tools offered = %v, want %v", got, tt.want)
			}
			system := c.msgs[0].Content
			for _, note := range []string{toolsNote, fileToolsNote, commandsNote} {
				want := strings.Contains(tt.note, note)
				if has := strings.Contains(system, note); has != want {
					t.Errorf("system prompt holds %q = %v, want %v", note, has, want)
				}
			}
		})
	}
}

func TestToolRoundThenAnswer(t *testing.T) {
	cfg := testConfig(t)
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("notes.search", `{"q":"garden"}`)}, usage: engine.Usage{PromptTokens: 10, OutputTokens: 2, EvalDuration: 20 * time.Millisecond}},
		{pieces: []string{"Sow them ", "on 12 April."}, usage: engine.Usage{PromptTokens: 30, OutputTokens: 5, EvalDuration: 50 * time.Millisecond}},
	}}
	tools := &fakeTools{
		specs:   []engine.ToolSpec{spec("notes.search")},
		results: map[string]fakeResult{"notes.search": {text: "garden.md: sow tomatoes on 12 April"}},
	}
	search := &fakeSearcher{results: []retrieve.Result{result("/srv/plan.md", "", "Plant tomatoes in May.", 1, 1, 0.02)}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "search+tools", Confidence: 0.9, Outcome: "ok"}}, search, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "When do I sow the tomatoes?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	want := []rpc.EventType{rpc.EventSession, rpc.EventRoute, rpc.EventSources,
		rpc.EventToolCall, rpc.EventToolResult, rpc.EventToken, rpc.EventToken, rpc.EventDone}
	if got := types(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v\nwant %v", got, want)
	}
	wantCall := rpc.ToolEvent{ID: "call-1", Name: "notes.search", Kind: "mcp", Args: json.RawMessage(`{"q":"garden"}`)}
	if got := evs[3].Tool; got == nil || !reflect.DeepEqual(*got, wantCall) {
		t.Errorf("tool_call = %+v, want %+v", got, wantCall)
	}
	if got := evs[4].Tool; got == nil || got.ID != "call-1" || got.Outcome != "ok" || got.DurationMillis != 5 || got.Args != nil {
		t.Errorf("tool_result = %+v, want call-1, ok, 5ms, no args", got)
	}
	// The stats sum over both rounds.
	if done := evs[7]; done.TokensIn != 40 || done.TokensOut != 7 || done.EvalMillis != 70 {
		t.Errorf("done = %+v, want 40 tokens in, 7 out, 70ms", done)
	}

	if n := len(eng.calls); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	// The second round reads the first round's call and its result.
	msgs := eng.lastCall().msgs
	tail := msgs[len(msgs)-2:]
	if tail[0].Role != engine.RoleAssistant || len(tail[0].ToolCalls) != 1 || tail[0].ToolCalls[0].Name != "notes.search" {
		t.Errorf("second last message = %+v, want the assistant's tool call", tail[0])
	}
	if tail[1].Role != engine.RoleTool || tail[1].ToolName != "notes.search" || tail[1].Content != "garden.md: sow tomatoes on 12 April" {
		t.Errorf("last message = %+v, want the tool result", tail[1])
	}
	if len(eng.lastCall().tools) != 1 {
		t.Errorf("second round offered %d tools, want 1", len(eng.lastCall().tools))
	}

	c := tools.recorded()[0]
	if c.ID != "call-1" || c.Session != evs[0].Session || c.Source != rpc.SourceCLI || string(c.Args) != `{"q":"garden"}` || c.Append == nil {
		t.Errorf("dispatch.Call = %+v", c)
	}
}

func TestParallelCallsKeepCallOrder(t *testing.T) {
	// "slow" waits until "fast" has finished. Run one at a time, in call
	// order, the turn would hang.
	gate := make(chan struct{})
	tools := &fakeTools{
		specs: []engine.ToolSpec{spec("slow"), spec("fast")},
		results: map[string]fakeResult{
			"slow": {text: "slow result", wait: gate},
			"fast": {text: "fast result", done: gate},
		},
	}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("slow", `{}`), {ID: "engine-7", Name: "fast"}}},
		{pieces: []string{"both done"}},
	}}
	evs, err := run(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "go"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var results []string
	var callIDs []string
	for _, ev := range evs {
		switch ev.Type {
		case rpc.EventToolCall:
			callIDs = append(callIDs, ev.Tool.ID)
		case rpc.EventToolResult:
			results = append(results, ev.Tool.ID)
		}
	}
	// The engine's own ID wins; the counter still moves past it.
	if want := []string{"call-1", "engine-7"}; !slices.Equal(callIDs, want) {
		t.Errorf("tool_call IDs = %v, want %v", callIDs, want)
	}
	// Each call reports one result. Which arrives first isn't fixed: "fast"
	// frees "slow" from inside its own call, so "slow" can finish and report
	// before the loop reports "fast". That both ran at once is shown by the
	// turn finishing at all. The model still gets the results in call
	// order, checked below.
	slices.Sort(results)
	if want := []string{"call-1", "engine-7"}; !slices.Equal(results, want) {
		t.Errorf("tool_result IDs = %v, want one result for each of %v", results, want)
	}
	msgs := eng.lastCall().msgs
	tail := msgs[len(msgs)-2:]
	if tail[0].ToolName != "slow" || tail[0].Content != "slow result" || tail[1].ToolName != "fast" || tail[1].Content != "fast result" {
		t.Errorf("tool messages = %+v, want slow then fast, in call order", tail)
	}
	// A call with no arguments reaches dispatch as an empty object.
	for _, c := range tools.recorded() {
		if c.Name == "fast" && string(c.Args) != `{}` {
			t.Errorf("fast args = %s, want {}", c.Args)
		}
	}
}

func TestRoundCapForcesAnswer(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agent.MaxRounds = 3
	// The model asks for a tool every round, each time with new arguments,
	// and writes text only when it has no tools left.
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("loop", `{"n":1}`)}},
		{calls: []engine.ToolCall{call("loop", `{"n":2}`)}},
		{pieces: []string{"Here is what I have."}, calls: []engine.ToolCall{call("loop", `{"n":3}`)}},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("loop")}, results: map[string]fakeResult{"loop": {text: "again"}}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "loop forever"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(eng.calls); n != 3 {
		t.Fatalf("model called %d times, want 3", n)
	}
	for i, c := range eng.calls {
		if offered := len(c.tools) > 0; offered != (i < 2) {
			t.Errorf("round %d offered tools = %v, want %v", i+1, offered, i < 2)
		}
	}
	if n := len(tools.recorded()); n != 2 {
		t.Errorf("dispatch ran %d calls, want 2: the last round's call is ignored", n)
	}
	lines := readLines(t, cfg, evs[0].Session)
	if last := lines[len(lines)-1]; last.Type != transcript.TypeAssistant || last.Text != "Here is what I have." {
		t.Errorf("last transcript line = %+v, want the forced answer", last)
	}
}

func TestDeniedAndDeclinedReachTheModel(t *testing.T) {
	tools := &fakeTools{
		specs: []engine.ToolSpec{spec("google.send_gmail_message")},
		results: map[string]fakeResult{
			"shell.run":                 {text: "shell.run isn't allowed", outcome: dispatch.OutcomeDenied},
			"google.send_gmail_message": {text: "the user said no", outcome: dispatch.OutcomeDeclined},
		},
	}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("shell.run", `{"cmd":"ls"}`), call("google.send_gmail_message", `{"to":"sam@example.com"}`)}},
		{pieces: []string{"I couldn't do that."}},
	}}
	evs, err := run(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "sell"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	outcomes := map[string]string{}
	for _, ev := range evs {
		if ev.Type == rpc.EventToolResult {
			outcomes[ev.Tool.Name] = ev.Tool.Outcome
		}
	}
	if outcomes["shell.run"] != "denied" || outcomes["google.send_gmail_message"] != "declined" {
		t.Errorf("outcomes = %v, want denied and declined", outcomes)
	}
	msgs := eng.lastCall().msgs
	tail := msgs[len(msgs)-2:]
	want := []engine.Message{
		{Role: engine.RoleTool, ToolName: "shell.run", Content: "shell.run isn't allowed"},
		{Role: engine.RoleTool, ToolName: "google.send_gmail_message", Content: "the user said no"},
	}
	if !reflect.DeepEqual(tail, want) {
		t.Errorf("tool messages = %+v\nwant %+v", tail, want)
	}
}

func TestApprovePassedThrough(t *testing.T) {
	tools := &fakeTools{
		specs:   []engine.ToolSpec{spec("google.send_gmail_message")},
		results: map[string]fakeResult{"google.send_gmail_message": {text: "placed", ask: true}},
	}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("google.send_gmail_message", `{"to":"sam@example.com"}`)}},
		{pieces: []string{"Done."}},
	}}
	var asked []rpc.Approval
	approve := func(ctx context.Context, ap rpc.Approval) (rpc.Choice, error) {
		asked = append(asked, ap)
		return rpc.ChoiceSession, nil
	}
	if _, err := runApprove(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "sell"}, approve); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(asked) != 1 || asked[0].Name != "google.send_gmail_message" {
		t.Errorf("approve saw %+v, want one question about send_gmail_message", asked)
	}
	if !slices.Equal(tools.choices, []rpc.Choice{rpc.ChoiceSession}) {
		t.Errorf("dispatch got choices %v, want [session]", tools.choices)
	}
}

func TestJobSourcePassedThrough(t *testing.T) {
	tools := &fakeTools{specs: []engine.ToolSpec{spec("remember")}, results: map[string]fakeResult{"remember": {text: "saved"}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("remember", `{"text":"x"}`)}},
		{pieces: []string{"ok"}},
	}}
	if _, err := run(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "note it", Source: rpc.SourceJob}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	c := tools.recorded()[0]
	if c.Source != rpc.SourceJob {
		t.Errorf("Call.Source = %q, want job", c.Source)
	}
	if c.Approve != nil {
		t.Error("Call.Approve is set, want nil: Handle got no approve function")
	}
}

func TestCancelledDuringToolCall(t *testing.T) {
	cfg := testConfig(t)
	tools := &fakeTools{specs: []engine.ToolSpec{spec("slow.op")}, results: map[string]fakeResult{"slow.op": {block: true}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("slow.op", `{}`)}},
		{pieces: []string{"never"}},
	}}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, tools, nil, nil, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var id string
	err := a.Handle(ctx, rpc.Request{Text: "q"}, func(ev rpc.Event) error {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case rpc.EventSession:
			id = ev.Session
		case rpc.EventToolCall:
			cancel() // as if the client hung up while the tool ran
		}
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle error = %v, want context.Canceled", err)
	}
	if n := len(eng.calls); n != 1 {
		t.Errorf("model called %d times, want 1", n)
	}
	var got []string
	for _, l := range readLines(t, cfg, id) {
		got = append(got, l.Type)
	}
	// The user line from the agent, the tool lines from dispatch, and no
	// assistant line.
	if want := []string{"user", "tool_call", "tool_result"}; !slices.Equal(got, want) {
		t.Errorf("transcript types = %v, want %v", got, want)
	}
}

func TestTranscriptAndHistoryAfterToolTurn(t *testing.T) {
	cfg := testConfig(t)
	tools := &fakeTools{specs: []engine.ToolSpec{spec("notes.search")}, results: map[string]fakeResult{"notes.search": {text: "raw result"}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{pieces: []string{"Let me look. "}, calls: []engine.ToolCall{call("notes.search", `{}`)}},
		{pieces: []string{"Found it."}},
	}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "find it"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	id := evs[0].Session
	var got []string
	for _, l := range allLines(t, cfg, id) {
		got = append(got, l.Type)
	}
	// The agent writes the user line, the model_switch line that names the
	// session's first answer model, and one assistant line; the tool lines
	// between them come from dispatch (here, the fake).
	if want := []string{"user", "tool_call", "tool_result", "model_switch", "assistant"}; !slices.Equal(got, want) {
		t.Fatalf("transcript types = %v, want %v", got, want)
	}
	lines := readLines(t, cfg, id)
	if l := lines[3]; l.Text != "Found it." {
		t.Errorf("assistant line = %q, want the final round's text", l.Text)
	}

	// The next turn's history holds the question and the answer only.
	eng.rounds = nil
	eng.pieces = []string{"ok"}
	router.dec = Decision{Route: "direct", Confidence: 1, Outcome: "ok"}
	if _, err := run(context.Background(), a, rpc.Request{Session: id, Text: "thanks"}); err != nil {
		t.Fatal(err)
	}
	want := []engine.Message{
		{Role: engine.RoleUser, Content: "find it"},
		{Role: engine.RoleAssistant, Content: "Found it."},
	}
	if !reflect.DeepEqual(router.history, want) {
		t.Errorf("history = %+v\nwant %+v", router.history, want)
	}
}

// TestToolTurnSpans checks that each round gets its own gen_ai.chat span
// and that the turn span counts the rounds as its iterations.
func TestToolTurnSpans(t *testing.T) {
	rec := recordSpans(t)
	tools := &fakeTools{specs: []engine.ToolSpec{spec("notes.search")}, results: map[string]fakeResult{"notes.search": {text: "r"}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("notes.search", `{}`)}},
		{pieces: []string{"done"}},
	}}
	if _, err := run(context.Background(), toolsAgent(t, "tools", eng, tools), rpc.Request{Text: "q"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	st := spanTree{spans: rec.Ended()}
	turn := st.find(t, "meru.turn", attribute.Int("meru.turn.iterations", 2))
	chats := 0
	for _, name := range st.children(turn) {
		if name == "gen_ai.chat" {
			chats++
		}
	}
	if chats != 2 {
		t.Errorf("turn has %d gen_ai.chat spans, want 2: %v", chats, st.children(turn))
	}
}

func TestToolKind(t *testing.T) {
	tests := []struct{ name, want string }{
		{"a2a.research.summarize", "a2a"},
		{"google.search_gmail_messages", "mcp"},
		{"remember", "builtin"},
		{"a2ax.tool", "mcp"},
		{"cmd.git-log", "command"},
		{"cmdx.tool", "mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolKind(tt.name); got != tt.want {
				t.Errorf("toolKind(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestQuestionNamingAToolServerGetsTools covers the route override: a
// question that names a connected server gets that server's tools, even
// when the router picked a route without tools.
func TestQuestionNamingAToolServerGetsTools(t *testing.T) {
	tools := &fakeTools{specs: []engine.ToolSpec{spec("obsidian.obsidian_simple_search"), spec("a2a.research.summarize"), spec("configure")}}
	tests := []struct {
		route, question, wantRoute string
	}{
		{"search", "search my Obsidian vault for AI", "search+tools"},
		{"direct", "ask research to summarize this", "tools"},
		{"direct", "what is the capital of France", "direct"},
		{"direct", "meru, can you configure things", "direct"}, // built-ins name no server
		{"tools", "search obsidian", "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			eng := &fakeEngine{pieces: []string{"ok"}}
			evs, err := run(context.Background(), toolsAgent(t, tt.route, eng, tools), rpc.Request{Text: tt.question})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			for _, ev := range evs {
				if ev.Type == rpc.EventRoute && ev.Route != tt.wantRoute {
					t.Errorf("route = %q, want %q", ev.Route, tt.wantRoute)
				}
			}
			if got, want := len(eng.lastCall().tools) > 0, tt.wantRoute != "direct"; got != want {
				t.Errorf("tools offered = %v, want %v", got, want)
			}
		})
	}
}

func TestToolServers(t *testing.T) {
	got := toolServers([]engine.ToolSpec{spec("Obsidian.search"), spec("obsidian.read"), spec("a2a.research.summarize"), spec("configure")})
	if want := []string{"obsidian", "research"}; !slices.Equal(got, want) {
		t.Errorf("toolServers = %q, want %q", got, want)
	}
}

// TestTurnConnectsMissingServersOnce checks the connect-on-demand rule
// from the agent's side: a turn that offers tools asks the tool runner to
// reach missing servers once, before it lists the tools, so a server that
// connects then is offered in the same turn. The search route offers the
// file tools but never a server's, so it doesn't wait on a server; a turn
// that offers no tools never asks either.
func TestTurnConnectsMissingServersOnce(t *testing.T) {
	tests := []struct {
		route        string
		wantConnects int
		wantOffered  []string
	}{
		{"tools", 1, []string{"read_file", "notes.search", "google.search_gmail_messages"}},
		{"search+tools", 1, []string{"read_file", "notes.search", "google.search_gmail_messages"}},
		{"search", 0, []string{"read_file"}},
		{"direct", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.route, func(t *testing.T) {
			tools := &fakeTools{
				specs: []engine.ToolSpec{spec("read_file"), spec("notes.search")},
				late:  []engine.ToolSpec{spec("google.search_gmail_messages")},
			}
			eng := &fakeEngine{rounds: []fakeRound{{pieces: []string{"ok"}}}}
			a := toolsAgent(t, tt.route, eng, tools)
			if _, err := run(context.Background(), a, rpc.Request{Text: "what did we agree on the launch date?"}); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if tools.connects != tt.wantConnects {
				t.Errorf("Refresh ran %d times, want %d", tools.connects, tt.wantConnects)
			}
			var offered []string
			for _, s := range eng.lastCall().tools {
				offered = append(offered, s.Name)
			}
			if !slices.Equal(offered, tt.wantOffered) {
				t.Errorf("offered %v, want %v", offered, tt.wantOffered)
			}
		})
	}
}
