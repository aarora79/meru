// This file is the one place in internal/connectors that starts another
// program. Every install step (npm, uv, docker) builds a Cmd and hands it
// to a Runner. The real Runner, ExecRunner, runs the program by its
// absolute path, with no shell and only the environment the Cmd lists.
// internal/policy fails the build if another file in this package imports
// os/exec.

package connectors

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cmd is one program to run: its absolute path, its arguments, its whole
// environment as "KEY=value" strings, and the folder to start in.
type Cmd struct {
	Path string
	Args []string
	// Env is the program's entire environment. Nothing from merud's own
	// environment goes along unless it is listed here, because merud's
	// environment may hold another tool's API key.
	Env []string
	// Dir is the folder the program starts in. Empty means merud's own.
	Dir string
}

// Runner runs one Cmd and waits for it to finish. line, when not nil,
// gets each line the program prints, stdout and stderr together, as it
// comes, so a client can show progress. It returns the last lines of the
// output, for an error message, and fails when the program can't start or
// exits with an error.
//
// Runner is a function type: any function with this signature is one.
// Tests pass a fake that records each Cmd; ExecRunner is the real one.
type Runner func(ctx context.Context, c Cmd, line func(string)) (string, error)

// tailLines is how many lines of output a Runner keeps for its result.
const tailLines = 40

// ErrNotAbsolute means a Cmd named its program by a bare name or a
// relative path. Meru never asks PATH where a program lives: launchd
// hands merud a short PATH, and a file dropped early on PATH could stand
// in for the real program.
var ErrNotAbsolute = errors.New("the program's path isn't absolute")

// ExecRunner returns the Runner that starts programs for real.
func ExecRunner() Runner {
	return func(ctx context.Context, c Cmd, line func(string)) (string, error) {
		if !filepath.IsAbs(c.Path) {
			return "", fmt.Errorf("%s: %w", c.Path, ErrNotAbsolute)
		}
		// exec.CommandContext runs c.Path itself, with each argument as
		// its own string; no shell reads them, so no argument can add a
		// second command. CommandContext kills the program when ctx ends.
		cmd := exec.CommandContext(ctx, c.Path, c.Args...) // #nosec G204 -- the path is absolute and comes from a pinned runtime or a fixed list
		// A nil Env would hand the child all of merud's environment, so
		// an empty list stays an empty list.
		cmd.Env = append([]string{}, c.Env...)
		cmd.Dir = c.Dir
		// WaitDelay bounds how long Wait waits for the output pipes after
		// the program is killed, in case a grandchild keeps them open.
		cmd.WaitDelay = 5 * time.Second
		out, err := runLines(cmd, line)
		if err != nil {
			return out, fmt.Errorf("%s %s: %w", filepath.Base(c.Path), strings.Join(c.Args, " "), err)
		}
		return out, nil
	}
}

// runLines starts cmd, hands each line of its output to line, and waits.
// It returns the last tailLines lines.
func runLines(cmd *exec.Cmd, line func(string)) (string, error) {
	// io.Pipe joins a writer to a reader: what the program writes to
	// stdout and stderr comes out of r, in order.
	r, w := io.Pipe()
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		return "", err
	}

	// The goroutine, a function running at the same time as this one,
	// reads lines while the program runs. The WaitGroup lets this
	// function wait for it before it returns, so the goroutine never
	// outlives the call.
	var (
		wg   sync.WaitGroup
		tail []string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			text := strings.TrimRight(sc.Text(), "\r")
			if line != nil {
				line(text)
			}
			tail = append(tail, text)
			if len(tail) > tailLines {
				tail = tail[1:]
			}
		}
		// Drain the rest if the scanner stopped on a very long line, so
		// the program never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, r)
	}()
	err := cmd.Wait()
	// Closing the writer ends the reader's loop.
	_ = w.Close()
	wg.Wait()
	return strings.Join(tail, "\n"), err
}
