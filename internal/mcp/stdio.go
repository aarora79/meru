// This file builds the child process for a stdio server: its command line,
// its trimmed environment, and the writer that copies its stderr into
// merud's debug log.

package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// baseEnv returns the names of the variables a stdio server inherits from merud, when merud
// has them. The rest of merud's environment stays out: it may hold API keys
// for other tools, and a server should see only the secrets its own entry
// names.
//
//   - PATH lets "npx" or "uvx" find their interpreter; HOME and USERPROFILE
//     let them find their caches.
//   - TMPDIR, TEMP and TMP name the scratch directory.
//   - SystemRoot and PATHEXT are needed by almost every Windows program;
//     without SystemRoot, networking fails to start.
//
// The names differ by platform, but a missing one is skipped, so one list
// serves all of them without build tags. It is a function, not a package
// variable, so no code can change the list at run time.
func baseEnv() []string {
	return []string{"PATH", "HOME", "USERPROFILE", "TMPDIR", "TEMP", "TMP", "SystemRoot", "PATHEXT"}
}

// terminateWait is how long closing a stdio server waits, after closing its
// stdin, before it signals the process; the SDK then waits as long again
// before killing it. Two seconds lets a well-behaved server flush and exit,
// and keeps merud's own shutdown quick.
const terminateWait = 2 * time.Second

// stderrLineCap and stderrTotalCap bound how much of a server's stderr
// reaches the log. A chatty or broken server could otherwise fill the disk.
// The cap counts per process: a restarted server gets a fresh allowance.
const (
	stderrLineCap  = 1 << 10  // 1 KiB per line; the rest of a longer line is dropped
	stderrTotalCap = 64 << 10 // 64 KiB per process, then one "cap reached" line
)

// childEnv returns the environment for a stdio server, as "KEY=value"
// strings: the base variables merud has, then the entry's own Env, which
// wins on a clash. The result is sorted so a log line or test shows it in a
// stable order.
func childEnv(extra map[string]string) []string {
	env := make(map[string]string, len(extra)+8)
	for _, k := range baseEnv() {
		// LookupEnv tells "unset" apart from "set to empty"; Getenv doesn't.
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range extra {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// newStdioTransport builds the SDK transport that starts cfg's command.
// procCtx bounds the child's life: cancelling it kills the process, which is
// the backstop behind the SDK's own close-stdin-then-signal shutdown.
//
// exec.CommandContext runs the program directly, never through a shell, so
// nothing in Args is interpreted.
func newStdioTransport(procCtx context.Context, cfg ServerConfig, log *slog.Logger) *mcp.CommandTransport {
	// #nosec G204 -- the command comes from the user's own config.toml; running it is the point
	cmd := exec.CommandContext(procCtx, cfg.Command, cfg.Args...)
	cmd.Env = childEnv(cfg.Env)
	cmd.Stderr = &stderrLog{log: log.With("mcp_server", cfg.Name)}
	// When Stderr isn't a file, the os/exec package copies it in a goroutine,
	// and Wait waits for that copy to end. A grandchild that keeps stderr
	// open would make Wait hang; WaitDelay caps that wait.
	cmd.WaitDelay = terminateWait
	return &mcp.CommandTransport{Command: cmd, TerminateDuration: terminateWait}
}

// stderrLog is an io.Writer that logs each line a server writes to stderr at
// debug level. Servers print their startup banner and errors there, which
// helps when one won't start. Lines longer than stderrLineCap are cut, and
// after stderrTotalCap bytes the rest is dropped.
//
// The mutex guards every field below it. os/exec writes from one goroutine,
// but the lock makes the type safe to share without relying on that.
type stderrLog struct {
	log *slog.Logger

	mu      sync.Mutex
	partial []byte // the start of a line that has no newline yet
	total   int    // bytes seen so far, for the total cap
	capped  bool   // true once the "cap reached" line is logged
}

// Write logs every complete line in p and keeps the unfinished tail for the
// next call. It always reports len(p) written: returning an error would make
// os/exec stop draining the pipe, and a full pipe blocks the server.
func (w *stderrLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	// defer runs the Unlock when Write returns, on every path.
	defer w.mu.Unlock()

	n := len(p)
	if w.capped {
		return n, nil
	}
	w.total += n
	if w.total > stderrTotalCap {
		w.capped = true
		w.partial = nil
		w.log.Debug("mcp server stderr: cap reached, dropping the rest", "cap_bytes", stderrTotalCap)
		return n, nil
	}

	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.partial = appendCapped(w.partial, p)
			break
		}
		line := appendCapped(w.partial, p[:i])
		w.partial = nil
		p = p[i+1:]
		if len(bytes.TrimSpace(line)) > 0 {
			w.log.Debug("mcp server stderr", "line", string(bytes.TrimRight(line, "\r")))
		}
	}
	return n, nil
}

// appendCapped appends add to line, keeping at most stderrLineCap bytes.
func appendCapped(line, add []byte) []byte {
	room := stderrLineCap - len(line)
	if room <= 0 {
		return line
	}
	if len(add) > room {
		add = add[:room]
	}
	return append(line, add...)
}
