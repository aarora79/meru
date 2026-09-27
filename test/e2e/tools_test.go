//go:build e2e

// This file tests tool calls end to end: merud starts cmd/fakemcp from
// config, the fake model calls its tools, dispatch runs or refuses each
// call, and the transcript, `meru log` and `meru tools` show what happened.

package e2e

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// toolsRoute scripts the router picking "tools" (letter C).
func toolsRoute() fakeollama.Reply {
	return routeReply(letter{"C", 0.9}, letter{"A", 0.05}, letter{"B", 0.03}, letter{"D", 0.02})
}

// startToolStack is startStack with cmd/fakemcp configured as the MCP server
// "notes": search runs freely, send asks first, secret isn't allowed.
func startToolStack(t *testing.T) *stack {
	t.Helper()
	f := startFake(t)
	h := newHome(t)
	servers := fmt.Sprintf(`[router]
temperature = 1.0

[[mcp.servers]]
name    = "notes"
command = %q
allow   = ["search", "send"]
confirm = ["send"]
`, filepath.Join(binDir, "fakemcp"))
	h.writeConfig(t, fakeConfig(f.url, servers))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)
	return &stack{home: h, fake: f, merud: m}
}

// TestToolCall is v0.3's "Done when": Meru answers a question by calling an
// MCP server. The model calls notes.search, reads the result, and answers.
func TestToolCall(t *testing.T) {
	t.Parallel()
	s := startToolStack(t)
	const answer = "Your garden plan: sow tomatoes on 12 April."
	s.fake.enqueue(t, fastModel, toolsRoute())
	s.fake.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "notes.search", Arguments: map[string]any{"query": "garden"}}}},
		fakeollama.Reply{Text: answer})

	res := runMeru(t, s.home, "when do I sow the tomatoes?")
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s\nmerud.log:\n%s", res.code, res.stderr, s.home.log())
	}
	if res.stdout != answer+"\n" {
		t.Errorf("stdout = %q, want only the answer", res.stdout)
	}
	if !strings.Contains(res.stderr, "notes.search") {
		t.Errorf("stderr lacks the tool line:\n%s", res.stderr)
	}

	// The model's second request must carry the tool's result.
	var sawResult bool
	for _, r := range s.fake.chatRequests(t, mainModel) {
		if strings.Contains(string(r.Body), "the garden plan for garden: sow tomatoes on 12 April") {
			sawResult = true
		}
	}
	if !sawResult {
		t.Errorf("no request to the model carried the tool result")
	}

	lines := readTranscript(t, sessionFiles(t, s.home)[0])
	var types []string
	for _, l := range lines {
		types = append(types, l.Type)
	}
	want := []string{transcript.TypeUser, transcript.TypeToolCall, transcript.TypeToolResult,
		transcript.TypeModelSwitch, transcript.TypeAssistant}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("transcript line types = %v, want %v", types, want)
	}
	call, result := lines[1], lines[2]
	if call.Kind != "mcp" || call.Server != "notes" || call.Tool != "search" || !strings.Contains(string(call.Args), "garden") {
		t.Errorf("tool_call line = %+v", call)
	}
	if result.Outcome != "ok" || !result.OK || result.CallID != call.CallID {
		t.Errorf("tool_result line = %+v", result)
	}

	log := runMeru(t, s.home, "log")
	if log.code != 0 || !strings.Contains(log.stdout, "notes.search") || !strings.Contains(log.stdout, "ok") {
		t.Errorf("meru log exited %d:\n%s%s", log.code, log.stdout, log.stderr)
	}
	tools := runMeru(t, s.home, "tools")
	if tools.code != 0 || !strings.Contains(tools.stdout, "notes.search") || !strings.Contains(tools.stdout, "notes.send") ||
		strings.Contains(tools.stdout, "notes.secret") {
		t.Errorf("meru tools exited %d:\n%s%s", tools.code, tools.stdout, tools.stderr)
	}
}

// TestToolRefused covers the two refusals: a tool in the confirm list, when
// nobody can approve it (meru's stdin isn't a terminal here), and a tool no
// allowlist names. Neither runs, and both reach the model as refusals.
func TestToolRefused(t *testing.T) {
	t.Parallel()
	s := startToolStack(t)
	s.fake.enqueue(t, fastModel, toolsRoute())
	s.fake.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{
			{Name: "notes.send", Arguments: map[string]any{"to": "sam", "text": "hi"}},
			{Name: "notes.secret", Arguments: map[string]any{}},
		}},
		fakeollama.Reply{Text: "I couldn't send it."})

	res := runMeru(t, s.home, "tell sam hi")
	if res.code != 0 {
		t.Fatalf("meru exited %d, stderr:\n%s\nmerud.log:\n%s", res.code, res.stderr, s.home.log())
	}
	outcomes := map[string]string{}
	calls := map[string]string{}
	for _, l := range readTranscript(t, sessionFiles(t, s.home)[0]) {
		switch l.Type {
		case transcript.TypeToolCall:
			calls[l.CallID] = l.Server + "." + l.Tool
		case transcript.TypeToolResult:
			outcomes[calls[l.CallID]] = l.Outcome
		}
	}
	if outcomes["notes.send"] != "declined" || outcomes["notes.secret"] != "denied" {
		t.Errorf("outcomes = %v, want send declined and secret denied", outcomes)
	}
	for _, r := range s.fake.chatRequests(t, mainModel) {
		if strings.Contains(string(r.Body), "you should not see this") || strings.Contains(string(r.Body), "sent to sam") {
			t.Errorf("a refused tool ran; the model saw its result")
		}
	}
}

// TestWebSearchMissingSearXNG runs merud with [web] searxng_url pointing at
// a port nothing listens on. merud starts anyway and logs why web search
// isn't ready; `meru tools` lists web_search and web_fetch; and a turn in
// which the model calls web_search completes, with the tool's error in
// front of the model.
func TestWebSearchMissingSearXNG(t *testing.T) {
	t.Parallel()
	// Take a free port and close it, so nothing answers there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	searxng := "http://" + ln.Addr().String()
	ln.Close()

	f := startFake(t)
	h := newHome(t)
	cfg := strings.Replace(fakeConfig(f.url, "[router]\ntemperature = 1.0\n"),
		`searxng_url = ""`, fmt.Sprintf("searxng_url = %q", searxng), 1)
	h.writeConfig(t, cfg)
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)
	s := &stack{home: h, fake: f, merud: m}
	if !strings.Contains(h.log(), "web search not ready") {
		t.Errorf("merud.log lacks the web search line:\n%s", h.log())
	}

	const answer = "I couldn't search the web: SearXNG isn't running."
	s.fake.enqueue(t, fastModel, toolsRoute())
	s.fake.enqueue(t, mainModel,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: "web_search", Arguments: map[string]any{"query": "latest Go release"}}}},
		fakeollama.Reply{Text: answer})

	res := runMeru(t, s.home, "search the web for the latest Go release")
	if res.code != 0 || res.stdout != answer+"\n" {
		t.Fatalf("meru exited %d, stdout %q, stderr:\n%s\nmerud.log:\n%s", res.code, res.stdout, res.stderr, s.home.log())
	}
	want := "SearXNG isn't answering on " + searxng
	var sawError bool
	for _, r := range s.fake.chatRequests(t, mainModel) {
		if strings.Contains(string(r.Body), want) {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("no request to the model carried %q", want)
	}
	for _, l := range readTranscript(t, sessionFiles(t, s.home)[0]) {
		switch {
		case l.Type == transcript.TypeToolCall && (l.Tool != "web_search" || l.Server != "meru" || l.Kind != "builtin"):
			t.Errorf("tool_call line = %+v, want the built-in web_search", l)
		case l.Type == transcript.TypeToolResult && l.Outcome != "error":
			t.Errorf("tool_result line = %+v, want outcome error", l)
		}
	}

	tools := runMeru(t, s.home, "tools")
	// [builtin] tools lists both web tools by default, so meru tools does too.
	if tools.code != 0 || !strings.Contains(tools.stdout, "web_search") || !strings.Contains(tools.stdout, "web_fetch") {
		t.Errorf("meru tools exited %d:\n%s%s", tools.code, tools.stdout, tools.stderr)
	}
}

// TestBuiltinToolsSwitch runs merud with [builtin] tools cut down to
// datetime and grep, and no [index] folders. `meru tools` lists datetime
// alone: configure and web_fetch are left out, and grep stays off for
// want of a folder, with a line in merud.log that says why.
func TestBuiltinToolsSwitch(t *testing.T) {
	t.Parallel()
	f := startFake(t)
	h := newHome(t)
	h.writeConfig(t, fakeConfig(f.url, "[builtin]\ntools = [\"datetime\", \"grep\"]\nconfirm = []\n"))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)

	tools := runMeru(t, h, "tools")
	if tools.code != 0 || !strings.Contains(tools.stdout, "datetime") {
		t.Fatalf("meru tools exited %d:\n%s%s", tools.code, tools.stdout, tools.stderr)
	}
	for _, off := range []string{"configure", "web_fetch", "grep", "remember"} {
		if strings.Contains(tools.stdout, off) {
			t.Errorf("meru tools lists %s, which is off:\n%s", off, tools.stdout)
		}
	}
	if log := h.log(); !strings.Contains(log, "built-in tool off") || !strings.Contains(log, "[index] folders is empty") {
		t.Errorf("merud.log doesn't say why grep is off:\n%s", log)
	}
}
