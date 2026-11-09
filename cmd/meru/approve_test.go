// This file tests what one-shot `meru "..."` does about tools: the approval
// prompt, with a scripted reader in place of the keyboard, and the tool
// lines on stderr, against an in-process rpc server that plays merud.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// allChoices is what merud offers for an ordinary tool in a confirm list.
var allChoices = []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}

// toolTurn returns a handler that plays one turn with a tool call that asks
// first: a tool_call event, the approval, a tool_result whose outcome
// follows the answer, then the answer "Done." with no trailing new line.
func toolTurn(choices []rpc.Choice) rpc.Handler {
	return func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
		args := json.RawMessage(`{"to":"sam@example.com","subject":"Garden"}`)
		emit(rpc.Event{Type: rpc.EventSession, Session: "s"})
		emit(rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: "1", Name: "mail.send", Kind: "mcp", Args: args}})
		c, err := approve(ctx, rpc.Approval{Name: "mail.send", Kind: "mcp", Args: args, Choices: choices})
		if err != nil {
			return err
		}
		result := &rpc.ToolEvent{ID: "1", Name: "mail.send", Kind: "mcp", Outcome: "ok", DurationMillis: 120}
		if c == rpc.ChoiceDeny {
			result.Outcome, result.DurationMillis = "declined", 0
		}
		emit(rpc.Event{Type: rpc.EventToolResult, Tool: result})
		return emit(rpc.Event{Type: rpc.EventToken, Text: "Done (" + string(c) + ")."})
	}
}

func TestAskApproval(t *testing.T) {
	tests := []struct {
		name     string
		input    string // what the user types
		terminal bool
		choices  []rpc.Choice
		wantOut  string
		wantErr  []string // pieces stderr must hold, in order
	}{
		{"once", "o\n", true, allChoices, "Done (once).\n",
			[]string{`→ mail.send {"to":"sam@example.com","subject":"Garden"}`, "Meru wants to run mail.send (mcp) with:",
				`  "to": "sam@example.com",`, "Run mail.send? [o]nce  [s]ession  [d]eny: ", "✓ mail.send 120 ms"}},
		{"whole word, any case", "SESSION\n", true, allChoices, "Done (session).\n", []string{"✓ mail.send 120 ms"}},
		{"empty and unknown ask again", "\nx\nd\n", true, allChoices, "Done (deny).\n",
			[]string{"[d]eny: Run mail.send? [o]nce  [s]ession  [d]eny: Run mail.send?", "✗ mail.send declined"}},
		{"only the choices offered", "s\no\n", true, []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}, "Done (once).\n",
			[]string{"Run mail.send? [o]nce  [d]eny: Run mail.send? [o]nce  [d]eny: "}},
		{"end of input denies", "", true, allChoices, "Done (deny).\n", []string{"Denied mail.send: no answer.", "✗ mail.send declined"}},
		{"not a terminal denies without asking", "o\n", false, allChoices, "Done (deny).\n",
			[]string{"Denied mail.send: standard input isn't a terminal, so nobody could approve it.", "✗ mail.send declined"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := startServer(t, toolTurn(tt.choices))
			var out, errOut bytes.Buffer
			p := newPrompter(strings.NewReader(tt.input), &errOut, tt.terminal)
			if err := ask(context.Background(), sock, "email sam", "", false, &out, &errOut, p.approve); err != nil {
				t.Fatalf("ask: %v (stderr %q)", err, errOut.String())
			}
			// stdout holds the answer and nothing else.
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			rest := errOut.String()
			for _, want := range tt.wantErr {
				i := strings.Index(rest, want)
				if i < 0 {
					t.Fatalf("stderr lacks %q after the earlier pieces:\n%s", want, errOut.String())
				}
				rest = rest[i+len(want):]
			}
			if !tt.terminal && strings.Contains(errOut.String(), "Run mail.send?") {
				t.Errorf("prompted without a terminal:\n%s", errOut.String())
			}
		})
	}
}

// TestAskToolLinesAfterText checks that a tool line printed while the
// answer sits mid-line starts on a line of its own, and that the new line
// goes to stderr, not into the answer.
func TestAskToolLinesAfterText(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		emit(rpc.Event{Type: rpc.EventToken, Text: "Let me look"})
		emit(rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{ID: "1", Name: "notes.search", Kind: "mcp", Args: json.RawMessage(`{"query":"garden"}`)}})
		emit(rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{ID: "1", Name: "notes.search", Kind: "mcp", Outcome: "ok", DurationMillis: 1500}})
		return emit(rpc.Event{Type: rpc.EventToken, Text: " and found it."})
	})
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "garden"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errOut.String())
	}
	if want := "Let me look and found it.\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if want := "\n→ notes.search {\"query\":\"garden\"}\n✓ notes.search 1.5s\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestAskNotice checks that merud's notice prints on stderr after the
// answer, as a "note:" line, and stays out of stdout.
func TestAskNotice(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		emit(rpc.Event{Type: rpc.EventToken, Text: "Done. It's now at ~/Projects/garden/."})
		return emit(rpc.Event{Type: rpc.EventNotice, Text: "Meru didn't run any tool for this answer, so nothing changed on your computer."})
	})
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "move the garden folder to ~/Projects"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errOut.String())
	}
	if want := "Done. It's now at ~/Projects/garden/.\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if want := "note: Meru didn't run any tool for this answer, so nothing changed on your computer.\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestAskSignIn checks that a tool server waiting for a sign-in prints a
// "note:" line on stderr with the command that signs in.
func TestAskSignIn(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		emit(rpc.Event{Type: rpc.EventSignInNeeded, Text: "github needs you to sign in before Meru can use its tools.",
			Servers: []rpc.ServerInfo{{Name: "github", Kind: "mcp"}}})
		return emit(rpc.Event{Type: rpc.EventToken, Text: "I can't reach GitHub."})
	})
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "how many stars does garden-planner have?"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errOut.String())
	}
	if want := "I can't reach GitHub.\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if want := "note: github needs you to sign in before Meru can use its tools. Run: meru mcp login github\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

func TestToolLine(t *testing.T) {
	tests := []struct {
		name string
		ev   rpc.Event
		want string
	}{
		{"call", rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{Name: "notes.search", Args: json.RawMessage(`{"query":"garden"}`)}}, `→ notes.search {"query":"garden"}`},
		{"call without args", rpc.Event{Type: rpc.EventToolCall, Tool: &rpc.ToolEvent{Name: "remember"}}, "→ remember"},
		{"ok", rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{Name: "notes.search", Outcome: "ok", DurationMillis: 120}}, "✓ notes.search 120 ms"},
		{"timeout", rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{Name: "notes.search", Outcome: "timeout"}}, "✗ notes.search timeout"},
		{"progress", rpc.Event{Type: rpc.EventProgress, Text: "Installing Meru's page reader (about 95 MB, once)", Tool: &rpc.ToolEvent{ID: "c1", Name: "web_fetch"}},
			"  web_fetch · Installing Meru's page reader (about 95 MB, once)"},
		{"no tool", rpc.Event{Type: rpc.EventToolCall}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolLine(tt.ev); got != tt.want {
				t.Errorf("toolLine = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApproveCancelled checks that Ctrl-C while the prompt waits ends the
// wait with the context's error instead of a choice.
func TestApproveCancelled(t *testing.T) {
	// A pipe nobody writes to blocks a read, like a terminal nobody types at.
	r, w := io.Pipe()
	defer w.Close()
	var errOut bytes.Buffer
	p := newPrompter(r, &errOut, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.approve(ctx, rpc.Approval{Name: "mail.send", Choices: allChoices}); err == nil {
		t.Error("approve returned no error after Ctrl-C")
	}
}

// TestSiteChoices checks the website choices in the prompt: each names the
// host and has a key of its own, apart from "s" for this session.
func TestSiteChoices(t *testing.T) {
	choices := []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSite, rpc.ChoiceSiteAlways, rpc.ChoiceDeny}
	want := "[o]nce  [w] go.dev this session  [a] go.dev always  [d]eny"
	if got := choiceMenu(choices, "go.dev"); got != want {
		t.Errorf("menu = %q, want %q", got, want)
	}
	for input, want := range map[string]rpc.Choice{"w": rpc.ChoiceSite, "A": rpc.ChoiceSiteAlways, "site_always": rpc.ChoiceSiteAlways} {
		if c, ok := pickChoice(input, choices); !ok || c != want {
			t.Errorf("pickChoice(%q) = %q %v, want %q", input, c, ok, want)
		}
	}
}

// TestAskConsent checks the question meru asks before a turn goes to an
// answer endpoint off this computer: its words, its three answers, and no
// on end of input.
func TestAskConsent(t *testing.T) {
	turn := func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
		c, err := approve(ctx, rpc.Approval{ID: "consent-1", Name: "@lab", Kind: rpc.KindConsent, Choices: allChoices,
			Consent: &rpc.ConsentInfo{Endpoint: "lab", Reach: "near-edge", Model: "big-model",
				Carries: []string{"your question", "2 earlier turns of this chat", "the results of any tools this turn runs"}}})
		if err != nil {
			return err
		}
		return emit(rpc.Event{Type: rpc.EventToken, Text: "Answer (" + string(c) + ")."})
	}
	for _, tt := range []struct{ input, want string }{
		{"t\n", "once"}, {"this chat\n", "session"}, {"N\n", "deny"}, {"x\nc\n", "session"}, {"", "deny"},
	} {
		sock := startServer(t, turn)
		var out, errOut bytes.Buffer
		p := newPrompter(strings.NewReader(tt.input), &errOut, true)
		if err := ask(context.Background(), sock, "q", "", false, &out, &errOut, p.approve); err != nil {
			t.Fatalf("%q: ask: %v", tt.input, err)
		}
		if want := "Answer (" + tt.want + ").\n"; out.String() != want {
			t.Errorf("%q: stdout = %q, want %q", tt.input, out.String(), want)
		}
		for _, w := range []string{"Meru would send this question to @lab (near-edge, model big-model).",
			"It carries your question, 2 earlier turns of this chat and the results of any tools this turn runs.",
			"Send it? [t]his time  this [c]hat  [n]o, answer here: "} {
			if !strings.Contains(errOut.String(), w) {
				t.Errorf("%q: stderr lacks %q:\n%s", tt.input, w, errOut.String())
			}
		}
	}
}
