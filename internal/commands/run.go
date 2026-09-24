// This file runs a rendered argv: Run starts the program with no shell, a
// short environment and a timeout, and Result holds what it wrote. The
// process-group handling differs by OS and lives in group_unix.go and
// group_other.go.

package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// maxOutput caps each of stdout and stderr, in bytes. The model reads at
// most 16,000 characters of a result anyway (dispatch cuts it); the cap
// keeps a program that prints without end from filling merud's memory.
const maxOutput = 1 << 20

// waitDelay is how long Run waits, after the program exits or is killed,
// for its output pipes to close. A child the program left behind can hold
// them open; after this Run stops reading and returns what it has.
const waitDelay = 2 * time.Second

// baseEnv lists the variables every program gets from merud's environment,
// when merud has them. The rest stays out: merud's environment may hold an
// API key meant for another tool. otherEnv, in group_*.go, adds the few a
// platform needs.
var baseEnv = []string{"PATH", "HOME", "LANG"}

// Result is what one run of a command produced.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Truncated is true when stdout or stderr passed the 1 MiB cap and
	// Run kept only the first 1 MiB of it.
	Truncated bool
	Duration  time.Duration
}

// Run executes a rendered argv and returns what the program wrote. A
// non-zero exit code comes back in Result, not as an error: the model
// should see a failed command and decide what to do. err covers only
// failures to run at all: the program is missing, the timeout fired
// (err wraps context.DeadlineExceeded), or ctx ended.
//
// The program starts in c.Cwd with the environment childEnv builds, no
// standard input, and its own process group, so the timeout can kill the
// program and every process it started.
func Run(ctx context.Context, c Command, argv []string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("run: empty argv")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	// defer runs cancel when Run returns, which frees the timer.
	defer cancel()

	var stdout, stderr capped
	// exec.CommandContext starts argv[0] directly, never through a shell,
	// so no character in an argument has any special meaning.
	// #nosec G204 -- the program and its arguments come from config.toml; Render checked each parameter
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.Cwd
	cmd.Env = childEnv(c.EnvAllow)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.wasCut() || stderr.wasCut(),
		Duration:  time.Since(start),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	// errors.As finds an *exec.ExitError in err's chain and sets exitErr.
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		// The timeout fired or the turn ended. Either way the program was
		// killed, and its exit code says only that.
		return res, fmt.Errorf("%s: %w", c.ToolName(), ctx.Err())
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
		// ErrWaitDelay: the program exited, but a child it left behind
		// kept the output open. What arrived before waitDelay is kept.
		return res, nil
	case errors.As(err, &exitErr):
		return res, nil
	default:
		return Result{}, fmt.Errorf("run %s: %w", argv[0], err)
	}
}

// Text formats r for the model: the exit code, then stdout, then stderr
// when it isn't empty, each under a label, and a note when Run cut the
// output.
func (r Result) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %d\n", r.ExitCode)
	if r.Stdout == "" {
		b.WriteString("stdout: (empty)\n")
	} else {
		b.WriteString("stdout:\n" + strings.TrimRight(r.Stdout, "\n") + "\n")
	}
	if r.Stderr != "" {
		b.WriteString("stderr:\n" + strings.TrimRight(r.Stderr, "\n") + "\n")
	}
	if r.Truncated {
		b.WriteString("[The program wrote more than 1 MiB; Meru kept the first 1 MiB of each stream.]\n")
	}
	return b.String()
}

// childEnv returns the program's environment as "KEY=value" strings: the
// base variables, the platform's own, and the names in allow, each only
// when merud's environment has it.
func childEnv(allow []string) []string {
	var env []string
	seen := map[string]bool{}
	for _, lists := range [][]string{baseEnv, otherEnv, allow} {
		for _, k := range lists {
			if seen[k] {
				continue
			}
			seen[k] = true
			// LookupEnv tells "unset" apart from "set to empty".
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	// A nil Env would give the child merud's whole environment, so an
	// empty one must stay an empty, non-nil list.
	if env == nil {
		env = []string{}
	}
	return env
}

// capped is an io.Writer that keeps the first maxOutput bytes written to
// it and drops the rest. os/exec writes to it from its own goroutine, so a
// mutex guards the fields.
type capped struct {
	mu  sync.Mutex
	buf []byte
	cut bool // true once a write went past the cap
}

// Write keeps what fits under the cap and always reports all of p as
// written: an error would make os/exec stop reading, and a program whose
// pipe fills up stops too, until the timeout kills it.
func (w *capped) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	room := maxOutput - len(w.buf)
	if len(p) > room {
		w.cut = true
		p = p[:max(room, 0)]
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

// String returns what the writer kept.
func (w *capped) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// wasCut reports whether a write went past the cap.
func (w *capped) wasCut() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cut
}
