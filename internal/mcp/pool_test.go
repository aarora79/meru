// This file tests the Pool against the test server over all three
// transports: listing and naming tools, the allow and confirm lists, call
// results, cancellation, timeouts, and servers that fail or die.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	cfg := ServerConfig{Name: "t", Command: "unused", Allow: []string{"echo", "add", "slow"},
		Confirm: []string{"add"}, AlwaysConfirm: []string{"slow"}}
	var calls atomic.Int32
	p := openPool(t, cfg, nil, memoryDial(t, &calls))
	tests := []struct {
		name       string
		want       bool
		wantAlways bool
	}{
		{"t.add", true, false},
		{"t.slow", true, true}, // always_confirm implies confirm
		{"t.echo", false, false},
		{"t.secret", false, false},
		{"other.add", false, false},
		{"add", false, false},
	}
	for _, tt := range tests {
		if got := p.NeedsConfirm(tt.name); got != tt.want {
			t.Errorf("NeedsConfirm(%q) = %v, want %v", tt.name, got, tt.want)
		}
		if got := p.AlwaysConfirms(tt.name); got != tt.wantAlways {
			t.Errorf("AlwaysConfirms(%q) = %v, want %v", tt.name, got, tt.wantAlways)
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

	// A call to a server that isn't connected fails at once with
	// ErrUnavailable; Call never tries to connect.
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

func TestCrashedServerComesBackOnTheNextTurn(t *testing.T) {
	p := openPool(t, stdioConfig(t, "t"), nil, dialTransport)
	first := callText(t, p, "t.pid")

	if _, err := p.Call(context.Background(), "t.crash", nil); err == nil {
		t.Fatal("Call(t.crash) succeeded, want an error")
	}
	waitFor(t, "server to show as not connected", func() bool { return !p.Status()[0].Connected })

	// Nothing restarts it on its own: calls fail at once, and its tools
	// leave the list the next turn would offer.
	if _, err := p.Call(context.Background(), "t.pid", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Call after the crash: error = %v, want ErrUnavailable", err)
	}
	if names := toolNames(p); len(names) != 0 {
		t.Errorf("Tools() = %v after the crash, want none", names)
	}

	// The next turn that offers tools starts a new process.
	p.Refresh(context.Background())
	second := callText(t, p, "t.pid")
	if second == first {
		t.Errorf("pid after the turn's try = %s, same as before", second)
	}
	if st := p.Status()[0]; !st.Connected || st.LastError != "" {
		t.Errorf("Status after the turn's try = %+v", st)
	}
}

// countingDial returns a dialFunc that counts its calls and fails while
// down holds true, as a server that isn't running would. Once down is
// false it connects to an in-memory test server.
func countingDial(t *testing.T, dials *atomic.Int32, down *atomic.Bool) dialFunc {
	var calls atomic.Int32
	up := memoryDial(t, &calls)
	return func(ctx context.Context, cfg ServerConfig, log *slog.Logger) (mcp.Transport, error) {
		dials.Add(1)
		if down.Load() {
			return nil, errors.New("connection refused")
		}
		return up(ctx, cfg, log)
	}
}

func TestConnectOnDemand(t *testing.T) {
	var dials atomic.Int32
	var down atomic.Bool
	down.Store(true)
	cfg := ServerConfig{Name: "g", URL: "http://127.0.0.1:8000/mcp", Allow: []string{"echo"}}
	p := openPool(t, cfg, nil, countingDial(t, &dials, &down))

	if got := dials.Load(); got != 1 {
		t.Fatalf("dials after startup = %d, want 1", got)
	}
	if st := p.Status()[0]; st.Connected || !strings.Contains(st.LastError, "connection refused") {
		t.Errorf("Status after a failed start = %+v, want not connected with the reason", st)
	}

	// A call between turns doesn't try to connect.
	if _, err := p.Call(context.Background(), "g.echo", nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Call while down: error = %v, want ErrUnavailable", err)
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("dials after a call = %d, want still 1", got)
	}

	// Each turn tries exactly once, and the count doesn't pile up.
	for turn := 2; turn <= 3; turn++ {
		p.Refresh(context.Background())
		if got := dials.Load(); got != int32(turn) {
			t.Errorf("dials after turn %d = %d, want %d", turn-1, got, turn)
		}
		if names := toolNames(p); len(names) != 0 {
			t.Errorf("Tools() = %v while down, want none", names)
		}
	}

	// The user starts the server; the next turn connects and gets its tools.
	down.Store(false)
	p.Refresh(context.Background())
	if got := toolNames(p); !slices.Equal(got, []string{"g.echo"}) {
		t.Errorf("Tools() after the server started = %v, want [g.echo]", got)
	}
	// A connected server isn't dialled again.
	p.Refresh(context.Background())
	if got := dials.Load(); got != 4 {
		t.Errorf("dials after connecting = %d, want 4", got)
	}
}

func TestNoBackgroundWork(t *testing.T) {
	// requests counts every HTTP request the fake server gets. It answers
	// each with 503, as a server still starting up might.
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

	p := openPool(t, ServerConfig{Name: "down", URL: ts.URL + "/mcp", Allow: []string{"echo"}}, nil, dialTransport)
	after := requests.Load()
	if after == 0 {
		t.Fatal("the startup try sent no request")
	}
	// Status reads only what the Pool holds.
	for range 5 {
		_ = p.Status()
		_ = p.Tools()
	}
	// Longer than any retry wait the Pool ever had.
	time.Sleep(300 * time.Millisecond)
	if got := requests.Load(); got != after {
		t.Errorf("requests with no turn running = %d, want %d (the startup try only)", got, after)
	}
}

func TestStatusCountsFromConfig(t *testing.T) {
	var dials atomic.Int32
	var down atomic.Bool
	down.Store(true)
	cfg := ServerConfig{
		Name: "g", URL: "http://127.0.0.1:8000/mcp",
		Allow:         []string{"echo", "add", "pid"},
		Confirm:       []string{"add"},
		AlwaysConfirm: []string{"add", "pid"},
	}
	p := openPool(t, cfg, nil, countingDial(t, &dials, &down))
	st := p.Status()[0]
	want := ServerStatus{Name: "g", Transport: "http", URL: cfg.URL, Listed: 3, Confirms: 2}
	if st.Name != want.Name || st.Transport != want.Transport || st.URL != want.URL ||
		st.Listed != want.Listed || st.Confirms != want.Confirms || st.Connected {
		t.Errorf("Status() = %+v, want %+v, not connected", st, want)
	}
	stdio := openPool(t, ServerConfig{Name: "s", Command: "unused", Allow: []string{"echo"}}, nil, memoryDial(t, new(atomic.Int32)))
	if st := stdio.Status()[0]; st.Transport != "stdio" || st.URL != "" || !st.Connected || st.Listed != 1 {
		t.Errorf("stdio Status() = %+v", st)
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
