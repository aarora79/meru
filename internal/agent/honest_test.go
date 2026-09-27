// This file tests honest.go: the claim check on its own, the line on what
// Meru can do, and whole turns that do or don't warn the user.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestClaimsAction(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		// Claims.
		{"Done. It's now at ~/Projects/garden", true},
		{"**Done!** The garden folder is in ~/Projects.", true},
		{"Done — it's now at ~/Projects/garden/.", true},
		{"I've moved the folder.", true},
		{"I have just moved the garden folder to ~/Projects.", true},
		{"I sent the email to Sam.", true},
		{"The file has been saved to ~/Projects.", true},
		{"Your events have been scheduled for Monday.", true},
		{"The folder is now in ~/Projects.", true},
		{"I’ve deleted the old notes.", true}, // a curly apostrophe
		{"I've created the file ~/Projects/garden/plan.md.", true},
		{"I updated the note on tomatoes.", true},
		{"Here is the plan.\n\nDone. It's now at ~/Projects/garden.", true},
		// Not claims.
		{"I can move it if you like.", false},
		{"Want me to save it?", false},
		{"If you like, I'll send the email.", false},
		{"I couldn't move it because Meru has no tool that moves files.", false},
		{"I haven't moved anything.", false},
		{"I didn't save the file.", false},
		{"Done is better than perfect.", false},
		{"Go 1.24 is now in beta.", false},
		{"I've created a short plan for the garden:", false},
		{"I've updated the function below to use slices.Reverse.", false},
		{"Should I delete the old notes?", false},
		{"Let me know and I'll move it.", false},
		{"Run this:\n```sh\nmv ~/meru-output/garden ~/Projects # I moved it here\n```", false},
		{"The script reads main.go. It prints hello.", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := claimsAction(tt.text); got != tt.want {
				t.Errorf("claimsAction(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestCanDoNote(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		outputDir string
		want      string
	}{
		{"no tools", nil, "~/meru-output",
			"You can't write files. Meru's own tools can't move, rename or delete files, or run programs."},
		{"write_file", []string{builtin.ReadFile, builtin.WriteFile}, "~/meru-output",
			"You can write files only inside ~/meru-output, with write_file. Meru's own tools can't move, rename or delete files, or run programs."},
		{"write_file with no folder", []string{builtin.WriteFile}, "",
			"You can't write files. Meru's own tools can't move, rename or delete files, or run programs."},
		{"commands, sorted", []string{"cmd.git-log", builtin.WriteFile, "cmd.du", "google.send_gmail_message"}, "~/meru-output",
			"You can write files only inside ~/meru-output, with write_file. Meru's own tools can't move, rename or delete files, or run programs other than cmd.du, cmd.git-log."},
		{"about_meru", []string{builtin.AboutMeru, builtin.DateTime}, "~/meru-output",
			"You can't write files. Meru's own tools can't move, rename or delete files, or run programs. " + selfNote},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var specs []engine.ToolSpec
			for _, n := range tt.tools {
				specs = append(specs, spec(n))
			}
			if got := canDoNote(specs, tt.outputDir); got != tt.want {
				t.Errorf("canDoNote = %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestPromptSaysWhatMeruCanDo checks that every turn's system prompt holds
// the honesty rule and the line on what Meru can do, with the output
// folder and the command names, even on a search turn, which offers
// neither write_file nor a command that asks first.
func TestPromptSaysWhatMeruCanDo(t *testing.T) {
	tools := &fakeTools{
		specs: []engine.ToolSpec{spec(builtin.ReadFile), spec(builtin.WriteFile), spec("cmd.git-log"), spec("cmd.du")},
		asks:  map[string]bool{builtin.WriteFile: true, "cmd.du": true},
	}
	eng := &fakeEngine{rounds: []fakeRound{{pieces: []string{"Meru has no tool that moves folders."}}}}
	a := toolsAgent(t, "search", eng, tools)
	if _, err := run(context.Background(), a, rpc.Request{Text: "move the garden folder to ~/Projects"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	system := eng.calls[0].msgs[0].Content
	for _, want := range []string{
		honestyRule,
		"You can write files only inside ~/meru-output, with write_file.",
		"run programs other than cmd.du, cmd.git-log.",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, system)
		}
	}
}

// TestUnbackedClaim runs whole turns and checks when the "notice" event
// comes, and that the assistant line keeps it.
func TestUnbackedClaim(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]fakeResult
		rounds  []fakeRound
		want    bool
	}{
		{name: "claim with no tool call",
			rounds: []fakeRound{{pieces: []string{"Done. It's now at ", "~/Projects/garden/"}}},
			want:   true},
		{name: "claim after a successful write_file",
			rounds: []fakeRound{
				{calls: []engine.ToolCall{call(builtin.WriteFile, `{"path":"garden/plan.md","content":"Sow tomatoes"}`)}},
				{pieces: []string{"Saved it. I've saved the plan to ~/meru-output/garden/plan.md."}},
			}},
		{name: "claim after a declined call",
			results: map[string]fakeResult{builtin.WriteFile: {outcome: dispatch.OutcomeDeclined}},
			rounds: []fakeRound{
				{calls: []engine.ToolCall{call(builtin.WriteFile, `{"path":"garden/plan.md","content":"Sow tomatoes"}`)}},
				{pieces: []string{"I've saved the plan to ~/meru-output/garden/plan.md."}},
			},
			want: true},
		{name: "honest answer with no tool call",
			rounds: []fakeRound{{pieces: []string{"I can't move folders: Meru has no tool for that. I can write a new copy in ~/meru-output."}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			tools := &fakeTools{specs: []engine.ToolSpec{spec(builtin.WriteFile)}, results: tt.results}
			router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
			a := New(cfg, &fakeEngine{rounds: tt.rounds}, router, nil, tools, nil, nil, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: "move the garden folder to ~/Projects"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
			if got := i >= 0; got != tt.want {
				t.Fatalf("notice sent = %v, want %v; events %v", got, tt.want, types(evs))
			}
			lines := readLines(t, cfg, evs[0].Session)
			answer := lines[len(lines)-1]
			if !tt.want {
				if answer.Notice != "" {
					t.Errorf("assistant line notice = %q, want none", answer.Notice)
				}
				return
			}
			if evs[i].Text != unbackedNotice {
				t.Errorf("notice = %q, want %q", evs[i].Text, unbackedNotice)
			}
			// The notice comes after the answer and right before "done".
			if i != len(evs)-2 || evs[i-1].Type != rpc.EventToken || evs[len(evs)-1].Type != rpc.EventDone {
				t.Errorf("events %v: want the notice between the last token and done", types(evs))
			}
			if answer.Type != transcript.TypeAssistant || answer.Notice != unbackedNotice {
				t.Errorf("last line = %+v, want an assistant line with the notice", answer)
			}
		})
	}
}

// TestNoticeInHistory checks that the next turn's model reads the notice
// after the answer that earned it.
func TestNoticeInHistory(t *testing.T) {
	eng := &fakeEngine{rounds: []fakeRound{
		{pieces: []string{"Done. It's now at ~/Projects/garden/."}},
		{pieces: []string{"It isn't there: Meru can't move folders."}},
	}}
	a := toolsAgent(t, "search", eng, nil)
	evs, err := run(context.Background(), a, rpc.Request{Text: "move the garden folder to ~/Projects"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if _, err := run(context.Background(), a, rpc.Request{Text: "it isn't there", Session: evs[0].Session}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	msgs := eng.lastCall().msgs
	want := "Done. It's now at ~/Projects/garden/.\n\n[" + unbackedNotice + "]"
	if got := msgs[len(msgs)-2]; got.Role != engine.RoleAssistant || got.Content != want {
		t.Errorf("earlier answer = %+v, want %q", got, want)
	}
}
