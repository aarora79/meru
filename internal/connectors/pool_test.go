// This file tests the supervisor behind a real MCP pool, the way merud
// joins them through the pool's Spawn hook.

package connectors

import (
	"context"
	"slices"
	"testing"
	"time"

	// The pool's package is also called mcp, like the SDK's; this name
	// keeps the two apart.
	meruMCP "github.com/aarora79/meru/internal/mcp"
)

// TestPoolWithSupervisor checks the pool's side: it offers the tools from
// the saved list before the program runs, Refresh starts nothing, the
// first call starts the program, a call in flight during a crash fails
// and is never sent again, and closing the pool, as a reload does, leaves
// the program to the supervisor.
func TestPoolWithSupervisor(t *testing.T) {
	s, f, clock := readyConnector(t)
	m := s.Manifest()
	pool, err := meruMCP.NewPool(context.Background(), []meruMCP.ServerConfig{
		{Name: m.ID, Spawn: s, Allow: m.MCP.Allow, Confirm: m.MCP.Confirm},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range pool.Tools() {
		names = append(names, tool.Name)
	}
	want := []string{"obsidian.obsidian_list_vaults", "obsidian.obsidian_read_note", "obsidian.obsidian_search_vault"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	pool.Refresh(context.Background())
	if f.dials.Load() != 1 {
		t.Fatalf("starts = %d after Refresh, want only the check's", f.dials.Load())
	}
	st := pool.Status()[0]
	if !st.Managed || !st.Connected || st.State != StateOK || st.Sentence != "Obsidian is ready. It starts when a question needs it." {
		t.Errorf("status = %+v", st)
	}

	block, started := make(chan struct{}), make(chan struct{}, 1)
	f.set(func(f *fakeConnector) { f.block, f.searchStart = block, started })
	defer close(block)
	errs := make(chan error, 1)
	go func() {
		_, err := pool.Call(context.Background(), "obsidian.obsidian_search_vault", []byte(`{"query":"x"}`))
		errs <- err
	}()
	<-started
	f.crash()
	if err := <-errs; err == nil {
		t.Fatal("the call in flight during the crash succeeded")
	}
	waitPhase(t, s, phaseBackoff)
	if len(pool.Tools()) != 0 {
		t.Error("the pool offers tools while the connector is down")
	}
	if st := pool.Status()[0]; st.Connected || st.State != StateStarting || st.LastError != st.Sentence {
		t.Errorf("status while down = %+v", st)
	}
	clock.Advance(time.Second)
	waitPhase(t, s, phaseOK)
	time.Sleep(50 * time.Millisecond) // time for a replay, were there one
	if n := f.searches.Load(); n != 1 {
		t.Errorf("the search reached the server %d times, want 1", n)
	}

	pool.Close()
	if p := phaseOf(s); p != phaseOK || f.running.Load() != 1 {
		t.Errorf("after the pool closed: phase = %s, programs = %d; want ok and 1", p, f.running.Load())
	}
}
