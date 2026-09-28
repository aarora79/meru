//go:build e2e

// This file tests the chat list ops end to end: a chat tagged, moved and
// deleted over the socket, and an incognito chat that leaves no file.

package e2e

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// op sends one request that runs no tools and returns its events. It fails
// the test when the connection fails or merud answers with an error.
func op(t *testing.T, socket string, req rpc.Request) []rpc.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var events []rpc.Event
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		if ev.Type == rpc.EventError {
			t.Fatalf("%s: %s", req.Op, ev.Error)
		}
		events = append(events, ev)
	}
	return events
}

func TestChatListOps(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	s.fake.enqueue(t, fastModel, directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "Plant garlic in autumn."})
	id := eventsOf(ask(t, s.home.socket, "", "When do I plant garlic?"), rpc.EventSession)[0].Session
	path := sessionFile(t, s.home, id)

	op(t, s.home.socket, rpc.Request{Op: rpc.OpSessionTag, Session: id, Tags: &rpc.TagChange{Add: []string{"bulbs"}}})
	moved := op(t, s.home.socket, rpc.Request{Op: rpc.OpSessionMove, Session: id, Text: "Garden"})
	if got := eventsOf(moved, rpc.EventSessions); len(got) != 1 || got[0].Sessions[0].Folder != "Garden" ||
		!slices.Equal(got[0].Sessions[0].Tags, []string{"bulbs"}) {
		t.Errorf("move reply = %+v", got)
	}
	// The meta line is plain JSON in the transcript, so grep finds the tag.
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), `"tags":["bulbs"]`) {
		t.Errorf("transcript = %s, %v", b, err)
	}
	folders := op(t, s.home.socket, rpc.Request{Op: rpc.OpChatFolders})
	if got := eventsOf(folders, rpc.EventChatFolders); len(got) != 1 || !slices.Equal(got[0].ChatFolders, []string{"Garden"}) {
		t.Errorf("chat folders = %+v", got)
	}

	op(t, s.home.socket, rpc.Request{Op: rpc.OpSessionDelete, Session: id})
	if !errNotExist(path) {
		t.Error("the transcript is still there after session_delete")
	}
	list := op(t, s.home.socket, rpc.Request{Op: rpc.OpSessions})
	if got := eventsOf(list, rpc.EventSessions); len(got) != 1 || len(got[0].Sessions) != 0 {
		t.Errorf("sessions after delete = %+v", got)
	}
}

func TestIncognitoChat(t *testing.T) {
	t.Parallel()
	s := startStack(t)
	s.fake.enqueue(t, fastModel, directRoute(), directRoute())
	s.fake.enqueue(t, mainModel, fakeollama.Reply{Text: "Noted."}, fakeollama.Reply{Text: "You asked about tulips."})

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var id string
	for ev, err := range rpc.Do(ctx, s.home.socket, rpc.Request{Op: rpc.OpAsk, Text: "Tell me about tulips.", Incognito: true}, nil) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == rpc.EventSession {
			id = ev.Session
			if !ev.Incognito {
				t.Errorf("session event = %+v, want incognito", ev)
			}
		}
	}
	if got := answerOf(ask(t, s.home.socket, id, "What did I ask?")); got != "You asked about tulips." {
		t.Errorf("follow-up answer = %q", got)
	}
	if files := sessionFiles(t, s.home); len(files) != 0 {
		t.Errorf("an incognito chat wrote %v", files)
	}
	op(t, s.home.socket, rpc.Request{Op: rpc.OpSessionDelete, Session: id})
	events := ask(t, s.home.socket, id, "Still there?")
	if last := lastEvent(t, events); last.Type != rpc.EventError || !strings.Contains(last.Error, "incognito") {
		t.Errorf("a question after the chat ended = %+v", last)
	}
}
