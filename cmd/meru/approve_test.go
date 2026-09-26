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
			if err := ask(context.Background(), sock, "email sam", &out, &errOut, p.approve); err != nil {
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
