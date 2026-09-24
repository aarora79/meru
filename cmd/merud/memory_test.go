// This file runs merud over a fake engine and drives the memory ops the way
// `meru memory` and `meru setup user` do: add, list and forget, the counts
// in the index status, and how adds, forgets and hand edits reach recall.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// memories returns the memories in a reply, and the closing event.
func memories(t *testing.T, sock string, req rpc.Request) ([]rpc.MemoryInfo, rpc.Event) {
	t.Helper()
	evs := call(t, sock, req)
	var out []rpc.MemoryInfo
	for _, ev := range evs {
		if ev.Type == rpc.EventMemories {
			out = append(out, ev.Memories...)
		}
	}
	return out, evs[len(evs)-1]
}

// lastSystem returns the system prompt of the engine's latest Stream call.
func (f *fakeEngine) lastSystem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.system
}

func TestMemoryOps(t *testing.T) {
	dir := shortDir(t)
	eng := &fakeEngine{version: "0.13.0"}
	d := startDaemon(t, dir, "", eng)

	// merud creates the memory folder at start, so an empty one counts 0.
	if st := status(t, d.sock); st.Memories != 0 || st.Profile != 0 {
		t.Errorf("status before any memory = %d memories, %d profile; want 0, 0", st.Memories, st.Profile)
	}

	added, last := memories(t, d.sock, rpc.Request{Op: rpc.OpMemoryAdd, Kind: "me", Text: "Name is Dana Reyes"})
	today := time.Now().Format("2006-01-02")
	if last.Type != rpc.EventDone || len(added) != 1 || added[0].ID != "me/name-is-dana-reyes.md" ||
		added[0].Kind != "me" || added[0].Source != "meru" || added[0].Created != today {
		t.Fatalf("add = %+v, closing %+v", added, last)
	}
	if _, last := memories(t, d.sock, rpc.Request{Op: rpc.OpMemoryAdd, Kind: "projects", Text: "Building Meru"}); last.Type != rpc.EventDone {
		t.Fatalf("second add closed with %+v", last)
	}
	// The file is on disk, where the user can read and edit it.
	if _, err := os.Stat(filepath.Join(dir, "memory", "me", "name-is-dana-reyes.md")); err != nil {
		t.Errorf("memory file: %v", err)
	}

	list, _ := memories(t, d.sock, rpc.Request{Op: rpc.OpMemoryList})
	if len(list) != 2 || list[0].ID != "me/name-is-dana-reyes.md" || list[1].Kind != "projects" {
		t.Errorf("list = %+v, want the two memories by ID", list)
	}
	if st := status(t, d.sock); st.Memories != 2 || st.Profile != 1 {
		t.Errorf("status = %d memories, %d profile; want 2, 1", st.Memories, st.Profile)
	}

	// The next question's system prompt holds the profile, with only the
	// profile kinds, and the project in the recalled memories: the add
	// synced it into the store at once.
	call(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "who am I?"})
	system := eng.lastSystem()
	if !strings.Contains(system, "What you know about the user:\n- Name is Dana Reyes\n\n") ||
		!strings.Contains(system, "Things you remember that may matter here:\n- (projects) Building Meru") {
		t.Errorf("system prompt = %q, want the profile, then the project as a recalled memory", system)
	}

	errs := []struct {
		name string
		req  rpc.Request
		want string
	}{
		{"add without a kind", rpc.Request{Op: rpc.OpMemoryAdd, Text: "x"}, "needs a kind"},
		{"add a bad kind", rpc.Request{Op: rpc.OpMemoryAdd, Kind: "../me", Text: "x"}, "letters, digits"},
		{"add no text", rpc.Request{Op: rpc.OpMemoryAdd, Kind: "me"}, "text is empty"},
		{"forget a missing memory", rpc.Request{Op: rpc.OpMemoryForget, ID: "me/nobody.md"}, "meru memory list shows"},
		{"forget outside the folder", rpc.Request{Op: rpc.OpMemoryForget, ID: "../config.toml"}, "isn't a memory ID"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			evs := call(t, d.sock, tt.req)
			if last := evs[len(evs)-1]; last.Type != rpc.EventError || !strings.Contains(last.Error, tt.want) {
				t.Errorf("last event = %+v, want an error containing %q", last, tt.want)
			}
		})
	}

	evs := call(t, d.sock, rpc.Request{Op: rpc.OpMemoryForget, ID: "me/name-is-dana-reyes.md"})
	if last := evs[len(evs)-1]; last.Type != rpc.EventDone {
		t.Fatalf("forget = %+v, want done", last)
	}
	if st := status(t, d.sock); st.Memories != 1 || st.Profile != 0 {
		t.Errorf("status after forget = %d memories, %d profile; want 1, 0", st.Memories, st.Profile)
	}

	// Forgetting the project takes it out of recall on the next question.
	evs = call(t, d.sock, rpc.Request{Op: rpc.OpMemoryForget, ID: "projects/building-meru.md"})
	if last := evs[len(evs)-1]; last.Type != rpc.EventDone {
		t.Fatalf("forget = %+v, want done", last)
	}
	call(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "what am I building?"})
	if system := eng.lastSystem(); strings.Contains(system, "Building Meru") {
		t.Errorf("system prompt = %q, want the forgotten project gone", system)
	}
}

// TestMemoryHandEdit checks merud picks up a memory file written by hand
// while it runs: the watcher syncs it, and the next question recalls it.
func TestMemoryHandEdit(t *testing.T) {
	dir := shortDir(t)
	eng := &fakeEngine{version: "0.13.0"}
	d := startDaemon(t, dir, "", eng)

	path := filepath.Join(dir, "memory", "people", "sam.md")
	write := func() {
		if err := os.WriteFile(path, []byte("Sam is the user's manager\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	deadline := time.Now().Add(10 * time.Second)
	for i := 0; ; i++ {
		call(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "who is Sam?"})
		if strings.Contains(eng.lastSystem(), "- (people) Sam is the user's manager") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hand-written memory never reached the prompt:\n%s", eng.lastSystem())
		}
		// Write again now and then, in case the first write landed
		// before the watcher was listening.
		if i%10 == 9 {
			write()
		}
		time.Sleep(50 * time.Millisecond)
	}
}
