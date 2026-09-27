// This file tests the commands and keys that match the desktop app: what
// each sends to merud, and what the screen does with the reply. Each test
// drives Update with a fake merud, as model_test.go does.

package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// TestCommandHelp checks that every command in commandList has its line
// in the /help box, in the same order.
func TestCommandHelp(t *testing.T) {
	names := strings.Split(commandList, ", ")
	if len(names) != len(commandHelp) {
		t.Fatalf("commandList has %d commands, commandHelp %d", len(names), len(commandHelp))
	}
	for i, n := range names {
		if use, _, _ := strings.Cut(commandHelp[i].use, " "); use != n {
			t.Errorf("commandHelp[%d] = %q, want %s", i, commandHelp[i].use, n)
		}
	}
}

// lastReq returns the newest request merud got, or fails the test.
func lastReq(t *testing.T, merud *fakeMerud) rpc.Request {
	t.Helper()
	if len(merud.reqs) == 0 {
		t.Fatal("merud got no request")
	}
	return merud.reqs[len(merud.reqs)-1]
}

func TestScope(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, _ = update(t, m, typeText("/scope mail"), press(tea.KeyEnter))
	if m.scope != rpc.ScopeMail || !strings.Contains(m.header(), "scope: mail and calendar") {
		t.Fatalf("scope = %q, header %q", m.scope, m.header())
	}
	m, cmd := update(t, m, typeText("what's on Friday?"), press(tea.KeyEnter))
	finishTurn(cmd)
	if r := lastReq(t, merud); r.Op != rpc.OpAsk || r.Scope != rpc.ScopeMail {
		t.Errorf("request = %+v, want an ask with scope mail", r)
	}
	m, _ = update(t, m, typeText("/scope everywhere"), press(tea.KeyEnter))
	if m.scope != rpc.ScopeMail || !strings.Contains(m.notice, "auto, files, mail, web, talk") {
		t.Errorf("an unknown scope: scope %q, notice %q", m.scope, m.notice)
	}
	// A mistake stays in the input, to fix; this test types a new line.
	m.input.Reset()
	m, _ = update(t, m, typeText("/scope auto"), press(tea.KeyEnter))
	if m.scope != "" || strings.Contains(m.header(), "scope:") {
		t.Errorf("after /scope auto: scope %q, header %q", m.scope, m.header())
	}
}

func TestRetry(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, _ = update(t, m, typeText("/retry"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "nothing to ask again") {
		t.Errorf("notice = %q", m.notice)
	}
	m.turns = []exchange{{question: "Describe this bed", state: stateDone, scope: rpc.ScopeWeb, images: []string{"/up/bed.png"}}}
	m, cmd := update(t, m, typeText("/retry"), press(tea.KeyEnter))
	finishTurn(cmd)
	r := lastReq(t, merud)
	if r.Text != "Describe this bed" || r.Scope != rpc.ScopeWeb || r.Images == nil || !reflect.DeepEqual(r.Images.Paths, []string{"/up/bed.png"}) {
		t.Errorf("request = %+v, want the question again with its scope and image", r)
	}
	if len(m.turns) != 2 || m.input.Value() != "" {
		t.Errorf("turns = %d, input %q", len(m.turns), m.input.Value())
	}
}

func TestAttach(t *testing.T) {
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "garden-plan.pdf")
	if err := os.WriteFile(copyPath, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventSaved, Text: copyPath, Kind: rpc.AttachFile}, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m.home = dir
	m.scope = rpc.ScopeWeb
	m, cmd := update(t, m, typeText("/attach ~/plans/garden-plan.pdf"), press(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("/attach sent nothing")
	}
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpAttachFile || r.Path != filepath.Join(dir, "plans", "garden-plan.pdf") {
		t.Errorf("request = %+v, want attach_file with the full path", r)
	}
	if len(m.attached) != 1 || m.attached[0].size != "4 B" || m.scope != rpc.ScopeFiles {
		t.Fatalf("attached = %+v, scope %q; want one file and scope files", m.attached, m.scope)
	}
	if !strings.Contains(m.View(), "attached: garden-plan.pdf") {
		t.Errorf("view lacks the attachments line:\n%s", m.View())
	}
	// An image rides along by path; the file gets a "Read this file" line.
	m.attached = append(m.attached, attachment{name: "bed.png", full: "/up/bed.png", kind: rpc.AttachImage})
	merud.events = []rpc.Event{{Type: rpc.EventDone}}
	m, cmd = update(t, m, typeText("Plan it"), press(tea.KeyEnter))
	finishTurn(cmd)
	r := lastReq(t, merud)
	if want := "Plan it\n\nRead this file: ~/garden-plan.pdf"; r.Text != want || r.Images == nil || r.Images.Paths[0] != "/up/bed.png" {
		t.Errorf("request text %q, images %+v; want %q and the image", r.Text, r.Images, want)
	}
	if len(m.attached) != 0 {
		t.Errorf("the attachments stayed on: %+v", m.attached)
	}
	// /attach alone takes them off; a refusal says why.
	m.attached = []attachment{{name: "x"}}
	m, _ = update(t, m, typeText("/attach"), press(tea.KeyEnter))
	if len(m.attached) != 0 || !strings.Contains(m.notice, "took 1 attachment off") {
		t.Errorf("attached %+v, notice %q", m.attached, m.notice)
	}
	m, _ = update(t, m, replyMsg{tag: tagAttach, err: errors.New("id_ed25519 looks like a secret")})
	if !strings.Contains(m.notice, "not attached: id_ed25519") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestSave checks /save: it asks merud to save the newest answer, the
// approval opens in the conversation's place, and the notice names the
// file.
func TestSave(t *testing.T) {
	approval := rpc.Approval{ID: "1", Name: "write_file", Kind: "builtin", Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}}
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventApproval, Approval: &approval}, {Type: rpc.EventSaved, Text: "/Users/dana/meru-output/notes/sowing.md"}, {Type: rpc.EventDone}}}
	snd := newFakeSender()
	m := testModel(merud.ask, snd)
	m, _ = update(t, m, typeText("/save"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "ask something first") {
		t.Errorf("notice = %q", m.notice)
	}
	m.session = "2026-09-27T101500-ab12"
	m.turns = []exchange{{question: "When?", answer: "On 12 April.", state: stateDone}}
	m, cmd := update(t, m, typeText("/save"), press(tea.KeyEnter))
	if cmd == nil || m.saving == 0 {
		t.Fatal("/save sent nothing")
	}
	// The save runs in its own goroutine, as Bubble Tea runs a command;
	// the approval comes back through the sender.
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	m = untilApproval(t, m, snd)
	if !m.approval.save || !strings.Contains(m.View(), "Run write_file?") || strings.Contains(m.View(), "edit first") {
		t.Fatalf("view lacks the save's approval, or offers edit first:\n%s", m.View())
	}
	m, _ = update(t, m, typeText("o"))
	m, _ = update(t, m, <-result)
	r := merud.reqs[0]
	if r.Op != rpc.OpSaveFile || r.Kind != rpc.SaveNote || r.Text != "On 12 April." || r.Source != rpc.SourceTUI {
		t.Errorf("request = %+v, want a note of the answer", r)
	}
	if m.saving != 0 || !strings.Contains(m.notice, "saved ") || !strings.Contains(m.notice, "sowing.md") {
		t.Errorf("saving %d, notice %q", m.saving, m.notice)
	}
	// /save chat saves the session; merud's refusal lands on the notice.
	merud.events = []rpc.Event{{Type: rpc.EventError, Error: "not saved: you didn't allow write_file"}}
	m, cmd = update(t, m, typeText("/save chat"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Kind != rpc.SaveChat || !strings.Contains(m.notice, "didn't allow") {
		t.Errorf("request %+v, notice %q", r, m.notice)
	}
}

// TestEditFirst checks e in a turn's approval box: it denies the call and
// puts it in the input as a draft.
func TestEditFirst(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, typeText("mail Sam"), press(tea.KeyEnter))
	reply := make(chan rpc.Choice, 1)
	m, _ = update(t, m, approvalRequestMsg{turn: m.turn, reply: reply, approval: rpc.Approval{
		Name: "google.send_gmail_message", Kind: "mcp", Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny},
		Args: json.RawMessage(`{"to":"sam@example.com"}`)}})
	m, _ = update(t, m, typeText("e"))
	if got := <-reply; got != rpc.ChoiceDeny {
		t.Errorf("choice = %q, want deny", got)
	}
	want := "Run google.send_gmail_message with these arguments instead:\n\n{\n  \"to\": \"sam@example.com\"\n}"
	if m.approval != nil || m.input.Value() != want {
		t.Errorf("approval %v, input %q; want the draft %q", m.approval, m.input.Value(), want)
	}
}

// TestUsedForget checks the /used box: d marks a memory, a second d asks
// merud to forget it, and the reply takes it off the turn.
func TestUsedForget(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventDone}}}
	m := usedTurn(testModel(merud.ask, newFakeSender()))
	m, _ = update(t, m, typeText("/used"), press(tea.KeyEnter), press(tea.KeyDown))
	m, cmd := update(t, m, typeText("d"))
	if cmd != nil || m.usedBox.confirm != "places/allotment.md" {
		t.Fatalf("first d: cmd %v, confirm %q", cmd != nil, m.usedBox.confirm)
	}
	m, cmd = update(t, m, typeText("d"))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpMemoryForget || r.ID != "places/allotment.md" {
		t.Errorf("request = %+v", r)
	}
	if mems := m.turns[0].memories; len(mems) != 1 || !strings.Contains(m.notice, "forgot: The allotment") {
		t.Errorf("memories %+v, notice %q", mems, m.notice)
	}
	if got := privacyLine(&m.turns[0]); got != "The model ran on this computer. Only google and example.com were contacted." {
		t.Errorf("privacy line = %q", got)
	}
}

func TestContacted(t *testing.T) {
	tools := []toolCall{
		{name: "google.search", kind: "mcp", outcome: "ok"},
		{name: "a2a.travel.book", kind: "a2a", outcome: "ok"},
		{name: "web_search", kind: "builtin", outcome: "ok"},
		{name: "web_fetch", kind: "builtin", host: "example.com", outcome: "ok"},
		{name: "obsidian.write", kind: "mcp", outcome: "declined"},
		{name: "read_file", kind: "builtin", outcome: "ok"},
		{name: "google.send", kind: "mcp", outcome: "ok"},
	}
	want := []string{"google", "travel", "web search", "example.com"}
	if got := contacted(tools); !reflect.DeepEqual(got, want) {
		t.Errorf("contacted = %q, want %q", got, want)
	}
	if got := urlHost(json.RawMessage(`{"url":"https://example.com/a"}`)); got != "example.com" {
		t.Errorf("urlHost = %q", got)
	}
}

// TestChatsReopen checks /chats: the list, a filter, and Enter, which
// puts the past turns on screen and makes their session the next one.
func TestChatsReopen(t *testing.T) {
	turns := []rpc.TurnInfo{
		{Question: "Plan the beds", Answer: "Three beds.\n\n```\nbed 1\n```\n", Route: "search", Sources: []string{"~/Notes/garden.md"},
			Tools: []rpc.ToolStep{{Name: "web_fetch", Kind: "builtin", Args: json.RawMessage(`{"url":"https://example.com"}`), Outcome: "ok"}}, DurationMillis: 2400},
		{Question: "And the paths?", Outcome: "timeout"},
	}
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventSessions, Sessions: sessionsFixture}, {Type: rpc.EventTurns, Turns: turns}, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, cmd := update(t, m, typeText("/chats Garden"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if b := m.chatsBox; b == nil || len(b.rows) != 1 || b.rows[0].ID != "2026-09-26T184000-cd34" {
		t.Fatalf("box = %+v, want the one garden chat", m.chatsBox)
	}
	m, cmd = update(t, m, press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpSessionTurns || r.Session != "2026-09-26T184000-cd34" {
		t.Errorf("request = %+v", r)
	}
	if m.chatsBox != nil || m.session != "2026-09-26T184000-cd34" || len(m.turns) != 2 {
		t.Fatalf("box %v, session %q, %d turns", m.chatsBox != nil, m.session, len(m.turns))
	}
	if t0 := m.turns[0]; t0.state != stateDone || t0.firstBlock != 1 || len(t0.code) != 1 || t0.tools[0].host != "example.com" || !t0.past {
		t.Errorf("first turn = %+v", bare(t0))
	}
	if t1 := m.turns[1]; t1.state != stateFailed || t1.err != "no answer (timeout)" {
		t.Errorf("second turn: state %v, err %q", t1.state, t1.err)
	}
	// The next question continues the reopened session.
	merud.events = []rpc.Event{{Type: rpc.EventDone}}
	_, cmd = update(t, m, typeText("more"), press(tea.KeyEnter))
	finishTurn(cmd)
	if r := lastReq(t, merud); r.Session != "2026-09-26T184000-cd34" {
		t.Errorf("next ask went to session %q", r.Session)
	}
}

func TestFoldersKeys(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{foldersFixture, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, cmd := update(t, m, typeText("/folders"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	// The third row is the first suggestion: Enter adds it.
	m, cmd = update(t, m, press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpFolderAdd || r.Path != "~/Documents" {
		t.Errorf("request = %+v, want folder_add ~/Documents", r)
	}
	// d twice on the first row removes it.
	m, _ = update(t, m, press(tea.KeyHome))
	m, cmd = update(t, m, typeText("d"))
	if cmd != nil || m.foldersBox.confirm != "~/Notes" {
		t.Fatalf("first d: cmd %v, confirm %q", cmd != nil, m.foldersBox.confirm)
	}
	m, cmd = update(t, m, typeText("d"))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpFolderRemove || r.Path != "~/Notes" {
		t.Errorf("request = %+v, want folder_remove ~/Notes", r)
	}
	// /folders add takes a path of the user's own.
	m.home = "/Users/dana"
	_, cmd = update(t, m, press(tea.KeyEsc), typeText("/folders add ~/Garden"), press(tea.KeyEnter))
	cmd()
	if r := lastReq(t, merud); r.Op != rpc.OpFolderAdd || r.Path != filepath.Join("/Users/dana", "Garden") {
		t.Errorf("request = %+v", r)
	}
}

func TestSkillsToggle(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{skillsFixture, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, cmd := update(t, m, typeText("/skills"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	m, cmd = update(t, m, press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpSkillDisable || r.ID != "writing" {
		t.Errorf("request = %+v, want skill_disable writing", r)
	}
	if m.notice != "saved to config.toml" {
		t.Errorf("notice = %q", m.notice)
	}
	m, cmd = update(t, m, press(tea.KeyEnd), press(tea.KeySpace))
	cmd()
	if r := lastReq(t, merud); r.Op != rpc.OpSkillEnable || r.ID != "explainer" {
		t.Errorf("request = %+v, want skill_enable explainer", r)
	}
}

func TestMeAddAndForget(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventMemories, Memories: profileFixture}, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	for _, tt := range []struct{ line, kind string }{{"/me add I grow tomatoes", "me"}, {"/me prefer short answers", "preferences"}} {
		var cmd tea.Cmd
		m, cmd = update(t, m, typeText(tt.line), press(tea.KeyEnter))
		m, _ = update(t, m, cmd())
		if r := lastReq(t, merud); r.Op != rpc.OpMemoryAdd || r.Kind != tt.kind {
			t.Errorf("%s: request = %+v", tt.line, r)
		}
	}
	m, _ = update(t, m, typeText("/me add"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "/me takes") {
		t.Errorf("notice = %q", m.notice)
	}
	// In the box, the memories go kind by kind, and d d forgets the marked
	// one.
	m.input.Reset()
	m, cmd := update(t, m, typeText("/me"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if ids := []string{m.meBox.memories[1].ID, m.meBox.memories[2].ID}; ids[0] != "me/work.md" || ids[1] != "preferences/answers-short.md" {
		t.Errorf("order = %v", ids)
	}
	m, _ = update(t, m, press(tea.KeyDown), typeText("d"))
	m, cmd = update(t, m, typeText("d"))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpMemoryForget || r.ID != "me/work.md" {
		t.Errorf("request = %+v", r)
	}
	if len(m.meBox.memories) != 2 {
		t.Errorf("box still holds %d memories", len(m.meBox.memories))
	}
}

func TestCopyAnswer(t *testing.T) {
	var got string
	m := testModel(nil, newFakeSender())
	m.copy = func(s string) (string, error) { got = s; return "pbcopy", nil }
	m, _ = update(t, m, typeText("/copy answer"), press(tea.KeyEnter))
	if m.notice != "no answer to copy yet" {
		t.Errorf("notice = %q", m.notice)
	}
	m.turns = []exchange{{answer: "Line one\nLine two", state: stateDone}}
	m, cmd := update(t, m, typeText("/copy answer"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if got != "Line one\nLine two" || m.notice != "copied the answer (2 lines)" {
		t.Errorf("copied %q, notice %q", got, m.notice)
	}
}

func TestHelpAndAboutScroll(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 20}, typeText("/help"), press(tea.KeyEnter))
	_ = m.View()
	m, _ = update(t, m, press(tea.KeyEnd))
	if !strings.Contains(m.View(), "/exit") {
		t.Errorf("End didn't scroll to the last command:\n%s", m.View())
	}
	m, _ = update(t, m, typeText("q"), typeText("/about"), press(tea.KeyEnter))
	_ = m.View()
	m, _ = update(t, m, press(tea.KeyEnd))
	if !strings.Contains(m.View(), "Read the license") {
		t.Errorf("End didn't scroll to the last link:\n%s", m.View())
	}
}
