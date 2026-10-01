// This file is the one place in internal/connectors that starts another
// program. Every install step (npm, uv, docker) builds a Cmd and hands it
// to a Runner. The real Runner, ExecRunner, runs the program by its
// absolute path, with no shell and only the environment the Cmd lists.
// A running connector starts here too: stdioTransport turns a Cmd into
// the child process the supervisor talks MCP to over its stdin and
// stdout, and httpTransport starts an http connector's program and waits
// for it to listen on its loopback port. StartPiped starts a program
// that is driven through two extra pipes, the way web_fetch's page reader
// drives chrome-headless-shell. internal/policy fails the build if another
// file in this package imports os/exec.

package connectors

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/loopback"
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

// childStopWait is how long stopping a stdio connector waits, after
// closing its stdin, before it signals the process; the SDK then waits as
// long again before it kills it. It matches the MCP pool's wait for a
// server added by hand.
const childStopWait = 2 * time.Second

// stdioTransport builds the MCP transport for a stdio connector: the
// program c, started when the supervisor connects, with its stdin and
// stdout carrying MCP and its stderr going to stderr. procCtx bounds the
// child's life: when it ends, the program is killed, the backstop behind
// the SDK's own close-stdin-then-signal stop. It fails when c names its
// program by a relative path.
func stdioTransport(procCtx context.Context, c Cmd, stderr io.Writer) (mcp.Transport, error) {
	if !filepath.IsAbs(c.Path) {
		return nil, fmt.Errorf("%s: %w", c.Path, ErrNotAbsolute)
	}
	// As in ExecRunner: the program runs by its absolute path, with each
	// argument as its own string and no shell.
	cmd := exec.CommandContext(procCtx, c.Path, c.Args...) // #nosec G204 -- the path is absolute and comes from a pinned install
	cmd.Env = append([]string{}, c.Env...)
	cmd.Dir = c.Dir
	cmd.Stderr = stderr
	// When Stderr isn't a file, os/exec copies it in a goroutine and Wait
	// waits for the copy. WaitDelay caps that wait, in case a grandchild
	// keeps stderr open.
	cmd.WaitDelay = childStopWait
	return &mcp.CommandTransport{Command: cmd, TerminateDuration: childStopWait}, nil
}

// ErrPortBusy means another program already listens at the address an
// http connector must use. Meru never stops that program: it may be the
// user's own server, started by hand. The supervisor reports it and
// tries again later.
var ErrPortBusy = errors.New("another program already listens there")

// The waits around an http connector's port.
const (
	// portFreeWait is how long a start waits for the port to come free
	// before it counts as busy. Meru's own program, stopped a moment ago,
	// may still hold it while it exits.
	portFreeWait = 3 * time.Second
	// portPoll is how often those waits look again.
	portPoll = 100 * time.Millisecond
)

// httpTransport starts an http connector's program c and returns the MCP
// transport to it at endpoint, a loopback URL such as
// http://127.0.0.1:8000/mcp, and a channel that closes when the program
// exits. It first makes sure nothing else listens at the endpoint's
// address, and fails with ErrPortBusy if something does; then it starts
// the program, with its output going to stderr, and waits, until ctx
// ends, for it to listen. procCtx bounds the program's life: when it
// ends, the program is killed.
//
// A Streamable HTTP session doesn't end when the server's program does,
// so the caller watches the returned channel to learn of a crash.
func httpTransport(ctx, procCtx context.Context, c Cmd, stderr io.Writer, endpoint string) (mcp.Transport, <-chan struct{}, error) {
	if !filepath.IsAbs(c.Path) {
		return nil, nil, fmt.Errorf("%s: %w", c.Path, ErrNotAbsolute)
	}
	addr, err := hostPort(endpoint)
	if err != nil {
		return nil, nil, err
	}
	if err := waitPortFree(ctx, addr, portFreeWait); err != nil {
		return nil, nil, err
	}
	// As in ExecRunner: the program runs by its absolute path, with each
	// argument as its own string and no shell.
	cmd := exec.CommandContext(procCtx, c.Path, c.Args...) // #nosec G204 -- the path is absolute and comes from a pinned install
	cmd.Env = append([]string{}, c.Env...)
	cmd.Dir = c.Dir
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	cmd.WaitDelay = childStopWait
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	// This goroutine reaps the program when it exits, and closes exited
	// to say so. It ends when the program does, which procCtx makes sure
	// of: whoever ends procCtx waits on exited.
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	if err := waitListening(ctx, addr, exited); err != nil {
		_ = cmd.Process.Kill()
		<-exited
		return nil, nil, err
	}
	return &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: loopbackClient(),
		// Meru lists the tools itself and ignores what the server sends
		// on its own, as the MCP pool does for a server added by hand.
		DisableStandaloneSSE: true,
	}, exited, nil
}

// loopbackClient returns the HTTP client for a connector's own server. It
// refuses to follow a redirect off this machine, so a connector can't
// send merud's requests elsewhere.
func loopbackClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := loopback.CheckURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect refused: %w", err)
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// hostPort returns the "host:port" a loopback URL such as
// http://127.0.0.1:8000/mcp names. Validate has made sure a manifest's
// URL has a port.
func hostPort(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Port() == "" {
		return "", fmt.Errorf("the address %q has no port", endpoint)
	}
	return u.Host, nil
}

// Listening reports whether a program accepts connections at addr, a
// "host:port", now.
func Listening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitPortFree returns nil once nothing listens at addr, and waits up to
// wait for that. It fails with ErrPortBusy when something still listens
// then, and with ctx's error when ctx ends first.
func waitPortFree(ctx context.Context, addr string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for Listening(addr) {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %w", addr, ErrPortBusy)
		}
		// select waits for whichever comes first: the end of ctx, or the
		// next look.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(portPoll):
		}
	}
	return nil
}

// waitListening returns nil once the program listens at addr. It fails
// when the program exits first (exited closes) or ctx ends.
func waitListening(ctx context.Context, addr string, exited <-chan struct{}) error {
	for !Listening(addr) {
		select {
		case <-exited:
			return fmt.Errorf("the program exited before it listened on %s", addr)
		case <-ctx.Done():
			return fmt.Errorf("the program didn't listen on %s in time: %w", addr, ctx.Err())
		case <-time.After(portPoll):
		}
	}
	return nil
}

// ErrNoPipes means this system can't hand a child the two extra pipes
// StartPiped needs. Go passes extra files to a child only on Unix systems,
// so on Windows a piped program can't start.
var ErrNoPipes = errors.New("this system can't pass extra pipes to a program, as Go does only on Unix")

// pipedStopWait is how long Stop waits, after closing the pipes, for the
// program to exit on its own before it kills it.
const pipedStopWait = 3 * time.Second

// Piped is a running program with two extra pipes: the program reads
// commands from its file descriptor 3 and writes replies to its file
// descriptor 4. That is how Chrome's --remote-debugging-pipe works. When
// merud dies, the operating system closes merud's ends of the pipes, and
// Chrome, reading end-of-file on fd 3, exits on its own, so no browser
// outlives merud.
type Piped struct {
	// In is merud's end of the program's fd 3: what merud writes here,
	// the program reads.
	In io.WriteCloser
	// Out is merud's end of the program's fd 4: what the program writes
	// there, merud reads here.
	Out io.ReadCloser

	cmd *exec.Cmd
	// done closes when the program has exited; err then holds Wait's
	// result. One goroutine, started by StartPiped, waits for the program
	// and owns both.
	done chan struct{}
	err  error
	// tail keeps the last lines the program wrote to stderr, for the
	// error a crash reports.
	tail *lineTail
}

// StartPiped starts c and returns at once, with the program running. It
// keeps every rule ExecRunner keeps: an absolute path, each argument its
// own string with no shell, and only the environment c lists. The
// program's stdout is discarded and its stderr kept, its last tailLines
// lines, for Stop's error. It fails with ErrNotAbsolute on a relative
// path, ErrNoPipes on Windows, and when the program can't start.
func StartPiped(c Cmd) (*Piped, error) {
	if !filepath.IsAbs(c.Path) {
		return nil, fmt.Errorf("%s: %w", c.Path, ErrNotAbsolute)
	}
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("%s: %w", filepath.Base(c.Path), ErrNoPipes)
	}
	// os.Pipe returns two connected files: what is written to the second
	// can be read from the first. The program gets one end of each pipe,
	// merud keeps the other.
	cmdR, cmdW, err := os.Pipe() // the program reads commands from cmdR
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(c.Path), err)
	}
	repR, repW, err := os.Pipe() // the program writes replies to repW
	if err != nil {
		_ = cmdR.Close()
		_ = cmdW.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(c.Path), err)
	}

	// As in ExecRunner: the program runs by its absolute path, with each
	// argument as its own string and no shell. exec.Command, not
	// CommandContext: the program outlives the call that starts it, and
	// Stop ends it.
	cmd := exec.Command(c.Path, c.Args...) // #nosec G204 -- the path is absolute and comes from a pinned runtime
	cmd.Env = append([]string{}, c.Env...)
	cmd.Dir = c.Dir
	// ExtraFiles become the child's file descriptors 3, 4 and so on, after
	// stdin, stdout and stderr (0, 1 and 2).
	cmd.ExtraFiles = []*os.File{cmdR, repW}
	tail := &lineTail{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{cmdR, cmdW, repR, repW} {
			_ = f.Close()
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(c.Path), err)
	}
	// The child holds its own copies of cmdR and repW now. merud closes
	// its copies, so that when the child exits, merud's read of repR ends
	// instead of waiting forever.
	_ = cmdR.Close()
	_ = repW.Close()

	p := &Piped{In: cmdW, Out: repR, cmd: cmd, done: make(chan struct{}), tail: tail}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Done returns a channel that closes when the program has exited.
func (p *Piped) Done() <-chan struct{} { return p.done }

// Stop ends the program. It closes merud's ends of the pipes, which tells
// Chrome to exit, waits up to pipedStopWait for it to, and kills it if it
// hasn't. It returns nil when the program exited cleanly, and otherwise an
// error with its exit status and the last lines of its stderr. Calling it
// twice is safe.
func (p *Piped) Stop() error {
	_ = p.In.Close()
	_ = p.Out.Close()
	select {
	case <-p.done:
	case <-time.After(pipedStopWait):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	if p.err != nil {
		return fmt.Errorf("%s: %w; it wrote: %s", filepath.Base(p.cmd.Path), p.err, p.tail.String())
	}
	return nil
}

// lineTail is an io.Writer that keeps the last tailLines lines written
// to it. The program's stderr goes here. The mutex guards lines, since
// exec copies stderr from its own goroutine while Stop may read it.
type lineTail struct {
	mu      sync.Mutex
	partial string
	lines   []string
}

// Write keeps the lines in p, joining a line split across writes.
func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := t.partial + string(p)
	parts := strings.Split(text, "\n")
	t.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		t.lines = append(t.lines, strings.TrimRight(l, "\r"))
		if len(t.lines) > tailLines {
			t.lines = t.lines[1:]
		}
	}
	return len(p), nil
}

// String returns the kept lines, joined by newlines.
func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(append(append([]string{}, t.lines...), t.partial), "\n")
}
