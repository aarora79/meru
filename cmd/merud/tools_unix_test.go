// This file checks that a reload stops the stdio children of the old
// pool. It asks the kernel about a process ID with signal 0, which only
// Unix-like systems offer, so the build tag keeps it off Windows.

//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestMCPReloadStopsOldChildren(t *testing.T) {
	dir := shortDir(t)
	cfgPath := filepath.Join(dir, "config.toml")
	first := filepath.Join(dir, "first.pid")
	second := filepath.Join(dir, "second.pid")
	d := startDaemon(t, dir, stdioServerEntry(t, "local", first, `"echo"`), &fakeEngine{version: "0.13.0"})
	pidA := readPID(t, first)
	if !processExists(pidA) {
		t.Fatalf("server %d isn't running after start", pidA)
	}

	// A changed entry starts a new child and stops the old one.
	if err := os.WriteFile(cfgPath, []byte(stdioServerEntry(t, "local", second, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	evs := call(t, d.sock, rpc.Request{Op: rpc.OpMCPReload})
	if last := lastEvent(evs); last.Type != rpc.EventDone {
		t.Fatalf("reload closed with %+v", last)
	}
	servers := mcpServers(t, evs)
	if len(servers) != 1 || !servers[0].Connected || len(servers[0].Tools) != 0 {
		t.Errorf("after the change, servers = %+v; want local, connected, no tools allowed", servers)
	}
	pidB := readPID(t, second)
	if processExists(pidA) {
		t.Errorf("old server %d still runs after the reload", pidA)
	}
	if !processExists(pidB) {
		t.Errorf("new server %d isn't running", pidB)
	}

	// A removed entry leaves no server and no child.
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	evs = call(t, d.sock, rpc.Request{Op: rpc.OpMCPReload})
	if last := lastEvent(evs); last.Type != rpc.EventDone {
		t.Fatalf("reload closed with %+v", last)
	}
	if got := mcpServers(t, evs); len(got) != 0 {
		t.Errorf("after the removal, servers = %+v, want none", got)
	}
	if processExists(pidB) {
		t.Errorf("removed server %d still runs after the reload", pidB)
	}
	if got := toolNamesOf(mcpServers(t, call(t, d.sock, rpc.Request{Op: rpc.OpTools}))); !slices.Equal(got, nil) {
		t.Errorf("tools = %v, want none", got)
	}
}

// readPID reads the process ID a stdio test server wrote to path.
func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("server never wrote its pid: %v", err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// processExists reports whether a process with this ID exists. Signal 0
// checks without sending anything; ESRCH means "no such process".
func processExists(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
