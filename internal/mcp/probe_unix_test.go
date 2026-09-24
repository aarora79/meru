// This file checks that a probe that fails part-way leaves no child
// running. processExists uses a Unix-only signal, so the build tag keeps
// the file off Windows.

//go:build unix

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeTimeoutStopsChild(t *testing.T) {
	// hang never finishes the handshake; hanglist finishes it and then
	// never lists the tools.
	for _, mode := range []string{"hang", "hanglist"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			cfg := stdioConfig(t, "t")
			cfg.Env[testServerEnv] = mode
			cfg.Env[testPIDEnv] = pidFile

			start := time.Now()
			_, err := probe(context.Background(), cfg, nil, dialTransport, time.Second)
			if err == nil || !strings.Contains(err.Error(), "no answer within 1s") ||
				!strings.Contains(err.Error(), "try again") {
				t.Fatalf("probe error = %v, want a timeout that says to try again", err)
			}
			if took := time.Since(start); took > 10*time.Second {
				t.Errorf("probe took %s to give up", took)
			}

			raw, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatalf("the child never wrote its pid: %v", err)
			}
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			// Probe waits for the child, so it must be gone, and reaped,
			// by the time Probe returns.
			if processExists(pid) {
				t.Errorf("process %d still exists after the probe failed", pid)
			}
		})
	}
}

func TestProbeStopsChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	cfg := stdioConfig(t, "t")
	cfg.Env[testPIDEnv] = pidFile
	if _, err := Probe(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if processExists(pid) {
		t.Errorf("process %d still exists after a probe that worked", pid)
	}
}
