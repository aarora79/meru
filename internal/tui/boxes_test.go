// This file holds the golden tests for the boxes that match the desktop
// app's screens: /chats, /used, /folders, /skills, /log, /about, /help, a
// save's approval, and the attachments line with a scope in the header.
// Each draws at 80 and at 40 columns, and every line must fit.

package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// sessionsFixture is merud's answer to OpSessions: three past chats, the
// newest first.
var sessionsFixture = []rpc.SessionInfo{
	{ID: "2026-09-27T101500-ab12", Title: "Plan the tomato beds for spring", Updated: "2026-09-27T10:21:00+02:00", Turns: 3},
	{ID: "2026-09-26T184000-cd34", Title: "Summarize the garden notes", Updated: "2026-09-26T18:44:00+02:00", Turns: 1},
	{ID: "2026-09-20T090000-ef56", Title: "What's on Friday?", Updated: "2026-09-20T09:02:00+02:00", Turns: 2},
}

// foldersFixture is merud's answer to OpFolders.
var foldersFixture = rpc.Event{Type: rpc.EventFolders,
	Folders:   []rpc.FolderInfo{{Path: "~/Notes", Exists: true, Files: 812}, {Path: "~/Projects/garden", Exists: true, Files: 64}},
	Suggested: []rpc.FolderInfo{{Path: "~/Documents", Exists: true, Files: 1000, More: true}, {Path: "~/Desktop", Exists: true, Files: 12}},
}

// skillsFixture is merud's answer to OpSkills: two built-in skills, one
// of them off, and one of the user's own.
var skillsFixture = rpc.Event{Type: rpc.EventSkills, Skills: []rpc.SkillInfo{
	{Name: "writing", Description: "Write prose people will read", Builtin: true},
	{Name: "garden-planner", Description: "Plan beds and sowing dates"},
	{Name: "explainer", Builtin: true, Disabled: true},
}}

// logFixture is merud's answer to OpLog, newest first.
var logFixture = []rpc.LogEntry{
	{Time: "2026-09-27T10:17:21+02:00", Kind: "mcp", Server: "google", Tool: "search_gmail_messages",
		Args: json.RawMessage(`{"query":"garden"}`), Result: "3 messages", Outcome: "ok", Approval: "", DurationMillis: 420},
	{Time: "2026-09-27T10:16:02+02:00", Kind: "builtin", Tool: "web_search", Args: json.RawMessage(`{"query":"tomato spacing"}`),
		Outcome: "ok", Caller: "meru", DurationMillis: 1200},
	{Time: "2026-09-27T10:12:40+02:00", Kind: "mcp", Server: "google", Tool: "send_gmail_message",
		Args: json.RawMessage(`{"to":"sam@example.com"}`), Outcome: "declined", Approval: "deny", DurationMillis: 3},
}

// usedTurn is a finished turn that searched the notes, called two tools
// and brought in two memories.
func usedTurn(m Model) Model {
	m.turns = append(m.turns, exchange{
		question: "When do I sow the tomatoes?", state: stateDone, answer: "On 12 April [1].",
		sources: []rpc.Citation{{N: 1, Path: "~/Notes/garden.md", Heading: "Sowing"}, {N: 2, Path: "~/Notes/seeds.md"}},
		tools: []toolCall{
			{id: "1", name: "google.search_gmail_messages", kind: "mcp", outcome: "ok", millis: 420},
			{id: "2", name: "web_fetch", kind: "builtin", host: "example.com", outcome: "ok", millis: 900},
		},
		memories: []rpc.MemoryInfo{
			{ID: "people/sam.md", Kind: "people", Text: "Sam helps with the garden on weekends"},
			{ID: "places/allotment.md", Kind: "places", Text: "The allotment is plot 14"},
		},
	})
	m.refresh()
	return m
}

func TestBoxesGolden(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, m Model) Model
	}{
		{"chats", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/chats"), press(tea.KeyEnter),
				replyMsg{tag: tagSessions, ev: rpc.Event{Type: rpc.EventSessions, Sessions: sessionsFixture}}, press(tea.KeyDown))
			return m
		}},
		{"chats-filtered", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/chats garden"), press(tea.KeyEnter),
				replyMsg{tag: tagSessions, ev: rpc.Event{Type: rpc.EventSessions, Sessions: sessionsFixture}})
			return m
		}},
		{"used", func(t *testing.T, m Model) Model {
			m, _ = update(t, usedTurn(m), typeText("/used"), press(tea.KeyEnter))
			return m
		}},
		{"used-confirm", func(t *testing.T, m Model) Model {
			m, _ = update(t, usedTurn(m), typeText("/used"), press(tea.KeyEnter), press(tea.KeyDown), typeText("d"))
			return m
		}},
		{"folders", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/folders"), press(tea.KeyEnter), replyMsg{tag: tagFolders, ev: foldersFixture})
			return m
		}},
		{"skills-box", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/skills"), press(tea.KeyEnter), replyMsg{tag: tagSkills, ev: skillsFixture})
			return m
		}},
		{"log", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/log"), press(tea.KeyEnter),
				replyMsg{tag: tagLog, ev: rpc.Event{Type: rpc.EventLog, Log: logFixture}}, press(tea.KeyDown))
			return m
		}},
		{"about", func(t *testing.T, m Model) Model {
			m.home, m.info.Dir = "/Users/dana", "/Users/dana/.meru"
			m, _ = update(t, m, typeText("/about"), press(tea.KeyEnter))
			return m
		}},
		{"help", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/help"), press(tea.KeyEnter))
			return m
		}},
		{"mcp-key", func(t *testing.T, m Model) Model {
			m, _ = update(t, m, typeText("/mcp"), press(tea.KeyEnter), connsReply, press(tea.KeyEnd), press(tea.KeyUp), press(tea.KeyEnter))
			return m
		}},
		{"save-approval", func(t *testing.T, m Model) Model {
			m.session, m.saving = "2026-09-27T101500-ab12", 1
			m, _ = update(t, m, approvalRequestMsg{turn: 1, save: true, reply: make(chan rpc.Choice, 1), approval: rpc.Approval{
				Name: "write_file", Kind: "builtin", Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny},
				Args: json.RawMessage(`{"path":"notes/2026-09-27-sowing.md"}`)}})
			return m
		}},
		{"attached", func(t *testing.T, m Model) Model {
			m.scope = rpc.ScopeFiles
			m.attached = []attachment{
				{name: "garden-plan.pdf", path: "~/meru-output/uploads/garden-plan.pdf", kind: rpc.AttachFile, size: "2.4 MB"},
				{name: "garden-bed.png", path: "~/meru-output/uploads/garden-bed.png", kind: rpc.AttachImage, size: "812 KB"},
			}
			m.layout()
			return m
		}},
	}
	for _, tt := range tests {
		for _, width := range []int{80, 40} {
			name := tt.name
			if width == 40 {
				name += "-narrow"
			}
			t.Run(name, func(t *testing.T) {
				m := testModel(nil, newFakeSender())
				m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: 24}, pingMsg{})
				m = tt.setup(t, m)
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) != 24 {
					t.Errorf("view has %d lines, want 24", len(lines))
				}
				for i, l := range lines {
					if w := ansi.StringWidth(l); w > width {
						t.Errorf("line %d is %d wide, want at most %d: %q", i+1, w, width, l)
					}
				}
				golden(t, name, view)
			})
		}
	}
}
