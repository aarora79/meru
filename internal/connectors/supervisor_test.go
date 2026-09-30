// This file tests the Supervisor's state machine with a fake clock and a
// fake connector: an MCP server that runs inside the test, reached over
// an in-memory transport, which a test can kill to play a crash. No test
// here starts a program, installs anything or waits out a real backoff.

package connectors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
)

// fakeClock is a Clock a test moves by hand with Advance. Its timers fire
// inside Advance, on the test's goroutine, in the order they fall due.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// fakeTimer is one wait on a fakeClock.
type fakeTimer struct {
	c     *fakeClock
	at    time.Time
	f     func()
	ended bool // fired or stopped
}

// newFakeClock returns a fakeClock that starts at a fixed time.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)}
}

// Now returns the clock's time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc sets a timer that fires f once Advance passes d from now.
func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// Stop calls the timer off, and reports whether it hadn't ended yet.
func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.ended
	t.ended = true
	return was
}

// Advance moves the clock on by d and fires every timer that falls due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.ended && !t.at.After(c.now) {
			t.ended = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	// The timers run without the clock's lock, as real ones do.
	for _, t := range due {
		t.f()
	}
}

// fakeConnector plays the connector's program: each dial starts a fresh
// in-memory MCP server offering Obsidian's three tools. A test can kill
// the running one, make the next starts fail, or hold a search call open.
type fakeConnector struct {
	t *testing.T

	installs atomic.Int32 // calls of install
	dials    atomic.Int32 // programs started
	searches atomic.Int32 // obsidian_search_vault calls that reached the server
	running  atomic.Int32 // programs running now

	mu          sync.Mutex // guards the fields below
	installed   bool
	installErr  error
	dialErr     error
	stderrLine  string // written to stderr at each start
	kill        context.CancelFunc
	block       chan struct{} // when set, a search waits until it closes
	searchStart chan struct{} // when set, a search sends on it once it is running
	values      map[string]string
	wg          sync.WaitGroup // the servers' goroutines
}

// newFakeConnector returns a fake that isn't installed yet, and waits for
// its servers when the test ends.
func newFakeConnector(t *testing.T) *fakeConnector {
	f := &fakeConnector{t: t}
	t.Cleanup(f.wg.Wait)
	return f
}

// set changes the fake's fields under its lock.
func (f *fakeConnector) set(change func(f *fakeConnector)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// crash kills the running program, as a crash would.
func (f *fakeConnector) crash() {
	f.mu.Lock()
	kill := f.kill
	f.mu.Unlock()
	if kill == nil {
		f.t.Fatal("crash: no program is running")
	}
	kill()
}

// fakeVersion is the version the fake installs.
const fakeVersion = "2.0.1"

// hook fills in s's machine steps with the fake's.
func (f *fakeConnector) hook(s *Supervisor) {
	inst := Installed{ID: s.m.ID, Version: fakeVersion, Dir: "/fake/pkg"}
	s.installed = func() (Installed, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return inst, f.installed
	}
	s.install = func(context.Context, func(string)) (Installed, error) {
		f.installs.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.installErr != nil {
			return Installed{}, f.installErr
		}
		f.installed = true
		return inst, nil
	}
	s.launch = func(_ Installed, values map[string]string) (Cmd, error) {
		f.mu.Lock()
		f.values = values
		f.mu.Unlock()
		return Cmd{Path: "/fake/node"}, nil
	}
	s.dial = f.dial
}

// dial starts a fresh fake server and returns the client's end of its
// in-memory transport. The server stops when procCtx ends or on crash.
func (f *fakeConnector) dial(procCtx context.Context, _ Cmd, stderr *tailLog) (mcp.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	f.dials.Add(1)
	if f.stderrLine != "" {
		_, _ = stderr.Write([]byte(f.stderrLine + "\n"))
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-obsidian", Version: fakeVersion}, nil)
	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}
	mcp.AddTool(server, &mcp.Tool{Name: "obsidian_list_vaults", Description: "List vaults."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return text("notes"), nil, nil
		})
	block, started := f.block, f.searchStart
	// killed ends when the program stops or the test kills it, so a held
	// call lets go too.
	killed, kill := context.WithCancel(procCtx)
	mcp.AddTool(server, &mcp.Tool{Name: "obsidian_search_vault", Description: "Search."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct {
			Query string `json:"query"`
		}) (*mcp.CallToolResult, any, error) {
			f.searches.Add(1)
			if started != nil {
				started <- struct{}{}
			}
			if block != nil {
				select {
				case <-block:
				case <-killed.Done():
				case <-ctx.Done():
				}
			}
			return text("found"), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "obsidian_read_note", Description: "Read."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return text("a note"), nil, nil
		})

	clientEnd, serverEnd := mcp.NewInMemoryTransports()
	end := &closableTransport{Transport: serverEnd}
	// A crash cuts the server's end of the pipe first, as a dying process
	// closes its stdout, so a call in flight gets no answer.
	f.kill = func() {
		end.close()
		kill()
	}
	f.running.Add(1)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		defer f.running.Add(-1)
		defer kill()
		_ = server.Run(killed, end)
	}()
	return clientEnd, nil
}

// closableTransport is a server's end of an in-memory transport that a
// test can cut, as a crash does: close shuts the connection at once, and
// no pending answer goes out.
type closableTransport struct {
	mcp.Transport

	mu   sync.Mutex
	conn mcp.Connection
}

// Connect connects the underlying transport and keeps the connection.
func (c *closableTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := c.Transport.Connect(ctx)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return conn, err
}

// close cuts the connection, if there is one.
func (c *closableTransport) close() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// obsidian returns the real Obsidian manifest.
func obsidian(t *testing.T) Manifest {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == "obsidian" {
			return m
		}
	}
	t.Fatal("no obsidian manifest")
	return Manifest{}
}

// testSupervisor returns a supervisor for the Obsidian manifest over the
// fake connector and a fake clock, with its state file in a temporary
// folder. It closes the supervisor when the test ends.
func testSupervisor(t *testing.T, f *fakeConnector) (*Supervisor, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	s := newSupervisor(obsidian(t), nil, clock, t.TempDir(), filepath.Join(t.TempDir(), "state", "obsidian.json"))
	f.hook(s)
	t.Cleanup(s.Close)
	return s, clock
}

// vaultTable returns a [connectors.obsidian] table that turns the
// connector on with a real vault folder.
func vaultTable(t *testing.T) config.Connector {
	return config.Connector{"enabled": true, "vault_path": t.TempDir()}
}

// phaseOf returns s's phase.
func phaseOf(s *Supervisor) phase {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// waitPhase waits up to five seconds for s to reach want.
func waitPhase(t *testing.T, s *Supervisor, want phase) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if phaseOf(s) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, sentence := s.State()
	t.Fatalf("phase = %s (%q), want %s", phaseOf(s), sentence, want)
}

// readyConnector returns a supervisor that has installed and checked the
// fake, so it stands ready with no program running.
func readyConnector(t *testing.T) (*Supervisor, *fakeConnector, *fakeClock) {
	t.Helper()
	f := newFakeConnector(t)
	s, clock := testSupervisor(t, f)
	s.Configure(vaultTable(t), nil, false)
	waitPhase(t, s, phaseReady)
	return s, f, clock
}

// call runs one obsidian_search_vault call through Spawn, as the pool
// does, and returns its error.
func call(t *testing.T, s *Supervisor) error {
	t.Helper()
	cs, done, err := s.Spawn(context.Background(), 5*time.Second)
	if err != nil {
		return err
	}
	defer done()
	_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "obsidian_search_vault", Arguments: map[string]any{"query": "x"}})
	return err
}

// TestConfigureStates checks the state and sentence each kind of config
// gives, before anything installs.
func TestConfigureStates(t *testing.T) {
	vault := t.TempDir()
	tests := []struct {
		name     string
		table    config.Connector
		byHand   bool
		state    string
		sentence string
		fix      []string
	}{
		{"no table is off", nil, false, StateOff, "Obsidian is off.", nil},
		{"enabled false is off", config.Connector{"enabled": false, "vault_path": vault}, false, StateOff, "Obsidian is off.", nil},
		{"a hand-added server wins", config.Connector{"enabled": true, "vault_path": vault}, true, StateByHand,
			"Obsidian is set up by hand, as the obsidian entry in [[mcp.servers]].", nil},
		{"by hand while off", nil, true, StateByHand, "Obsidian is set up by hand, as the obsidian entry in [[mcp.servers]].", nil},
		{"no vault folder", config.Connector{"enabled": true}, false, StateNeedsConfig, "Obsidian needs your vault folder.", []string{"vault_path"}},
		{"a vault folder that isn't there", config.Connector{"enabled": true, "vault_path": "/no/such/vault"}, false, StateNeedsConfig,
			"Obsidian can't find the vault folder /no/such/vault.", []string{"vault_path"}},
		{"a vault name of the wrong form", config.Connector{"enabled": true, "vault_path": vault, "vault_name": "My Notes"}, false,
			StateNeedsConfig, "Obsidian's vault name doesn't fit the form it needs (^[a-z][a-z0-9_-]*$).", []string{"vault_name"}},
		{"a key the manifest lacks", config.Connector{"enabled": true, "vault_path": vault, "vault": "x"}, false, StateNeedsConfig,
			"Obsidian has no setting called vault; remove it from [connectors.obsidian].", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeConnector(t)
			s, _ := testSupervisor(t, f)
			s.Configure(tt.table, nil, tt.byHand)
			st := s.Status()
			if st.State != tt.state || st.Sentence != tt.sentence || !slices.Equal(st.Fix, tt.fix) {
				t.Errorf("status = %s %q fix %v, want %s %q fix %v", st.State, st.Sentence, st.Fix, tt.state, tt.sentence, tt.fix)
			}
			if s.Tools() != nil {
				t.Error("tools offered in a state that isn't ok")
			}
			if _, _, err := s.Spawn(context.Background(), time.Second); err == nil || err.Error() != tt.sentence {
				t.Errorf("Spawn error = %v, want %q", err, tt.sentence)
			}
			if f.installs.Load() != 0 || f.dials.Load() != 0 {
				t.Errorf("installs = %d, starts = %d; want none", f.installs.Load(), f.dials.Load())
			}
		})
	}
}

// TestNeedsConfigThenInstallThenReady walks the first run: the user turns
// the connector on without a vault, then gives one. The supervisor
// installs, checks the program once, keeps its tools and stops it.
func TestNeedsConfigThenInstallThenReady(t *testing.T) {
	f := newFakeConnector(t)
	s, _ := testSupervisor(t, f)
	s.Configure(config.Connector{"enabled": true}, nil, false)
	if st := s.Status(); st.State != StateNeedsConfig {
		t.Fatalf("state = %s, want needs_config", st.State)
	}

	vault := filepath.Join(t.TempDir(), "My Vault")
	if err := os.Mkdir(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Configure(config.Connector{"enabled": true, "vault_path": vault}, nil, false)
	waitPhase(t, s, phaseReady)

	st := s.Status()
	if st.State != StateOK || st.Sentence != "Obsidian is ready. It starts when a question needs it." {
		t.Errorf("status = %s %q", st.State, st.Sentence)
	}
	if f.installs.Load() != 1 || f.dials.Load() != 1 {
		t.Errorf("installs = %d, starts = %d; want one of each", f.installs.Load(), f.dials.Load())
	}
	waitFor(t, "the check's program to stop", func() bool { return f.running.Load() == 0 })
	if got := len(s.Tools()); got != 3 {
		t.Errorf("tools = %d, want the 3 the check listed", got)
	}
	// The vault name comes from the folder's name.
	f.mu.Lock()
	name := f.values["vault_name"]
	f.mu.Unlock()
	if name != "my-vault" {
		t.Errorf("vault_name = %q, want my-vault", name)
	}
	if _, ok := readToolCache(s.state, "obsidian", fakeVersion); !ok {
		t.Error("the tool list wasn't saved")
	}

	// A second supervisor over the same files starts ready at once, from
	// the saved list, with no install and no program.
	s2 := newSupervisor(obsidian(t), nil, newFakeClock(), "", s.state)
	f.hook(s2)
	t.Cleanup(s2.Close)
	s2.Configure(config.Connector{"enabled": true, "vault_path": vault}, nil, false)
	if p := phaseOf(s2); p != phaseReady {
		t.Errorf("phase = %s, want ready from the saved list", p)
	}
	if f.installs.Load() != 1 || f.dials.Load() != 1 {
		t.Errorf("installs = %d, starts = %d after the second supervisor; want 1 and 1", f.installs.Load(), f.dials.Load())
	}
}

// TestInstallFailure checks that a failed install sets failed with the
// reason, and that a reload tries again.
func TestInstallFailure(t *testing.T) {
	f := newFakeConnector(t)
	f.set(func(f *fakeConnector) {
		f.installErr = errors.New("connector obsidian: npm install: exit status 1\nnpm ERR! 404")
	})
	s, _ := testSupervisor(t, f)
	table := vaultTable(t)
	s.Configure(table, nil, false)
	waitPhase(t, s, phaseFailed)
	if _, sentence := s.State(); sentence != "Obsidian couldn't install: npm install: exit status 1." {
		t.Errorf("sentence = %q", sentence)
	}

	f.set(func(f *fakeConnector) { f.installErr = nil })
	s.Configure(table, nil, false)
	waitPhase(t, s, phaseReady)
}

// TestLazyStartAndIdleStop checks that a ready connector starts on its
// first call, stays up while calls run, and stops after its idle
// timeout, back to ready.
func TestLazyStartAndIdleStop(t *testing.T) {
	s, f, clock := readyConnector(t)
	if f.running.Load() != 0 {
		t.Fatal("a program runs before any call")
	}
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}
	if p := phaseOf(s); p != phaseOK {
		t.Fatalf("phase = %s after a call, want ok", p)
	}
	if _, sentence := s.State(); sentence != "Obsidian is running." {
		t.Errorf("sentence = %q", sentence)
	}
	if f.dials.Load() != 2 { // the check, then the start
		t.Errorf("starts = %d, want 2", f.dials.Load())
	}

	// A call that is still running holds the program up past the timeout.
	cs, done, err := s.Spawn(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(11 * time.Minute)
	if p := phaseOf(s); p != phaseOK {
		t.Errorf("phase = %s while a call runs, want ok", p)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "obsidian_read_note"}); err != nil {
		t.Fatal(err)
	}
	done()
	done() // a second done changes nothing

	clock.Advance(9 * time.Minute)
	if p := phaseOf(s); p != phaseOK {
		t.Errorf("phase = %s before the idle timeout, want ok", p)
	}
	clock.Advance(time.Minute)
	if p := phaseOf(s); p != phaseReady {
		t.Fatalf("phase = %s after the idle timeout, want ready", p)
	}
	waitFor(t, "the idle program to stop", func() bool { return f.running.Load() == 0 })
	if len(s.Tools()) != 3 {
		t.Error("a ready connector should still offer its tools")
	}

	// The next call starts it again.
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}
	if f.dials.Load() != 3 {
		t.Errorf("starts = %d, want 3", f.dials.Load())
	}
}

// TestCrashBackoffRestart checks that a crash waits 1 s, then 2 s after a
// second crash, before the program starts again, and that tools go away
// while it is down.
func TestCrashBackoffRestart(t *testing.T) {
	s, f, clock := readyConnector(t)
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}

	f.crash()
	waitPhase(t, s, phaseBackoff)
	if st, sentence := s.State(); st != StateStarting || !strings.Contains(sentence, "starts again in 1 s") {
		t.Errorf("state = %s %q", st, sentence)
	}
	if s.Tools() != nil {
		t.Error("tools offered while the program is down")
	}
	clock.Advance(time.Second)
	waitPhase(t, s, phaseOK)
	if f.dials.Load() != 3 {
		t.Errorf("starts = %d, want 3", f.dials.Load())
	}

	f.crash()
	waitPhase(t, s, phaseBackoff)
	clock.Advance(time.Second)
	if p := phaseOf(s); p != phaseBackoff {
		t.Fatalf("phase = %s one second into a two-second wait", p)
	}
	clock.Advance(time.Second)
	waitPhase(t, s, phaseOK)
	if err := call(t, s); err != nil {
		t.Errorf("a call after the restart: %v", err)
	}
}

// TestFiveCrashesFail checks that the fifth crash within ten minutes sets
// failed with the program's last line of error output, and that crashes
// older than the window don't count.
func TestFiveCrashesFail(t *testing.T) {
	s, f, clock := readyConnector(t)
	f.set(func(f *fakeConnector) { f.stderrLine = "vault is locked" })
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}
	crashAndRestart := func() {
		t.Helper()
		f.crash()
		waitPhase(t, s, phaseBackoff)
		clock.Advance(time.Minute) // longer than any wait
		waitPhase(t, s, phaseOK)
	}
	// Four crashes, then eleven quiet minutes: the window forgets them.
	for range 4 {
		crashAndRestart()
	}
	clock.Advance(11 * time.Minute) // the idle timeout stops it too
	waitPhase(t, s, phaseReady)
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		crashAndRestart()
	}
	f.crash()
	waitPhase(t, s, phaseFailed)
	st, sentence := s.State()
	if st != StateFailed || sentence != "Obsidian keeps stopping: vault is locked." {
		t.Errorf("state = %s %q", st, sentence)
	}
	if s.Tools() != nil {
		t.Error("a failed connector offers tools")
	}
	if err := call(t, s); err == nil || !strings.Contains(err.Error(), "keeps stopping") {
		t.Errorf("a call to a failed connector: %v", err)
	}
}

// TestFailedStartsCount checks that a start that fails counts as a crash,
// and that the call waiting for it gets the error at once.
func TestFailedStartsCount(t *testing.T) {
	s, f, clock := readyConnector(t)
	f.set(func(f *fakeConnector) { f.dialErr = errors.New("exec format error") })
	err := call(t, s)
	if err == nil || !strings.Contains(err.Error(), "couldn't start: exec format error") {
		t.Fatalf("call = %v, want the start's error", err)
	}
	for range 4 {
		clock.Advance(time.Minute)
		waitFor(t, "the next failed start", func() bool { p := phaseOf(s); return p == phaseBackoff || p == phaseFailed })
	}
	if p := phaseOf(s); p != phaseFailed {
		t.Errorf("phase = %s after five failed starts, want failed", p)
	}
}

// TestCallDuringCrashIsNotReplayed checks the guarantee a server added by
// hand has: a call running when the program crashes fails with its
// error, and nothing sends it again once the program is back.
func TestCallDuringCrashIsNotReplayed(t *testing.T) {
	s, f, clock := readyConnector(t)
	block, started := make(chan struct{}), make(chan struct{}, 1)
	f.set(func(f *fakeConnector) { f.block, f.searchStart = block, started })
	defer close(block)

	errs := make(chan error, 1)
	go func() { errs <- call(t, s) }()
	<-started
	f.crash()
	if err := <-errs; err == nil {
		t.Fatal("the call in flight during the crash succeeded")
	}
	waitPhase(t, s, phaseBackoff)
	clock.Advance(time.Second)
	waitPhase(t, s, phaseOK)
	time.Sleep(50 * time.Millisecond) // time for a replay, were there one
	if n := f.searches.Load(); n != 1 {
		t.Errorf("the search reached the server %d times, want 1", n)
	}
}

// TestReloadKeepsARunningProgram checks that Configure with the same
// config leaves a running program alone, and that byHand stops it.
func TestReloadKeepsARunningProgram(t *testing.T) {
	f := newFakeConnector(t)
	s, _ := testSupervisor(t, f)
	table := vaultTable(t)
	s.Configure(table, nil, false)
	waitPhase(t, s, phaseReady)
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}
	s.Configure(table, nil, false)
	if p := phaseOf(s); p != phaseOK || f.dials.Load() != 2 {
		t.Errorf("phase = %s, starts = %d after a reload that changed nothing", p, f.dials.Load())
	}
	s.Configure(table, nil, true)
	if p := phaseOf(s); p != phaseByHand {
		t.Errorf("phase = %s, want by_hand", p)
	}
	waitFor(t, "the program to stop", func() bool { return f.running.Load() == 0 })
}

// TestCloseStopsEverything checks that Close stops the program and that
// Spawn fails after it.
func TestCloseStopsEverything(t *testing.T) {
	s, f, _ := readyConnector(t)
	if err := call(t, s); err != nil {
		t.Fatal(err)
	}
	s.Close()
	waitFor(t, "the program to stop after Close", func() bool { return f.running.Load() == 0 })
	if err := call(t, s); err == nil {
		t.Error("Spawn worked after Close")
	}
}

// TestVaultName checks the vault names Meru makes up from folder names.
func TestVaultName(t *testing.T) {
	for in, want := range map[string]string{
		"Notes": "notes", "My Vault": "my-vault", "2026 notes": "notes", "___": "vault", "work_notes": "work_notes",
	} {
		if got := vaultName(in); got != want {
			t.Errorf("vaultName(%q) = %q, want %q", in, got, want)
		}
	}
}

// waitFor waits up to five seconds for cond to hold.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
