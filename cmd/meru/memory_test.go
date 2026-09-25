// This file tests `meru memory` and `meru setup user` against an
// in-process rpc server that plays merud and keeps its memories in a slice.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/rpc"
)

// fakeMemories plays merud's memory ops. It names each new memory after its
// kind and a counter, as merud names files after their text.
type fakeMemories struct {
	mu   sync.Mutex // guards mems and ops; the server runs handlers on its own goroutines
	mems []rpc.MemoryInfo
	ops  []rpc.Op
}

// handle answers one request the way merud does.
func (f *fakeMemories) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, req.Op)
	switch req.Op {
	case rpc.OpPing:
		return nil
	case rpc.OpMemoryList:
		return emit(rpc.Event{Type: rpc.EventMemories, Memories: slices.Clone(f.mems)})
	case rpc.OpMemoryAdd:
		m := rpc.MemoryInfo{
			ID:      fmt.Sprintf("%s/m%d.md", req.Kind, len(f.mems)+1),
			Kind:    req.Kind,
			Text:    req.Text,
			Created: "2026-09-24",
			Source:  "cli",
		}
		f.mems = append(f.mems, m)
		return emit(rpc.Event{Type: rpc.EventMemories, Memories: []rpc.MemoryInfo{m}})
	case rpc.OpMemoryForget:
		i := slices.IndexFunc(f.mems, func(m rpc.MemoryInfo) bool { return m.ID == req.ID })
		if i < 0 {
			return emit(rpc.Event{Type: rpc.EventError, Error: "no such memory"})
		}
		f.mems = slices.Delete(f.mems, i, i+1)
		return nil
	case rpc.OpAsk:
		return emit(rpc.Event{Type: rpc.EventToken, Text: "I answer questions."})
	}
	return emit(rpc.Event{Type: rpc.EventError, Error: fmt.Sprintf("unknown op %q", req.Op)})
}

// texts returns the text of every memory the fake holds.
func (f *fakeMemories) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.mems {
		out = append(out, m.Kind+" "+m.Text)
	}
	return out
}

// someMemories is what the fake starts with in the list tests.
var someMemories = []rpc.MemoryInfo{
	{ID: "project/garden.md", Kind: "project", Text: "The garden gets two\nraised beds.", Created: "2026-09-20"},
	{ID: "preferences/answers-short.md", Kind: "preferences", Text: "Answers: short", Created: "2026-09-24", Source: "meru setup user"},
	{ID: "me/name-dana-reyes.md", Kind: "me", Text: "Name: Dana Reyes", Created: "2026-09-24", Source: "meru setup user"},
}

func TestMemoryCommand(t *testing.T) {
	all := `me
  me/name-dana-reyes.md         Name: Dana Reyes  2026-09-24 · meru setup user

preferences
  preferences/answers-short.md  Answers: short  2026-09-24 · meru setup user

project
  project/garden.md             The garden gets two raised beds.  2026-09-20
`
	tests := []struct {
		name      string
		start     []rpc.MemoryInfo
		args      []string
		wantCode  int
		wantOut   string
		wantErr   string
		wantTexts []string // what the fake holds afterwards; nil skips the check
	}{
		{name: "list", start: someMemories, args: []string{"list"}, wantOut: all},
		{name: "list one kind", start: someMemories, args: []string{"list", "me"},
			wantOut: "me\n  me/name-dana-reyes.md  Name: Dana Reyes  2026-09-24 · meru setup user\n"},
		{name: "list a kind with none", start: someMemories, args: []string{"list", "work"}, wantOut: "No memories of kind \"work\".\n"},
		{name: "list none", args: []string{"list"}, wantOut: "Meru has no memories yet. Run `meru setup user` to tell it about you.\n"},
		{name: "add", args: []string{"add", "me", "I", "have", "two", "kids"},
			wantOut: "Saved me/m1.md\n", wantTexts: []string{"me I have two kids"}},
		{name: "forget", start: someMemories, args: []string{"forget", "me/name-dana-reyes.md"},
			wantOut:   "Forgot me/name-dana-reyes.md: Name: Dana Reyes\n",
			wantTexts: []string{"project The garden gets two\nraised beds.", "preferences Answers: short"}},
		{name: "forget an unknown ID", start: someMemories, args: []string{"forget", "me/nobody.md"},
			wantCode: 1, wantErr: `no memory has the ID "me/nobody.md"`},
		{name: "add without text", args: []string{"add", "me"}, wantCode: 1, wantErr: "usage: meru memory"},
		{name: "no words", args: nil, wantCode: 1, wantErr: "usage: meru memory"},
		{name: "unknown word", args: []string{"remove", "x"}, wantCode: 1, wantErr: "usage: meru memory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMemories{mems: slices.Clone(tt.start)}
			sock := startServer(t, f.handle)
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock, "memory"}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
			if tt.wantTexts != nil && !slices.Equal(f.texts(), tt.wantTexts) {
				t.Errorf("memories = %q, want %q", f.texts(), tt.wantTexts)
			}
		})
	}
}

func TestMemoryCommandOlderMerud(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		return emit(rpc.Event{Type: rpc.EventError, Error: `unknown op "memory_list"`})
	})
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-socket", sock, "memory", "list"}, &out, &errOut); code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "unknown op") {
		t.Errorf("stderr = %q, want merud's error", errOut.String())
	}
}

// TestSetupUser feeds setup user a person's answers and checks which
// memories merud got and what the person saw.
func TestSetupUser(t *testing.T) {
	known := []rpc.MemoryInfo{
		{ID: "me/name-sam.md", Kind: "me", Text: "Name: Sam"},
		{ID: "project/garden.md", Kind: "project", Text: "Garden beds"},
	}
	tests := []struct {
		name      string
		start     []rpc.MemoryInfo
		input     string
		wantTexts []string
		wantOut   []string
	}{
		{
			name: "first time, every answer",
			input: "Dana Reyes\nstaff engineer at Acme\nBoston\n" +
				"I have two kids\nSam is my brother\n\n" +
				"short, with bullet points\n",
			wantTexts: []string{
				"me Name: Dana Reyes", "me Work: staff engineer at Acme", "me Lives in: Boston",
				"me I have two kids", "me Sam is my brother",
				"preferences Answers: short, with bullet points",
			},
			wantOut: []string{"Saved:", "me/m1.md  Name: Dana Reyes", "preferences/m6.md  Answers: short", "meru memory list", "remember that"},
		},
		{
			name:      "skip everything",
			input:     "\n\n\n\n\n",
			wantTexts: nil,
			wantOut:   []string{"Nothing saved"},
		},
		{
			name:      "keep what Meru knows",
			start:     known,
			input:     "\nDana\n\n\n\n\n",
			wantTexts: []string{"me Name: Sam", "project Garden beds", "me Name: Dana"},
			wantOut:   []string{"already knows", "Name: Sam"},
		},
		{
			name:      "replace what Meru knows",
			start:     known,
			input:     "n\nDana\n\n\n\n\n",
			wantTexts: []string{"project Garden beds", "me Name: Dana"},
			wantOut:   []string{"Forgot all of it"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMemories{mems: slices.Clone(tt.start)}
			sock := startServer(t, f.handle)
			c, out, _ := scripted(tt.input)
			if err := setupUserCmd(context.Background(), sock, c); err != nil {
				t.Fatalf("setup user: %v\n%s", err, out)
			}
			if !slices.Equal(f.texts(), tt.wantTexts) {
				t.Errorf("memories = %q, want %q", f.texts(), tt.wantTexts)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out.String(), "Garden beds") {
				t.Errorf("setup user showed a memory outside the profile:\n%s", out)
			}
		})
	}
}

func TestSetupUserWithoutMerud(t *testing.T) {
	c, _, _ := scripted("")
	err := setupUserCmd(context.Background(), filepath.Join(t.TempDir(), "none.sock"), c)
	if err == nil || !strings.Contains(err.Error(), "is merud running?") {
		t.Errorf("error = %v, want a hint to start merud", err)
	}
}

// TestSetupOffersProfile runs the whole setup against a running merud: it
// offers setup user before the test question and saves the answer.
func TestSetupOffersProfile(t *testing.T) {
	f := &fakeMemories{}
	sock := startServer(t, f.handle)
	writeConfig(t, sock)
	// No download, skip the servers, yes to the profile, a name, skip the rest.
	c, out, _ := scripted("n\n" + strings.Repeat("k\n", len(catalog.Entries())) + "\nDana\n\n\n\n\n")
	if err := setupCmd(context.Background(), sock, c); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if got := f.texts(); !slices.Equal(got, []string{"me Name: Dana"}) {
		t.Errorf("memories = %q, want the name", got)
	}
	about := strings.Index(out.String(), "6. About you")
	test := strings.Index(out.String(), "7. A test question")
	if about < 0 || test < about || !strings.Contains(out.String(), "I answer questions.") {
		t.Errorf("want step 6 about you before step 7 and its answer:\n%s", out)
	}
}

// writeConfig puts a small config.toml next to sock, so setup finds a
// config and skips writing one.
func writeConfig(t *testing.T, sock string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(sock), "config.toml")
	if err := os.WriteFile(path, []byte("profile = \"lite\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
