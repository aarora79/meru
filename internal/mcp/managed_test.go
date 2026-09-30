// This file tests the Spawn hook: a managed server, whose program a
// supervisor runs, next to a server added by hand in the same Pool. The
// supervisor here is a fake that hands out sessions to the in-memory test
// server. internal/connectors tests the real supervisor behind a Pool.

package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSpawner is a Spawner over one in-memory test server. It counts the
// sessions it hands out and the calls that ended, and a test sets what
// Tools and State report, or makes Spawn fail.
type fakeSpawner struct {
	t *testing.T

	spawns atomic.Int32
	dones  atomic.Int32

	mu       sync.Mutex
	session  *mcp.ClientSession
	tools    []*mcp.Tool
	state    string
	sentence string
	err      error
}

// newFakeSpawner connects a client to a fresh test server, lists its
// tools, and reports the state "ok".
func newFakeSpawner(t *testing.T) *fakeSpawner {
	t.Helper()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := newTestServer().Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := newClient(nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	tools, err := listTools(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSpawner{t: t, session: cs, tools: tools, state: "ok", sentence: "Notes is ready."}
}

// Spawn hands out the session, or the error a test set.
func (f *fakeSpawner) Spawn(context.Context, time.Duration) (*mcp.ClientSession, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, nil, f.err
	}
	f.spawns.Add(1)
	return f.session, func() { f.dones.Add(1) }, nil
}

// Tools returns what the test set.
func (f *fakeSpawner) Tools() []*mcp.Tool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools
}

// State returns what the test set.
func (f *fakeSpawner) State() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.sentence
}

// down makes the fake report a connector that isn't taking calls.
func (f *fakeSpawner) down(state, sentence string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools, f.state, f.sentence, f.err = nil, state, sentence, errors.New(sentence)
}

// TestManagedServer checks the Pool's side of the Spawn hook: NewPool and
// Refresh start nothing, Tools follows the supervisor, Call asks Spawn and
// ends with done, and a supervisor that isn't taking calls makes the call
// fail with ErrUnavailable and its sentence.
func TestManagedServer(t *testing.T) {
	f := newFakeSpawner(t)
	var dials atomic.Int32
	p, err := newPool(context.Background(), []ServerConfig{
		{Name: "notes", Spawn: f, Allow: []string{"echo", "add"}, Confirm: []string{"add"}},
		{Name: "byhand", Command: "unused", Allow: []string{"echo"}},
	}, nil, countingDial(t, &dials, &atomic.Bool{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if dials.Load() != 1 {
		t.Errorf("dials = %d, want 1: only the server added by hand connects at startup", dials.Load())
	}
	if f.spawns.Load() != 0 {
		t.Fatal("NewPool asked the supervisor for a session")
	}
	p.Refresh(context.Background())
	if f.spawns.Load() != 0 {
		t.Fatal("Refresh asked the supervisor for a session")
	}

	want := []string{"notes.add", "notes.echo", "byhand.echo"}
	if got := toolNames(p); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	if !p.NeedsConfirm("notes.add") || p.NeedsConfirm("notes.echo") {
		t.Error("the managed server's confirm list isn't applied")
	}
	if got := callText(t, p, "notes.echo", `{"text":"hi"}`); got != "hi" {
		t.Errorf("echo = %q", got)
	}
	if f.spawns.Load() != 1 || f.dones.Load() != 1 {
		t.Errorf("spawns = %d, dones = %d; want 1 and 1", f.spawns.Load(), f.dones.Load())
	}
	if _, err := p.Call(context.Background(), "notes.secret", nil); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a tool outside the allow list: %v", err)
	}

	st := p.Status()[0]
	if !st.Managed || !st.Connected || st.State != "ok" || st.Sentence != "Notes is ready." || st.Allowed != 2 || st.Transport != "stdio" {
		t.Errorf("status = %+v", st)
	}
	if hand := p.Status()[1]; hand.Managed || hand.State != "" || !hand.Connected {
		t.Errorf("the server added by hand reports %+v", hand)
	}

	f.down("needs_config", "Notes needs your folder.")
	if got := toolNames(p); !slices.Equal(got, []string{"byhand.echo"}) {
		t.Errorf("tools while down = %v", got)
	}
	_, err = p.Call(context.Background(), "notes.echo", []byte(`{"text":"x"}`))
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "Notes needs your folder.") {
		t.Errorf("call while down: %v", err)
	}
	st = p.Status()[0]
	if st.Connected || st.State != "needs_config" || st.LastError != "Notes needs your folder." || st.Offered != 0 {
		t.Errorf("status while down = %+v", st)
	}
}

// TestManagedValidate checks that a managed entry takes none of the keys
// that say how to reach a server added by hand.
func TestManagedValidate(t *testing.T) {
	f := &fakeSpawner{}
	tests := []struct {
		name string
		cfg  ServerConfig
		ok   bool
	}{
		{"spawn alone", ServerConfig{Name: "notes", Spawn: f, Allow: []string{"echo"}}, true},
		{"spawn and command", ServerConfig{Name: "notes", Spawn: f, Command: "x"}, false},
		{"spawn and url", ServerConfig{Name: "notes", Spawn: f, URL: "http://127.0.0.1:1/mcp"}, false},
		{"spawn and env", ServerConfig{Name: "notes", Spawn: f, Env: map[string]string{"A": "b"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate = %v, want ok %v", err, tt.ok)
			}
		})
	}
}
