//go:build e2e

// This file tests merud when Ollama isn't there at start, or is too old:
// merud keeps its socket, the connectors op and `meru mcp status` say
// what is wrong, a question gets a clear error, and once a new enough
// Ollama answers, merud warms the models and answers.

package e2e

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// freeAddr returns a loopback host:port where nothing listens now: a port
// the system handed out and took back.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// waitMCPStatus runs `meru mcp status` until its output holds want, and
// returns it. Each run also makes merud check Ollama at once.
func waitMCPStatus(t *testing.T, h *home, want string) string {
	t.Helper()
	var out string
	waitFor(t, 20*time.Second, "meru mcp status to say "+want, func() bool {
		res := runMeru(t, h, "mcp", "status")
		out = res.stdout
		return res.code == 0 && strings.Contains(out, want)
	})
	return out
}

// TestOllamaStartsLate starts merud before Ollama. merud answers, reports
// Ollama down, and turns a question away; when the fake Ollama starts on
// the configured port, merud warms the models and answers.
func TestOllamaStartsLate(t *testing.T) {
	t.Parallel()
	addr := freeAddr(t)
	h := newHome(t)
	h.writeConfig(t, fakeConfig("http://"+addr, "[router]\ntemperature = 1.0\n"))
	m := startMerud(t, h, nil)

	out := waitMCPStatus(t, h, "Ollama isn't running at http://"+addr+".")
	if !strings.Contains(out, "ollama") || !strings.Contains(out, "failed") {
		t.Errorf("meru mcp status:\n%s", out)
	}
	res := runMeru(t, h, "is anyone there?")
	if res.code != 1 || !strings.Contains(res.stderr, "Ollama isn't running, so Meru can't answer yet.") {
		t.Errorf("a question with no Ollama: exit %d, stderr %q", res.code, res.stderr)
	}
	if m.exited() {
		t.Fatalf("merud exited while Ollama was down\nstderr:\n%s", m.stderr.String())
	}

	f := startFake(t, "-addr", addr)
	waitMCPStatus(t, h, "is running at http://"+addr+".")
	waitReady(t, h, m, readyTimeout)
	f.enqueue(t, fastModel, directRoute())
	f.enqueue(t, mainModel, fakeollama.Reply{Text: "Here now."})
	res = runMeru(t, h, "is anyone there?")
	if res.code != 0 || !strings.Contains(res.stdout, "Here now.") {
		t.Errorf("a question once Ollama runs: exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
}

// TestOllamaTooOld starts merud against an Ollama older than Meru needs.
// merud stays up and says which version it found and which it needs.
func TestOllamaTooOld(t *testing.T) {
	t.Parallel()
	f := startFake(t, "-version", "0.12.0")
	h := newHome(t)
	h.writeConfig(t, fakeConfig(f.url, ""))
	m := startMerud(t, h, nil)

	waitMCPStatus(t, h, "Ollama 0.12.0 is too old; Meru needs 0.12.11 or later.")
	res := runMeru(t, h, "is anyone there?")
	if res.code != 1 || !strings.Contains(res.stderr, "Meru can't answer yet: Ollama 0.12.0 is too old") {
		t.Errorf("a question with an old Ollama: exit %d, stderr %q", res.code, res.stderr)
	}
	if m.exited() {
		t.Fatalf("merud exited on an old Ollama\nstderr:\n%s", m.stderr.String())
	}
}
