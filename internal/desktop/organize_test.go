// This file tests the Bridge methods behind the rail's right-click menu,
// the folder and tags on each row, and an incognito chat: the flag on the
// first question and the delete the Bridge sends when the app quits.

package desktop

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// organizeServer answers the chat list ops as merud does and keeps every
// request it gets.
type organizeServer struct {
	mu   sync.Mutex // guards reqs
	reqs []rpc.Request
}

// handle is the rpc.Handler.
func (s *organizeServer) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	switch req.Op {
	case rpc.OpSessions:
		return emit(rpc.Event{Type: rpc.EventSessions, Sessions: []rpc.SessionInfo{
			{ID: "2026-09-27T090000-a1b2", Title: "When do I plant garlic?", Updated: "2026-09-27T09:00:00Z", Folder: "Garden", Tags: []string{"bulbs"}},
		}})
	case rpc.OpSessionMove, rpc.OpSessionTag:
		return emit(rpc.Event{Type: rpc.EventSessions, Sessions: []rpc.SessionInfo{{ID: req.Session}}})
	case rpc.OpChatFolders, rpc.OpChatFolderAdd, rpc.OpChatFolderRename, rpc.OpChatFolderRemove:
		return emit(rpc.Event{Type: rpc.EventChatFolders, ChatFolders: []string{"Garden"}})
	case rpc.OpAsk:
		ev := rpc.Event{Type: rpc.EventSession, Session: "2026-09-28T090000-c3d4"}
		if req.Incognito {
			ev = rpc.Event{Type: rpc.EventSession, Session: "incognito-0123abcd", Incognito: true}
		}
		return emit(ev)
	}
	return nil // session_delete answers "done" alone
}

// requests returns a copy of the requests so far.
func (s *organizeServer) requests() []rpc.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

func TestOrganizeMethods(t *testing.T) {
	srv := &organizeServer{}
	b, _ := newBridge(startServer(t, srv.handle))
	ctx := context.Background()
	id := "2026-09-27T090000-a1b2"

	list, err := b.Sessions(ctx)
	if err != nil || len(list) != 1 || list[0].Folder != "Garden" || !slices.Equal(list[0].Tags, []string{"bulbs"}) {
		t.Fatalf("Sessions = %+v, %v", list, err)
	}
	calls := []struct {
		name string
		run  func() error
		want rpc.Request
	}{
		{"delete", func() error { return b.DeleteSession(ctx, id) }, rpc.Request{Op: rpc.OpSessionDelete, Session: id}},
		{"move", func() error { return b.MoveSession(ctx, id, "Garden") }, rpc.Request{Op: rpc.OpSessionMove, Session: id, Text: "Garden"}},
		{"folders", func() error { _, err := b.ChatFolders(ctx); return err }, rpc.Request{Op: rpc.OpChatFolders}},
		{"add folder", func() error { _, err := b.AddChatFolder(ctx, "Trips"); return err }, rpc.Request{Op: rpc.OpChatFolderAdd, ID: "Trips"}},
		{"rename folder", func() error { _, err := b.RenameChatFolder(ctx, "Trips", "Travel"); return err },
			rpc.Request{Op: rpc.OpChatFolderRename, ID: "Trips", Text: "Travel"}},
		{"remove folder", func() error { _, err := b.RemoveChatFolder(ctx, "Travel"); return err }, rpc.Request{Op: rpc.OpChatFolderRemove, ID: "Travel"}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if err := c.run(); err != nil {
				t.Fatal(err)
			}
			reqs := srv.requests()
			if got := reqs[len(reqs)-1]; got != c.want {
				t.Errorf("request = %+v, want %+v", got, c.want)
			}
		})
	}
	if err := b.TagSession(ctx, id, []string{"garlic"}, []string{"bulbs"}); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	got := reqs[len(reqs)-1]
	if got.Op != rpc.OpSessionTag || got.Tags == nil || !reflect.DeepEqual(*got.Tags, rpc.TagChange{Add: []string{"garlic"}, Remove: []string{"bulbs"}}) {
		t.Errorf("tag request = %+v", got)
	}
}

// TestIncognitoSend checks that the first question of an incognito chat
// carries the flag, that the next one continues it by ID, and that quitting
// tells merud to forget it.
func TestIncognitoSend(t *testing.T) {
	srv := &organizeServer{}
	b, r := newBridge(startServer(t, srv.handle))
	if err := b.Send("", "Tell me about tulips.", "", true); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "end of turn 1", isEnd(1))
	if err := b.Send("incognito-0123abcd", "And daffodils?", "", true); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "end of turn 2", isEnd(2))
	if err := b.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %+v, want two asks and a delete", reqs)
	}
	if !reqs[0].Incognito || reqs[0].Session != "" {
		t.Errorf("first ask = %+v, want a new incognito chat", reqs[0])
	}
	if reqs[1].Incognito || reqs[1].Session != "incognito-0123abcd" {
		t.Errorf("second ask = %+v, want it to continue the chat by ID", reqs[1])
	}
	if reqs[2].Op != rpc.OpSessionDelete || reqs[2].Session != "incognito-0123abcd" {
		t.Errorf("quit sent %+v, want session_delete for the incognito chat", reqs[2])
	}
}
