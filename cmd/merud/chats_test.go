// This file tests the ops that organize the chat list: delete, move, tag,
// the chat folder ops, and the incognito cases, over a real transcript
// folder and a real store.

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// chatsFixture holds a history service over a temporary sessions folder
// and store, and the IDs of the chats it made.
type chatsFixture struct {
	h       historyService
	st      *store.Store
	dir     string
	forgot  []string
	garden  string
	kitchen string
}

// newChatsFixture makes two answered chats, replays them into a new store,
// and returns the fixture.
func newChatsFixture(t *testing.T) *chatsFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "e", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &chatsFixture{st: st, dir: filepath.Join(t.TempDir(), "sessions")}
	at := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	for i, q := range []string{"when do I plant garlic?", "how long do I boil an egg?"} {
		s, err := transcript.New(f.dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range []transcript.Line{
			{TS: at, Type: transcript.TypeUser, Text: q},
			{TS: at.Add(time.Second), Type: transcript.TypeAssistant, Text: "In autumn."},
		} {
			if err := s.Append(l); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			f.garden = s.ID()
		} else {
			f.kitchen = s.ID()
		}
	}
	if _, err := st.ReplaySessions(ctx, f.dir); err != nil {
		t.Fatal(err)
	}
	forget := func(id string) bool { f.forgot = append(f.forgot, id); return true }
	f.h = newHistoryService(f.dir, "", st, forget, nil)
	return f
}

// chatEvents returns an emit function and the events it keeps.
func chatEvents() (func(rpc.Event) error, *[]rpc.Event) {
	var evs []rpc.Event
	return func(ev rpc.Event) error { evs = append(evs, ev); return nil }, &evs
}

// sessionsOf lists the sessions through handleSessions.
func sessionsOf(t *testing.T, h historyService) []rpc.SessionInfo {
	t.Helper()
	emit, evs := chatEvents()
	if err := h.handleSessions(0, emit); err != nil {
		t.Fatal(err)
	}
	return (*evs)[0].Sessions
}

func TestHandleDelete(t *testing.T) {
	f := newChatsFixture(t)
	ctx := context.Background()
	tests := []struct {
		name string
		id   string
		ok   bool
	}{
		{"no ID", "", false},
		{"a path", "../../etc/passwd", false},
		{"an incognito chat", "incognito-0123abcd", true},
		{"the garden chat", f.garden, true},
		{"again, once it is gone", f.garden, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := f.h.handleDelete(ctx, tt.id); (err == nil) != tt.ok {
				t.Errorf("handleDelete(%q) = %v", tt.id, err)
			}
		})
	}
	if !reflect.DeepEqual(f.forgot, []string{"incognito-0123abcd"}) {
		t.Errorf("forgot = %v, want the incognito chat", f.forgot)
	}
	if list := sessionsOf(t, f.h); len(list) != 1 || list[0].ID != f.kitchen {
		t.Errorf("sessions = %+v, want the kitchen chat alone", list)
	}
	if hits, _ := f.st.SearchMessageKeyword(ctx, "garlic", "", 5); len(hits) != 0 {
		t.Errorf("search still finds the deleted chat: %+v", hits)
	}
}

func TestHandleMoveAndTag(t *testing.T) {
	f := newChatsFixture(t)
	ctx := context.Background()
	emit, evs := chatEvents()
	if err := f.h.handleMove(ctx, rpc.Request{Session: f.garden, Text: "  Garden  "}, emit); err != nil {
		t.Fatalf("handleMove: %v", err)
	}
	if got := (*evs)[0].Sessions[0]; got.ID != f.garden || got.Folder != "Garden" {
		t.Errorf("move reply = %+v", got)
	}
	if err := f.h.handleTag(ctx, rpc.Request{Session: f.garden, Tags: &rpc.TagChange{Add: []string{"#Bulbs", "autumn"}}}, emit); err != nil {
		t.Fatalf("handleTag: %v", err)
	}
	if err := f.h.handleTag(ctx, rpc.Request{Session: f.garden, Tags: &rpc.TagChange{Remove: []string{"autumn"}}}, emit); err != nil {
		t.Fatalf("handleTag remove: %v", err)
	}
	got := (*evs)[2].Sessions[0]
	if got.Folder != "Garden" || !reflect.DeepEqual(got.Tags, []string{"bulbs"}) {
		t.Errorf("tag reply = %+v", got)
	}
	// The tag reaches the session search at once.
	if hits, err := f.st.SearchSummaryKeyword(ctx, "bulbs", "", 5); err != nil || len(hits) != 1 || hits[0].Session != f.garden {
		t.Errorf("tag search = %+v, %v", hits, err)
	}
	// The new folder joined the list.
	emit2, evs2 := chatEvents()
	if err := f.h.handleChatFolders(emit2); err != nil || !reflect.DeepEqual((*evs2)[0].ChatFolders, []string{"Garden"}) {
		t.Errorf("folders = %+v, %v", *evs2, err)
	}
	// Out of the folder again.
	if err := f.h.handleMove(ctx, rpc.Request{Session: f.garden}, emit); err != nil {
		t.Fatal(err)
	}
	for _, s := range sessionsOf(t, f.h) {
		if s.Folder != "" {
			t.Errorf("%s still sits in %q", s.ID, s.Folder)
		}
	}

	bad := []rpc.Request{
		{Session: "incognito-0123abcd", Text: "Garden"},
		{Session: "2026-01-01T000000-0000", Text: "Garden"},
		{Session: f.garden, Text: "bad\x07name"},
	}
	for _, req := range bad {
		if err := f.h.handleMove(ctx, req, emit); err == nil {
			t.Errorf("handleMove(%+v) worked", req)
		}
	}
	for _, tags := range []*rpc.TagChange{nil, {}, {Add: []string{"two words"}}} {
		if err := f.h.handleTag(ctx, rpc.Request{Session: f.garden, Tags: tags}, emit); err == nil {
			t.Errorf("handleTag(%+v) worked", tags)
		}
	}
}

func TestChatFolderOps(t *testing.T) {
	f := newChatsFixture(t)
	ctx := context.Background()
	emit, evs := chatEvents()
	last := func() []string { return (*evs)[len(*evs)-1].ChatFolders }

	if err := f.h.handleChatFolderAdd(rpc.Request{ID: "Recipes"}, emit); err != nil {
		t.Fatal(err)
	}
	if err := f.h.handleChatFolderAdd(rpc.Request{ID: "Recipes"}, emit); err != nil || !slices.Equal(last(), []string{"Recipes"}) {
		t.Errorf("adding twice = %v, %v", last(), err)
	}
	if err := f.h.handleMove(ctx, rpc.Request{Session: f.kitchen, Text: "Recipes"}, emit); err != nil {
		t.Fatal(err)
	}
	if err := f.h.handleTag(ctx, rpc.Request{Session: f.kitchen, Tags: &rpc.TagChange{Add: []string{"eggs"}}}, emit); err != nil {
		t.Fatal(err)
	}
	if err := f.h.handleChatFolderAdd(rpc.Request{ID: "Garden"}, emit); err != nil {
		t.Fatal(err)
	}

	// Rename moves the chat with its folder and keeps its tags.
	if err := f.h.handleChatFolderRename(ctx, rpc.Request{ID: "Recipes", Text: "Cooking"}, emit); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !slices.Equal(last(), []string{"Cooking", "Garden"}) {
		t.Errorf("folders after rename = %v", last())
	}
	for _, s := range sessionsOf(t, f.h) {
		if s.ID == f.kitchen && (s.Folder != "Cooking" || !slices.Equal(s.Tags, []string{"eggs"})) {
			t.Errorf("kitchen chat after rename = %+v", s)
		}
	}
	for _, req := range []rpc.Request{{ID: "Cooking", Text: "Garden"}, {ID: "Nowhere", Text: "Else"}, {ID: "Cooking", Text: ""}} {
		if err := f.h.handleChatFolderRename(ctx, req, emit); err == nil {
			t.Errorf("rename %+v worked", req)
		}
	}

	// Remove moves the chat back to the main list and deletes no chat.
	if err := f.h.handleChatFolderRemove(ctx, rpc.Request{ID: "Cooking"}, emit); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !slices.Equal(last(), []string{"Garden"}) {
		t.Errorf("folders after remove = %v", last())
	}
	list := sessionsOf(t, f.h)
	if len(list) != 2 {
		t.Fatalf("sessions after remove = %+v, want both chats", list)
	}
	for _, s := range list {
		if s.Folder != "" {
			t.Errorf("%s still in %q", s.ID, s.Folder)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, transcript.FoldersFile)); err != nil {
		t.Errorf("folders.json: %v", err)
	}
}

// TestSaveIncognito checks that save_file refuses an incognito chat before
// it touches anything.
func TestSaveIncognito(t *testing.T) {
	s := saveService{sessionsDir: t.TempDir(), outputDir: t.TempDir(), now: time.Now}
	err := s.handleSave(context.Background(), rpc.Request{Kind: rpc.SaveChat, Session: "incognito-0123abcd"}, nil, nil)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("handleSave = %v, want the incognito refusal", err)
	}
}
