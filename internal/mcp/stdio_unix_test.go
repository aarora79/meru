// This file checks that Close leaves no stdio child running. It asks the
// kernel about a process ID with signal 0, which only Unix-like systems
// offer, so the build tag below keeps it off Windows.

//go:build unix

package mcp

import (
	"errors"
	"strconv"
	"syscall"
	"testing"
)

func TestCloseStopsChildren(t *testing.T) {
	a := stdioConfig(t, "a")
	b := stdioConfig(t, "b")
	p, err := newPool(t.Context(), []ServerConfig{a, b}, nil, dialTransport)
	if err != nil {
		t.Fatal(err)
	}

	var pids []int
	for _, name := range []string{"a.pid", "b.pid"} {
		pid, err := strconv.Atoi(callText(t, p, name))
		if err != nil {
			t.Fatal(err)
		}
		if !processExists(pid) {
			t.Fatalf("%s: process %d isn't running before Close", name, pid)
		}
		pids = append(pids, pid)
	}

	p.Close()

	// Close waits for the children, so they must be gone, and reaped, the
	// moment it returns. A zombie (exited but not reaped) still answers
	// signal 0, so this also proves Close waited for each child.
	for _, pid := range pids {
		if processExists(pid) {
			t.Errorf("process %d still exists after Close", pid)
		}
	}
}

// processExists reports whether a process with this ID exists. Signal 0
// checks without sending anything; ESRCH means "no such process".
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
