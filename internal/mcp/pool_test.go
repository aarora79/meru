// This file tests the Pool against the test server over all three
// transports: listing and naming tools, the allow and confirm lists, call
// results, cancellation, timeouts, and servers that fail or die.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// transportCase is one way to reach the test server.
type transportCase struct {
	name string
	// open returns a started Pool with one server named "t" and the
	// counter of tools/call requests (memory transport only; nil elsewhere).
	open func(t *testing.T, edit func(*ServerConfig)) (*Pool, *atomic.Int32)
}

// transports lists the three transports every behaviour test runs over.
func transports() []transportCase {
	return []transportCase{
		{"memory", func(t *testing.T, edit func(*ServerConfig)) (*Pool, *atomic.Int32) {
			// The memory transport ignores Command, but Validate needs one.
			cfg := ServerConfig{Name: "t", Command: "unused", Allow: allTestTools()}
			var calls atomic.Int32
			return openPool(t, cfg, edit, memoryDial(t, &calls)), &calls
		}},
		{"stdio", func(t *testing.T, edit func(*ServerConfig)) (*Pool, *atomic.Int32) {
			return openPool(t, stdioConfig(t, "t"), edit, dialTransport), nil
		}},
		{"http", func(t *testing.T, edit func(*ServerConfig)) (*Pool, *atomic.Int32) {
			return openPool(t, httpConfig(t, "t"), edit, dialTransport), nil
		}},
	}
}

// openPool applies edit to cfg, starts a Pool with it, and closes the Pool
// when the test ends.
func openPool(t *testing.T, cfg ServerConfig, edit func(*ServerConfig), dial dialFunc) *Pool {
	t.Helper()
	if edit != nil {
		edit(&cfg)
	}
	p, err := newPool(context.Background(), []ServerConfig{cfg}, nil, dial)
	if err != nil {
		t.Fatalf("newPool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// toolNames returns the names of the tools p offers the model.
func toolNames(p *Pool) []string {
	var names []string
	for _, spec := range p.Tools() {
		names = append(names, spec.Name)
	}
	return names
}

func TestToolsAreNamespacedAndFiltered(t *testing.T) {
	for _, tc := range transports() {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := tc.open(t, func(c *ServerConfig) { c.Allow = []string{"echo", "add", "missing"} })

			if got, want := toolNames(p), []string{"t.add", "t.echo"}; !slices.Equal(got, want) {
				t.Errorf("Tools() names = %v, want %v", got, want)
			}
			for _, spec := range p.Tools() {
				if spec.Description == "" {
					t.Errorf("%s: empty description", spec.Name)
				}
				var schema map[string]any
				if err := json.Unmarshal(spec.Parameters, &schema); err != nil || schema["type"] != "object" {
					t.Errorf("%s: schema %s is not an object schema (err %v)", spec.Name, spec.Parameters, err)
				}
			}

			st := p.Status()
			if len(st) != 1 {
				t.Fatalf("Status() has %d entries, want 1", len(st))
			}
			s := st[0]
			if !s.Connected || s.Allowed != 2 || s.Offered != 8 || s.LastError != "" {
				t.Errorf("Status() = %+v, want connected, 2 allowed of 8 offered, no error", s)
			}
			if !slices.Equal(s.Unknown, []string{"missing"}) {
				t.Errorf("Status().Unknown = %v, want [missing]", s.Unknown)
			}
		})
	}
}

func TestDenyByDefault(t *testing.T) {
	var calls atomic.Int32
	cfg := ServerConfig{Name: "t", Command: "unused"} // no allow list at all
	p := openPool(t, cfg, nil, memoryDial(t, &calls))
	if got := p.Tools(); len(got) != 0 {
		t.Errorf("Tools() = %v, want none", got)
	}
	if _, err := p.Call(context.Background(), "t.echo", nil); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("Call(t.echo) error = %v, want ErrNotAllowed", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("server received %d tools/call requests, want 0", n)
	}
}

func TestCallRefusesToolsOutsideTheAllowList(t *testing.T) {
	var calls atomic.Int32
	cfg := ServerConfig{Name: "t", Command: "unused", Allow: allTestTools()}
	p := openPool(t, cfg, nil, memoryDial(t, &calls))

	for _, name := range []string{"t.secret", "other.echo", "echo", "", "t.", ".echo", "t.echo.x"} {
		t.Run(name, func(t *testing.T) {
			_, err := p.Call(context.Background(), name, json.RawMessage(`{}`))
			if !errors.Is(err, ErrNotAllowed) {
				t.Errorf("Call(%q) error = %v, want ErrNotAllowed", name, err)
			}
		})
	}
	if slices.Contains(toolNames(p), "t.secret") {
		t.Error("Tools() lists t.secret")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("server received %d tools/call requests, want 0", n)
	}
}

func TestNeedsConfirm(t *testing.T) {
	cfg := ServerConfig{Name: "t", Command: "unused", Allow: []string{"echo", "add"}, Confirm: []string{"add"}}
	var calls atomic.Int32
	p := openPool(t, cfg, nil, memoryDial(t, &calls))
	tests := []struct {
		name string
		want bool
	}{
		{"t.add", true},
		{"t.echo", false},
		{"t.secret", false},
		{"other.add", false},
		{"add", false},
	}
	for _, tt := range tests {
		if got := p.NeedsConfirm(tt.name); got != tt.want {
			t.Errorf("NeedsConfirm(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCallResults(t *testing.T) {
	tests := []struct {
		name           string
		tool           string
		args           string
		wantText       string
		wantStructured string // "" means none
		wantIsError    bool
	}{
		{"echo", "t.echo", `{"text":"hello"}`, "hello", "", false},
		{"structured", "t.add", `{"A":2,"B":3}`, "5", `{"sum":5}`, false},
		{"tool error", "t.fail", `{}`, "the tool broke", "", true},
		{"no arguments", "t.fail", ``, "the tool broke", "", true},
	}
	for _, tc := range transports() {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := tc.open(t, nil)
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					res, err := p.Call(context.Background(), tt.tool, json.RawMessage(tt.args))
					if err != nil {
						t.Fatalf("Call: %v", err)
					}
					if res.Text != tt.wantText || res.IsError != tt.wantIsError {
						t.Errorf("Call = %+v, want text %q, isError %v", res, tt.wantText, tt.wantIsError)
					}
					if string(res.Structured) != tt.wantStructured {
						t.Errorf("Structured = %s, want %s", res.Structured, tt.wantStructured)
					}
					if res.Duration <= 0 {
						t.Errorf("Duration = %v, want > 0", res.Duration)
					}
				})
			}
		})
	}
}

func TestCallRejectsBadArguments(t *testing.T) {
	var calls atomic.Int32
	p := openPool(t, ServerConfig{Name: "t", Command: "unused", Allow: allTestTools()}, nil, memoryDial(t, &calls))
	for _, args := range []string{`not json`, `"a string"`, `[1,2]`, `{"text":`} {
		_, err := p.Call(context.Background(), "t.echo", json.RawMessage(args))
		if err == nil || errors.Is(err, ErrNotAllowed) {
			t.Errorf("Call(args %s) error = %v, want an argument error", args, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("server received %d tools/call requests, want 0", n)
	}
}

func TestCallCancelledMidCall(t *testing.T) {
	for _, tc := range transports() {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := tc.open(t, nil)
			ctx, cancel := context.WithCancel(context.Background())
			// time.AfterFunc runs cancel on its own goroutine after 100 ms,
			// while Call is waiting on the server.
			timer := time.AfterFunc(100*time.Millisecond, cancel)
			defer timer.Stop()

			start := time.Now()
			_, err := p.Call(ctx, "t.slow", json.RawMessage(`{"millis":20000}`))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Call error = %v, want context.Canceled", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Call took %v after cancel, want it to stop at once", d)
			}
			// The server survives a cancelled call.
			res, err := p.Call(context.Background(), "t.echo", json.RawMessage(`{"text":"still here"}`))
			if err != nil || res.Text != "still here" {
				t.Errorf("Call after cancel = %+v, %v; want the echo", res, err)
			}
		})
	}
}

func TestCallTimeout(t *testing.T) {
	for _, tc := range transports() {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := tc.open(t, func(c *ServerConfig) { c.Timeout = 200 * time.Millisecond })
			start := time.Now()
			_, err := p.Call(context.Background(), "t.slow", json.RawMessage(`{"millis":20000}`))
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Call error = %v, want context.DeadlineExceeded", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Call took %v, want about 200ms", d)
			}
			if _, err := p.Call(context.Background(), "t.slow", json.RawMessage(`{"millis":1}`)); err != nil {
				t.Errorf("fast call after timeout: %v", err)
			}
		})
	}
}

func TestConcurrentCalls(t *testing.T) {
	for _, tc := range transports() {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := tc.open(t, nil)
			var wg sync.WaitGroup
			errs := make(chan error, 20)
			for i := range 20 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					want := strconv.Itoa(i)
					res, err := p.Call(context.Background(), "t.echo", json.RawMessage(`{"text":"`+want+`"}`))
					if err == nil && res.Text != want {
						err = errors.New("got " + res.Text + ", want " + want)
					}
					errs <- err
					_ = p.Status()
					_ = p.Tools()
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestServerThatFailsToStart(t *testing.T) {
	missing := stdioConfig(t, "missing")
	missing.Command = filepath.Join(t.TempDir(), "no-such-server")

	// exits runs the test binary without the server switch, so it runs no
	// tests and exits before the handshake.
	exits := stdioConfig(t, "exits")
	exits.Env = nil

	// refused points at a loopback port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedURL := "http://" + ln.Addr().String() + "/mcp"
	_ = ln.Close()
	refused := ServerConfig{Name: "refused", URL: refusedURL, Allow: []string{"echo"}}

	good := stdioConfig(t, "good")

	p, err := NewPool(context.Background(), []ServerConfig{missing, exits, refused, good}, nil)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(p.Close)

	for _, s := range p.Status() {
		wantUp := s.Name == "good"
		if s.Connected != wantUp {
			t.Errorf("%s: Connected = %v, want %v (last error %q)", s.Name, s.Connected, wantUp, s.LastError)
		}
		if !wantUp && s.LastError == "" {
			t.Errorf("%s: LastError is empty", s.Name)
		}
	}
	for _, name := range toolNames(p) {
		if !strings.HasPrefix(name, "good.") {
			t.Errorf("Tools() lists %s from a server that never started", name)
		}
	}

	// A call to a dead server fails fast with ErrUnavailable, inside the
	// reconnect wait.
	for _, name := range []string{"missing.echo", "exits.echo", "refused.echo"} {
		if _, err := p.Call(context.Background(), name, nil); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Call(%s) error = %v, want ErrUnavailable", name, err)
		}
	}
	if res, err := p.Call(context.Background(), "good.echo", json.RawMessage(`{"text":"ok"}`)); err != nil || res.Text != "ok" {
		t.Errorf("Call(good.echo) = %+v, %v", res, err)
	}
}

func TestLoopbackServerCannotRedirectOffMachine(t *testing.T) {
	// The handler answers every request with a redirect to a host that
	// isn't loopback. The client must refuse to follow it.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://mcp.example.com/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(ts.Close)

	p, err := NewPool(context.Background(), []ServerConfig{{Name: "r", URL: ts.URL, Allow: []string{"echo"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	st := p.Status()[0]
	if st.Connected || !strings.Contains(st.LastError, "redirect refused") {
		t.Errorf("Status = %+v, want not connected with a refused redirect", st)
	}
}

func TestNewPoolRefusesBadConfig(t *testing.T) {
	_, err := NewPool(context.Background(), []ServerConfig{{Name: "x", Command: "a", URL: "http://127.0.0.1:1/mcp"}}, nil)
	if err == nil {
		t.Fatal("NewPool accepted an entry with both command and url")
	}
}

func TestCrashedServerRestarts(t *testing.T) {
	p := openPool(t, stdioConfig(t, "t"), nil, dialTransport)
	first := callText(t, p, "t.pid")

	if _, err := p.Call(context.Background(), "t.crash", nil); err == nil {
		t.Fatal("Call(t.crash) succeeded, want an error")
	}
	waitFor(t, "server to show as disconnected", func() bool { return !p.Status()[0].Connected })

	// Inside the reconnect wait, calls fail fast.
	if _, err := p.Call(context.Background(), "t.pid", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Call inside the reconnect wait: error = %v, want ErrUnavailable", err)
	}
	// The tools stay listed, so the model can still call them.
	if !slices.Contains(toolNames(p), "t.pid") {
		t.Error("Tools() dropped the crashed server's tools")
	}

	// After the wait, the next call starts a new process.
	p.reconnectAfter = 0
	second := callText(t, p, "t.pid")
	if second == first {
		t.Errorf("pid after restart = %s, same as before", second)
	}
	if st := p.Status()[0]; !st.Connected || st.LastError != "" {
		t.Errorf("Status after restart = %+v", st)
	}
}

func TestChildGetsOnlyNamedEnv(t *testing.T) {
	// t.Setenv sets a variable in this process for the test's length.
	t.Setenv("MERU_TEST_LEAK", "should-not-leak")
	p := openPool(t, stdioConfig(t, "t"), func(c *ServerConfig) { c.Env["MERU_TEST_GIVEN"] = "given" }, dialTransport)

	tests := []struct {
		name string
		want string
	}{
		{"MERU_TEST_LEAK", ""},
		{"MERU_TEST_GIVEN", "given"},
		{testServerEnv, "1"},
	}
	for _, tt := range tests {
		res, err := p.Call(context.Background(), "t.getenv", json.RawMessage(`{"name":"`+tt.name+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != tt.want {
			t.Errorf("child sees %s=%q, want %q", tt.name, res.Text, tt.want)
		}
	}
	if res := callText(t, p, "t.getenv", `{"name":"PATH"}`); res == "" {
		t.Error("child has no PATH")
	}
}

func TestStartupContextDoesNotBoundServers(t *testing.T) {
	for _, cfg := range []ServerConfig{stdioConfig(t, "stdio"), httpConfig(t, "http")} {
		t.Run(cfg.Name, func(t *testing.T) {
			// merud passes NewPool a startup context; ending it must not end
			// the servers, which live until Close.
			ctx, cancel := context.WithCancel(context.Background())
			p, err := NewPool(ctx, []ServerConfig{cfg}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.Close)
			cancel()
			time.Sleep(50 * time.Millisecond)
			if got := callText(t, p, cfg.Name+".echo", `{"text":"alive"}`); got != "alive" {
				t.Errorf("echo after startup ctx ended = %q", got)
			}
		})
	}
}

func TestCallAfterClose(t *testing.T) {
	p := openPool(t, stdioConfig(t, "t"), nil, dialTransport)
	p.Close()
	if _, err := p.Call(context.Background(), "t.echo", nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Call after Close: error = %v, want ErrUnavailable", err)
	}
	p.Close() // a second Close is harmless
}

// callText calls name with optional JSON args and returns the result text,
// failing the test on any error.
func callText(t *testing.T, p *Pool, name string, args ...string) string {
	t.Helper()
	raw := json.RawMessage(`{}`)
	if len(args) > 0 {
		raw = json.RawMessage(args[0])
	}
	res, err := p.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("Call(%s): %v", name, err)
	}
	return res.Text
}

// waitFor polls cond every 10 ms for up to 10 s, and fails the test if it
// never holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
