//go:build e2e

// This file holds the harness every test shares: starting and stopping
// processes, a private Meru home per test, the fake Ollama and its control
// endpoints, merud with a readiness check, and readers for the socket and
// the session transcripts.

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/skills"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
	"github.com/aarora79/meru/internal/transcript"
)

// Model names the fake-backed tests put in config.toml. Fast and main differ
// on purpose: the fake keeps one reply queue per model, so a test can script
// the router's reply (fast) and the answer (main) separately, and the order
// in which merud makes the two calls can't mix them up.
const (
	fastModel  = "fake-fast"
	mainModel  = "fake-main"
	embedModel = "fake-embed"
)

// Timeouts. They are generous because the race detector slows the binaries
// down several times over; a healthy run finishes far inside them.
const (
	readyTimeout = 30 * time.Second // merud from start to a successful ping
	exitTimeout  = 15 * time.Second // a process from a signal to its exit
	callTimeout  = 30 * time.Second // one question from send to done
	pollEvery    = 10 * time.Millisecond
)

// syncBuffer is a bytes.Buffer that several goroutines may use at once. The
// os/exec package copies a child's output into it from its own goroutine
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex // guards buf
	buf bytes.Buffer
}

// Write appends p. It makes *syncBuffer an io.Writer, so it can be a
// command's Stdout or Stderr.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// proc is one running child process with its output.
type proc struct {
	name   string
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	// done is closed when the process has exited. A closed channel never
	// blocks a receive, so any number of goroutines can wait on it.
	done chan struct{}
}

// startProc starts a built binary with args and extra environment variables
// (in "KEY=value" form, added to the test's own environment). It registers a
// cleanup that stops the process if it is still running, waits for it, and
// fails the test if a race-enabled binary reported a data race.
func startProc(t *testing.T, name string, env []string, args ...string) *proc {
	t.Helper()
	return startProcInput(t, name, env, "", args...)
}

// startProcInput is startProc with input as the process's standard input,
// for a command that asks questions. An empty input gives it none.
func startProcInput(t *testing.T, name string, env []string, input string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin(name), args...)
	cmd.Env = append(os.Environ(), env...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	p := &proc{name: name, cmd: cmd, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout = p.stdout
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	// A goroutine (a function running alongside this one) waits for the
	// process, so a test can wait on p.done with a deadline instead of
	// blocking forever in cmd.Wait.
	go func() {
		_ = cmd.Wait() // the exit status is read later from cmd.ProcessState
		close(p.done)
	}()
	// t.Cleanup runs after the test ends, last registered first, so a merud
	// stops before the fake it talks to.
	t.Cleanup(func() {
		p.stop(t)
		checkNoRace(t, p)
	})
	return p
}

// exited reports whether the process has ended, without waiting.
func (p *proc) exited() bool {
	// select with a default case never blocks: it takes the default when
	// the channel isn't ready.
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// wait waits up to timeout for the process to exit and returns its exit
// status. It fails the test if the process is still running at the deadline.
func (p *proc) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(timeout):
		t.Fatalf("%s still running after %v\nstderr:\n%s", p.name, timeout, p.stderr.String())
		return -1
	}
}

// signal sends sig to the process. It fails the test if the send fails.
func (p *proc) signal(t *testing.T, sig os.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %s: %v", p.name, err)
	}
}

// stop ends the process if it is still running: SIGTERM first, so it can
// shut down cleanly, then SIGKILL if it hasn't exited within exitTimeout. It
// always waits for the exit, so no process outlives its test.
func (p *proc) stop(t *testing.T) {
	if p.exited() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM) // it may exit on its own meanwhile
	select {
	case <-p.done:
		return
	case <-time.After(exitTimeout):
	}
	t.Errorf("%s ignored SIGTERM for %v; killing it", p.name, exitTimeout)
	_ = p.cmd.Process.Kill()
	<-p.done
}

// checkNoRace fails the test when a race-enabled binary reported a data race.
// The race detector prints its report on stderr and makes the program exit
// with status 66.
func checkNoRace(t *testing.T, p *proc) {
	if strings.Contains(p.stderr.String(), "WARNING: DATA RACE") {
		t.Errorf("%s reported a data race:\n%s", p.name, p.stderr.String())
	}
}

// home is one private Meru home: the directory that holds config.toml,
// merud.sock, merud.log and sessions/.
type home struct {
	dir    string
	config string
	socket string
}

// newHome makes an empty Meru home and removes it when the test ends.
//
// It uses os.MkdirTemp rather than t.TempDir: t.TempDir's path includes the
// test's name, and macOS caps a Unix socket path at 104 bytes.
//
// It also makes an empty skills/<name> folder for each built-in skill, so
// merud installs none. With skills, each turn's skill pick calls the fast
// model while the router does, and the two race for the replies and
// failures a test queues for the router.
func newHome(t *testing.T) *home {
	t.Helper()
	dir, err := os.MkdirTemp("", "meru-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range skills.Builtins() {
		if err := os.MkdirAll(filepath.Join(dir, "skills", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &home{
		dir:    dir,
		config: filepath.Join(dir, "config.toml"),
		socket: filepath.Join(dir, "merud.sock"),
	}
}

// writeConfig writes body to the home's config.toml with mode 0600.
func (h *home) writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(h.config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// log returns merud's log file, or a note when it can't be read.
func (h *home) log() string {
	b, err := os.ReadFile(filepath.Join(h.dir, "merud.log"))
	if err != nil {
		return fmt.Sprintf("(no merud.log: %v)", err)
	}
	return string(b)
}

// fakeConfig returns a config.toml that points merud at the fake Ollama at
// baseURL, with the test model names. extra is appended as-is, for tests
// that need more keys.
//
// Web search is off, so no test reaches a SearXNG that happens to run on
// this machine; TestWebSearchMissingSearXNG turns it on.
func fakeConfig(baseURL, extra string) string {
	return fmt.Sprintf("[ollama]\nbase_url = %q\n\n[models]\nfast = %q\nmain = %q\nembed = %q\n\n[web]\nsearxng_url = \"\"\n\n%s",
		baseURL, fastModel, mainModel, embedModel, extra)
}

// fake is a running cmd/fakeollama process and its address.
type fake struct {
	proc *proc
	url  string // such as http://127.0.0.1:53412
}

// startFake starts cmd/fakeollama on a free loopback port and waits for its
// "listening on" line. args go to fakeollama, such as "-version", "0.12.0".
func startFake(t *testing.T, args ...string) *fake {
	t.Helper()
	p := startProc(t, "fakeollama", nil, append([]string{"-addr", "127.0.0.1:0"}, args...)...)
	const prefix = "listening on "
	deadline := time.Now().Add(readyTimeout)
	for {
		line, _, found := strings.Cut(p.stdout.String(), "\n")
		if found && strings.HasPrefix(line, prefix) {
			return &fake{proc: p, url: strings.TrimPrefix(line, prefix)}
		}
		if p.exited() || time.Now().After(deadline) {
			t.Fatalf("fakeollama didn't start\nstdout:\n%s\nstderr:\n%s", p.stdout.String(), p.stderr.String())
		}
		time.Sleep(pollEvery)
	}
}

// control sends one request to the fake's /_fake/ control endpoints and
// decodes a JSON reply into out when out isn't nil.
func (f *fake) control(t *testing.T, method, path string, in, out any) {
	t.Helper()
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, f.url+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

// enqueue scripts the next replies the fake gives for model.
func (f *fake) enqueue(t *testing.T, model string, replies ...fakeollama.Reply) {
	t.Helper()
	f.control(t, http.MethodPost, "/_fake/enqueue", map[string]any{"model": model, "replies": replies}, nil)
}

// failNext makes the fake's next request to path fail with status and msg.
func (f *fake) failNext(t *testing.T, path string, status int, msg string) {
	t.Helper()
	f.control(t, http.MethodPost, "/_fake/fail", map[string]any{"path": path, "status": status, "error": msg}, nil)
}

// requests returns every request the fake has received, oldest first.
func (f *fake) requests(t *testing.T) []fakeollama.Request {
	t.Helper()
	var out []fakeollama.Request
	f.control(t, http.MethodGet, "/_fake/requests", nil, &out)
	return out
}

// chatRequests returns the /api/chat requests the fake received for model.
func (f *fake) chatRequests(t *testing.T, model string) []fakeollama.Request {
	t.Helper()
	var out []fakeollama.Request
	for _, r := range f.requests(t) {
		if r.Path == "/api/chat" && r.Model == model {
			out = append(out, r)
		}
	}
	return out
}

// letter is one route letter and the probability the fake reports for it.
type letter struct {
	token string
	prob  float64
}

// routeReply scripts the router's one-token reply: the first letter is the
// token the model "picks", and every letter shows up among the alternatives
// with its probability. Ollama reports natural logarithms, hence math.Log.
func routeReply(letters ...letter) fakeollama.Reply {
	lp := fakeollama.LogProb{Token: letters[0].token, LogProb: math.Log(letters[0].prob)}
	for _, l := range letters {
		lp.Top = append(lp.Top, fakeollama.TokenLogProb{Token: l.token, LogProb: math.Log(l.prob)})
	}
	return fakeollama.Reply{LogProbs: []fakeollama.LogProb{lp}}
}

// directRoute is a router reply that picks "direct" with confidence 0.9.
func directRoute() fakeollama.Reply {
	return routeReply(letter{"A", 0.9}, letter{"B", 0.05}, letter{"C", 0.03}, letter{"D", 0.02})
}

// stack is a fake Ollama with a merud in front of it, each in its own home.
type stack struct {
	home  *home
	fake  *fake
	merud *proc
}

// startStack starts a fake Ollama and a merud that uses it, and waits until
// `meru ping` succeeds.
func startStack(t *testing.T) *stack {
	t.Helper()
	f := startFake(t)
	h := newHome(t)
	// Temperature 1.0 leaves the router's probabilities as the fake scripts
	// them, so a test can expect the confidence it put in.
	h.writeConfig(t, fakeConfig(f.url, "[router]\ntemperature = 1.0\n"))
	m := startMerud(t, h, nil)
	waitReady(t, h, m, readyTimeout)
	return &stack{home: h, fake: f, merud: m}
}

// startMerud starts merud on h's config and socket without waiting for it.
func startMerud(t *testing.T, h *home, env []string) *proc {
	t.Helper()
	return startProc(t, "merud", env, "-config", h.config, "-socket", h.socket)
}

// waitReady runs `meru ping` until it succeeds. It fails the test, printing
// merud's stderr and log, when merud exits first or timeout passes.
func waitReady(t *testing.T, h *home, merud *proc, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if merud.exited() {
			t.Fatalf("merud exited with status %d before answering a ping\nstderr:\n%s\nlog:\n%s",
				merud.cmd.ProcessState.ExitCode(), merud.stderr.String(), h.log())
		}
		if res := runMeru(t, h, "ping"); res.code == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("merud didn't answer a ping within %v\nstderr:\n%s\nlog:\n%s", timeout, merud.stderr.String(), h.log())
		}
		time.Sleep(pollEvery)
	}
}

// result is what a finished meru run produced.
type result struct {
	stdout, stderr string
	code           int
}

// runMeru runs meru against h's socket with args and waits for it to exit.
func runMeru(t *testing.T, h *home, args ...string) result {
	t.Helper()
	p := startMeru(t, h, args...)
	code := p.wait(t, callTimeout)
	return result{stdout: p.stdout.String(), stderr: p.stderr.String(), code: code}
}

// runMeruInput is runMeru with input typed on meru's standard input.
func runMeruInput(t *testing.T, h *home, input string, args ...string) result {
	t.Helper()
	p := startProcInput(t, "meru", nil, input, append([]string{"-socket", h.socket}, args...)...)
	code := p.wait(t, callTimeout)
	return result{stdout: p.stdout.String(), stderr: p.stderr.String(), code: code}
}

// startMeru starts meru against h's socket with args, without waiting.
func startMeru(t *testing.T, h *home, args ...string) *proc {
	t.Helper()
	return startProc(t, "meru", nil, append([]string{"-socket", h.socket}, args...)...)
}

// ask sends one question to merud through internal/rpc, the library meru
// itself uses, and returns every event of the reply. session continues a
// session when not empty. It fails the test when the connection fails.
func ask(t *testing.T, socket, session, question string) []rpc.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	req := rpc.Request{Op: rpc.OpAsk, Session: session, Text: question, Source: rpc.SourceCLI}
	var events []rpc.Event
	// rpc.Do returns an iterator; range calls the loop body once per event.
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			t.Fatalf("ask %q: %v", question, err)
		}
		events = append(events, ev)
	}
	return events
}

// eventsOf returns the events of type typ, in order.
func eventsOf(events []rpc.Event, typ rpc.EventType) []rpc.Event {
	var out []rpc.Event
	for _, ev := range events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// answerOf joins the text of every token event.
func answerOf(events []rpc.Event) string {
	var b strings.Builder
	for _, ev := range eventsOf(events, rpc.EventToken) {
		b.WriteString(ev.Text)
	}
	return b.String()
}

// lastEvent returns the final event, which is "done" or "error".
func lastEvent(t *testing.T, events []rpc.Event) rpc.Event {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("reply had no events")
	}
	return events[len(events)-1]
}

// sessionFiles returns the path of every transcript under h's sessions/.
// Transcripts live at sessions/YYYY/MM/<id>.jsonl.
func sessionFiles(t *testing.T, h *home) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(h.dir, "sessions", "*", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// sessionFile returns the transcript path for session id.
func sessionFile(t *testing.T, h *home, id string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(h.dir, "sessions", "*", "*", id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("want one transcript for session %s, found %v", id, paths)
	}
	return paths[0]
}

// readTranscript parses every line of a transcript file. Unlike merud, it
// fails on a line that isn't valid JSON, because merud should never write
// one.
func readTranscript(t *testing.T, path string) []transcript.Line {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a path the test built itself
	if err != nil {
		t.Fatal(err)
	}
	var lines []transcript.Line
	for raw := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		if raw == "" {
			continue
		}
		var l transcript.Line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("%s: bad line %q: %v", path, raw, err)
		}
		lines = append(lines, l)
	}
	return lines
}

// waitFor polls cond until it returns true, and fails the test with what
// when timeout passes first.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(pollEvery)
	}
}

// errNotExist reports whether path is missing.
func errNotExist(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}
