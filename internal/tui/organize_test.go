// This file tests the commands that organize chats: what each sends to
// merud and what the screen does with the reply, with a fake merud as
// parity_test.go uses.

package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

const testSession = "2026-09-27T101500-ab12"

func TestDeleteCommand(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m, _ = update(t, m, typeText("/delete"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "nothing to delete") || len(merud.reqs) != 0 {
		t.Errorf("a new chat: notice %q, requests %+v", m.notice, merud.reqs)
	}
	m.session = testSession
	m.turns = []exchange{{question: "When do I plant garlic?", answer: "In autumn.", state: stateDone}}

	// The first /delete arms it; a line in between disarms it.
	m, _ = update(t, m, typeText("/delete"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "/delete again") || len(merud.reqs) != 0 {
		t.Fatalf("first /delete: notice %q, requests %+v", m.notice, merud.reqs)
	}
	m, _ = update(t, m, typeText("/folder"), press(tea.KeyEnter))
	m, cmd := update(t, m, typeText("/delete"), press(tea.KeyEnter))
	if cmd != nil || !strings.Contains(m.notice, "/delete again") {
		t.Fatalf("a /folder between didn't disarm: notice %q", m.notice)
	}

	m, cmd = update(t, m, typeText("/delete"), press(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("the second /delete sent nothing")
	}
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpSessionDelete || r.Session != testSession {
		t.Errorf("request = %+v, want session_delete for the chat", r)
	}
	if m.session != "" || len(m.turns) != 0 || !strings.Contains(m.notice, "deleted the chat") {
		t.Errorf("after delete: session %q, %d turns, notice %q", m.session, len(m.turns), m.notice)
	}
}

func TestMoveAndTagCommands(t *testing.T) {
	merud := &fakeMerud{}
	m := testModel(merud.ask, newFakeSender())
	m, _ = update(t, m, typeText("/move Garden"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "ask something first") || len(merud.reqs) != 0 {
		t.Errorf("a new chat: notice %q", m.notice)
	}
	m.session = testSession
	tests := []struct {
		line   string
		want   rpc.Request
		reply  rpc.SessionInfo
		notice string
	}{
		{"/move Garden", rpc.Request{Op: rpc.OpSessionMove, Session: testSession, Text: "Garden"},
			rpc.SessionInfo{ID: testSession, Folder: "Garden"}, "this chat is in Garden · no tags"},
		{"/move", rpc.Request{Op: rpc.OpSessionMove, Session: testSession},
			rpc.SessionInfo{ID: testSession}, "this chat is in no folder · no tags"},
		{"/tag bulbs autumn", rpc.Request{Op: rpc.OpSessionTag, Session: testSession, Tags: &rpc.TagChange{Add: []string{"bulbs", "autumn"}}},
			rpc.SessionInfo{ID: testSession, Tags: []string{"bulbs", "autumn"}}, "tags: bulbs, autumn"},
		{"/untag autumn", rpc.Request{Op: rpc.OpSessionTag, Session: testSession, Tags: &rpc.TagChange{Remove: []string{"autumn"}}},
			rpc.SessionInfo{ID: testSession, Tags: []string{"bulbs"}}, "tags: bulbs"},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			merud.events = []rpc.Event{{Type: rpc.EventSessions, Sessions: []rpc.SessionInfo{tt.reply}}, {Type: rpc.EventDone}}
			var cmd tea.Cmd
			m, cmd = update(t, m, typeText(tt.line), press(tea.KeyEnter))
			if cmd == nil {
				t.Fatal("sent nothing")
			}
			m, _ = update(t, m, cmd())
			if r := lastReq(t, merud); !reflect.DeepEqual(r, tt.want) {
				t.Errorf("request = %+v, want %+v", r, tt.want)
			}
			if !strings.Contains(m.notice, tt.notice) {
				t.Errorf("notice = %q, want %q", m.notice, tt.notice)
			}
		})
	}
	m.input.Reset()
	m, _ = update(t, m, typeText("/tag"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "name a tag") {
		t.Errorf("/tag alone: notice %q", m.notice)
	}
}

func TestFolderCommand(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventChatFolders, ChatFolders: []string{"Cooking", "Garden"}}, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	tests := []struct {
		line string
		want rpc.Request
	}{
		{"/folder", rpc.Request{Op: rpc.OpChatFolders}},
		{"/folder new Garden", rpc.Request{Op: rpc.OpChatFolderAdd, ID: "Garden"}},
		{"/folder rename Recipes -> Cooking", rpc.Request{Op: rpc.OpChatFolderRename, ID: "Recipes", Text: "Cooking"}},
		{"/folder delete Old plans", rpc.Request{Op: rpc.OpChatFolderRemove, ID: "Old plans"}},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			var cmd tea.Cmd
			m, cmd = update(t, m, typeText(tt.line), press(tea.KeyEnter))
			m, _ = update(t, m, cmd())
			if r := lastReq(t, merud); r != tt.want {
				t.Errorf("request = %+v, want %+v", r, tt.want)
			}
			if m.notice != "chat folders: Cooking, Garden" {
				t.Errorf("notice = %q", m.notice)
			}
		})
	}
	n := len(merud.reqs)
	for _, bad := range []string{"/folder rename Recipes", "/folder new", "/folder paint Garden"} {
		m.input.Reset()
		m, _ = update(t, m, typeText(bad), press(tea.KeyEnter))
		if !strings.Contains(m.notice, "/folder new <name>") || len(merud.reqs) != n {
			t.Errorf("%s: notice %q", bad, m.notice)
		}
	}
}

func TestIncognitoCommand(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventSession, Session: "incognito-0123abcd", Incognito: true}, {Type: rpc.EventDone}}}
	snd := newFakeSender()
	m := testModel(merud.ask, snd)
	m, _ = update(t, m, typeText("/incognito"), press(tea.KeyEnter))
	if !m.incognito || !strings.Contains(m.header(), "incognito") {
		t.Fatalf("incognito %v, header %q", m.incognito, m.header())
	}
	m = ask(t, m, merud, snd, "Tell me about tulips.")
	if r := lastReq(t, merud); !r.Incognito || r.Session != "" {
		t.Errorf("first ask = %+v, want a new incognito chat", r)
	}
	if m.session != "incognito-0123abcd" {
		t.Fatalf("session = %q", m.session)
	}
	m = ask(t, m, merud, snd, "And daffodils?")
	if r := lastReq(t, merud); r.Incognito || r.Session != "incognito-0123abcd" {
		t.Errorf("second ask = %+v, want it to continue by ID", r)
	}
	// An incognito chat takes no folder or tags.
	m, _ = update(t, m, typeText("/tag bulbs"), press(tea.KeyEnter))
	if !strings.Contains(m.notice, "incognito") {
		t.Errorf("/tag in an incognito chat: notice %q", m.notice)
	}
	// /new leaves it, and tells merud to forget it.
	m.input.Reset()
	merud.events = []rpc.Event{{Type: rpc.EventDone}}
	m, cmd := update(t, m, typeText("/new"), press(tea.KeyEnter))
	if cmd == nil || m.incognito {
		t.Fatalf("/new: cmd %v, incognito %v", cmd, m.incognito)
	}
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpSessionDelete || r.Session != "incognito-0123abcd" {
		t.Errorf("leaving sent %+v, want session_delete", r)
	}
}

// TestChatsBoxOrganize checks the /chats box: rows show folder and tags,
// words find a chat by a tag, and d twice deletes the marked chat.
func TestChatsBoxOrganize(t *testing.T) {
	rows := []rpc.SessionInfo{
		{ID: testSession, Title: "When do I plant garlic?", Updated: "2026-09-27T10:15:00Z", Turns: 1, Folder: "Garden", Tags: []string{"bulbs"}},
		{ID: "2026-09-26T090000-cd34", Title: "How long do I boil an egg?", Updated: "2026-09-26T09:00:00Z", Turns: 1},
	}
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventSessions, Sessions: rows}, {Type: rpc.EventDone}}}
	m := testModel(merud.ask, newFakeSender())
	m.session = testSession
	m.turns = []exchange{{question: "When do I plant garlic?", state: stateDone}}
	m, cmd := update(t, m, typeText("/chats bulbs"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if len(m.chatsBox.rows) != 1 || !strings.Contains(m.View(), "[Garden] When do I plant garlic? #bulbs") {
		t.Fatalf("rows %+v, view:\n%s", m.chatsBox.rows, m.View())
	}
	merud.events = []rpc.Event{{Type: rpc.EventDone}}
	m, _ = update(t, m, typeText("d"))
	if len(merud.reqs) != 1 || m.chatsBox.armed != testSession {
		t.Fatalf("first d: requests %+v", merud.reqs)
	}
	m, cmd = update(t, m, typeText("d"))
	m, _ = update(t, m, cmd())
	if r := lastReq(t, merud); r.Op != rpc.OpSessionDelete || r.Session != testSession {
		t.Errorf("request = %+v", r)
	}
	if len(m.chatsBox.rows) != 0 || m.session != "" {
		t.Errorf("after delete: rows %+v, session %q", m.chatsBox.rows, m.session)
	}
}
