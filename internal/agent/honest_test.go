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

func TestToolClaims(t *testing.T) {
	tools := []string{builtin.DateTime, builtin.Grep, builtin.SearchFiles, "notes.search", "cmd.du"}
	tests := []struct {
		text  string
		want  bool
		names []string // the tools the first claim names
	}{
		// Claims.
		{"I called the `datetime` tool — it queries your computer's clock.", true, []string{"datetime"}},
		{"I called the datetime tool.", true, []string{"datetime"}},
		{"I ran date.", true, []string{"date"}},
		{"I just ran `date` on your Mac.", true, []string{"date"}},
		{"I found three notes using the grep tool.", true, []string{"grep"}},
		{"I’ve used the search_files tool to look.", true, []string{"search_files"}},
		{"I used a tool to check the clock.", true, nil},
		{"I checked with the notes.search tool.", true, []string{"search"}},
		{"The datetime tool returned 15:25.", true, []string{"datetime"}},
		{"According to the `datetime` tool, it's 15:25.", true, []string{"datetime"}},
		{"I called `datetime`.", true, []string{"datetime"}},
		// Not claims.
		{"I can call the datetime tool for you.", false, nil},
		{"Want me to run grep?", false, nil},
		{"I didn't call any tool; the time is in my prompt.", false, nil},
		{"You can search your notes using the grep tool.", false, nil},
		{"I used your notes to answer.", false, nil},
		{"I used `strings.Builder` in the example.", false, nil},
		{"I ran the numbers again.", false, nil},
		{"If you like, I'll run the datetime tool.", false, nil},
		{"It's 20:02 EDT.", false, nil},
		{"```\n# I ran date here\n$ date\n```", false, nil},
		// A miss the rules accept: no rule reads "by calling".
		{"I got it by calling the datetime tool.", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := toolClaims(tt.text, tools)
			if (len(got) > 0) != tt.want {
				t.Fatalf("toolClaims = %v, want a claim: %v", got, tt.want)
			}
			if tt.want && !slices.Equal(got[0].names, tt.names) {
				t.Errorf("names = %q, want %q", got[0].names, tt.names)
			}
		})
	}
}

func TestUnbackedCall(t *testing.T) {
	tests := []struct {
		name     string
		claims   []toolClaim
		called   []string
		wantTool string
		want     bool
	}{
		{"named tool never ran", []toolClaim{{names: []string{"datetime"}}}, nil, "datetime", true},
		{"named tool ran", []toolClaim{{names: []string{"datetime"}}}, []string{"datetime"}, "", false},
		{"another tool ran", []toolClaim{{names: []string{"datetime"}}}, []string{"grep"}, "datetime", true},
		{"model name against the recorded name", []toolClaim{{names: []string{"search"}}}, []string{"notes.search"}, "", false},
		{"no name, nothing ran", []toolClaim{{}}, nil, "", true},
		{"no name, a tool ran", []toolClaim{{}}, []string{"grep"}, "", false},
		{"a program Meru never runs", []toolClaim{{names: []string{"date"}}}, []string{"datetime"}, "date", true},
		{"the second claim fails", []toolClaim{{names: []string{"grep"}}, {names: []string{"datetime"}}}, []string{"grep"}, "datetime", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, got := unbackedCall(tt.claims, tt.called)
			if got != tt.want || tool != tt.wantTool {
				t.Errorf("unbackedCall = %q, %v; want %q, %v", tool, got, tt.wantTool, tt.want)
			}
		})
	}
}

// TestMadeUpTime replays a failure seen on a real turn, with invented
// words. Asked the time, the model called no tool and answered with a
// time; asked how it knew, it said it had called the datetime tool. The
// first answer's prompt must now end with the time, and the second answer
// must get the notice.
func TestMadeUpTime(t *testing.T) {
	cfg := testConfig(t)
	tools := &fakeTools{specs: []engine.ToolSpec{spec(builtin.DateTime), spec(builtin.AboutMeru)}}
	eng := &fakeEngine{rounds: []fakeRound{
		{pieces: []string{"It's 3:25 PM on Monday."}},
		{pieces: []string{"I called the `datetime` tool — it queries your computer's clock."}},
	}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, tools, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "whats the date and time right now"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if system := eng.calls[0].msgs[0].Content; !clockLine.MatchString(system) {
		t.Errorf("system prompt doesn't end with the time:\n%s", system)
	}
	if slices.ContainsFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice }) {
		t.Errorf("first answer got a notice, but it claims no call")
	}

	evs, err = run(context.Background(), a, rpc.Request{Text: "did you run date or how did you get this time", Session: evs[0].Session})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
	if i < 0 {
		t.Fatalf("no notice; events %v", types(evs))
	}
	if want := callNotice("datetime"); evs[i].Text != want {
		t.Errorf("notice = %q, want %q", evs[i].Text, want)
	}
	lines := readLines(t, cfg, evs[0].Session)
	if answer := lines[len(lines)-1]; answer.Notice != callNotice("datetime") {
		t.Errorf("assistant line notice = %q, want the call notice", answer.Notice)
	}
}

// TestToolClaimBackedByHistory checks that a follow-up answer that names
// a tool an earlier turn called gets no notice, and that one that names a
// tool no turn called does.
func TestToolClaimBackedByHistory(t *testing.T) {
	tests := []struct {
		name   string
		second string
		want   bool
	}{
		{"names the tool the first turn called", "I called the datetime tool in my last answer.", false},
		{"names a tool no turn called", "I used the grep tool to find it.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &fakeTools{specs: []engine.ToolSpec{spec(builtin.DateTime), spec(builtin.Grep)}}
			eng := &fakeEngine{rounds: []fakeRound{
				{calls: []engine.ToolCall{call(builtin.DateTime, `{}`)}},
				{pieces: []string{"It's 09:15."}},
				{pieces: []string{tt.second}},
			}}
			a := toolsAgent(t, "tools", eng, tools)
			evs, err := run(context.Background(), a, rpc.Request{Text: "what time is it"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			evs, err = run(context.Background(), a, rpc.Request{Text: "how do you know", Session: evs[0].Session})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			got := slices.ContainsFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
			if got != tt.want {
				t.Errorf("notice sent = %v, want %v; events %v", got, tt.want, types(evs))
			}
		})
	}
}
