// This file runs merud over a fake engine and drives the memory ops the way
// `meru memory` and `meru setup user` do: add, list, forget, and the counts
// in the index status.

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

func TestMemoryOps(t *testing.T) {
	dir := shortDir(t)
	eng := &fakeEngine{version: "0.13.0"}
	d := startDaemon(t, dir, "", eng)

	// merud creates the memory folder at start, so an empty one counts 0.
	if st := status(t, d.sock); st.Memories != 0 || st.Profile != 0 {
		t.Errorf("status before any memory = %d memories, %d profile; want 0, 0", st.Memories, st.Profile)
	}

	added, last := memories(t, d.sock, rpc.Request{Op: rpc.OpMemoryAdd, Kind: "me", Text: "Name is Amit Arora"})
	today := time.Now().Format("2006-01-02")
	if last.Type != rpc.EventDone || len(added) != 1 || added[0].ID != "me/name-is-amit-arora.md" ||
		added[0].Kind != "me" || added[0].Source != "meru" || added[0].Created != today {
		t.Fatalf("add = %+v, closing %+v", added, last)
	}
	if _, last := memories(t, d.sock, rpc.Request{Op: rpc.OpMemoryAdd, Kind: "projects", Text: "Building Meru"}); last.Type != rpc.EventDone {
		t.Fatalf("second add closed with %+v", last)
	}
	// The file is on disk, where the user can read and edit it.
	if _, err := os.Stat(filepath.Join(dir, "memory", "me", "name-is-amit-arora.md")); err != nil {
		t.Errorf("memory file: %v", err)
	}

	list, _ := memories(t, d.sock, rpc.Request{Op: rpc.OpMemoryList})
	if len(list) != 2 || list[0].ID != "me/name-is-amit-arora.md" || list[1].Kind != "projects" {
		t.Errorf("list = %+v, want the two memories by ID", list)
	}
	if st := status(t, d.sock); st.Memories != 2 || st.Profile != 1 {
		t.Errorf("status = %d memories, %d profile; want 2, 1", st.Memories, st.Profile)
	}

	// The next question's system prompt holds the profile, and only the
	// profile kinds: the project waits for recall.
	call(t, d.sock, rpc.Request{Op: rpc.OpAsk, Text: "who am I?"})
	eng.mu.Lock()
	system := eng.system
	eng.mu.Unlock()
	if !strings.Contains(system, "What you know about the user:\n- Name is Amit Arora") || strings.Contains(system, "Building Meru") {
		t.Errorf("system prompt = %q, want the profile and not the project", system)
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

	evs := call(t, d.sock, rpc.Request{Op: rpc.OpMemoryForget, ID: "me/name-is-amit-arora.md"})
	if last := evs[len(evs)-1]; last.Type != rpc.EventDone {
		t.Fatalf("forget = %+v, want done", last)
	}
	if st := status(t, d.sock); st.Memories != 1 || st.Profile != 0 {
		t.Errorf("status after forget = %d memories, %d profile; want 1, 0", st.Memories, st.Profile)
	}
}
