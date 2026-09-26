// This file tests Refresh, the turn-start step that lists each connected
// server's tools again, and what happens when a Streamable HTTP server
// restarts behind merud's back: the next turn offers the new tools, and a
// call on the dead session marks the server not connected without being
// sent again. It also holds the benchmark behind the cost that
// ARCHITECTURE.md quotes for the extra tools/list.

package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// restartable is a Streamable HTTP MCP server on 127.0.0.1 that a test can
// restart with a different set of tools while it keeps its address, as a
// user does when they restart workspace-mcp.
type restartable struct {
	t    *testing.T
	addr string // host:port, fixed for the life of the test

	mu      sync.Mutex   // guards the fields below
	handler http.Handler // the current server process, in effect
	srv     *http.Server // nil while stopped
	seen    map[string]bool
	stale   map[string]bool // session IDs from before the last restart
	// jsonGone makes the server answer a stale session with HTTP 404 and a
	// JSON-RPC error in the body, the way some Python servers do, instead
	// of the Go SDK's plain-text "session not found".
	jsonGone bool

	// calls counts the tools/call requests that reached a tool.
	calls atomic.Int32
}

// newRestartable starts a server that offers tools, on a free loopback
// port, and stops it when the test ends.
func newRestartable(t *testing.T, tools ...string) *restartable {
	t.Helper()
	r := &restartable{t: t, seen: map[string]bool{}, stale: map[string]bool{}}
	r.load(tools)
	r.listen("127.0.0.1:0")
	t.Cleanup(r.stop)
	return r
}

// url is the server's MCP endpoint.
func (r *restartable) url() string { return "http://" + r.addr + "/mcp" }

// load swaps in a fresh server that offers tools. Every tool answers "ok".
// Sessions the old server handed out mean nothing to the new one.
func (r *restartable) load(tools []string) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "restartable", Version: "1"}, nil)
	for _, name := range tools {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: "Test tool " + name + "."},
			func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
				r.calls.Add(1)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
			})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handler = h
	for id := range r.seen {
		r.stale[id] = true
	}
}

// ServeHTTP makes restartable an http.Handler. It passes each request to
// the current server, after the jsonGone check.
func (r *restartable) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	id := req.Header.Get("Mcp-Session-Id")
	r.mu.Lock()
	h, jsonGone, stale := r.handler, r.jsonGone, r.stale[id]
	if id != "" {
		r.seen[id] = true
	}
	r.mu.Unlock()
	if jsonGone && stale {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Session not found"}}`))
		return
	}
	h.ServeHTTP(w, req)
}

// listen starts serving on addr and records the address it got.
func (r *restartable) listen(addr string) {
	r.t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		r.t.Fatalf("listen on %s: %v", addr, err)
	}
	srv := &http.Server{Handler: r}
	r.mu.Lock()
	r.addr, r.srv = ln.Addr().String(), srv
	r.mu.Unlock()
	// Serve runs until stop closes the server; its error then is always
	// http.ErrServerClosed, so the test drops it.
	go func() { _ = srv.Serve(ln) }()
}

// stop closes the listener and every open connection, so a request to the
// address is refused, as with a server that exited.
func (r *restartable) stop() {
	r.mu.Lock()
	srv := r.srv
	r.srv = nil
	r.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// restart stops the server and starts a new one offering tools at the same
// address.
func (r *restartable) restart(tools ...string) {
	r.stop()
	r.load(tools)
	r.listen(r.addr)
}

// restartKinds lists the ways a server can come back or stay away, for
// the table-driven tests below. prepare runs before the restart.
var restartKinds = []struct {
	name    string
	prepare func(r *restartable)
	// down leaves the server stopped until after the first Refresh.
	down bool
}{
	{name: "session not found", prepare: func(*restartable) {}},
	{name: "JSON-RPC error body", prepare: func(r *restartable) {
		r.mu.Lock()
		r.jsonGone = true
		r.mu.Unlock()
	}},
	{name: "stopped, then started", prepare: func(*restartable) {}, down: true},
}

// The real case behind Refresh: an allowed tool was missing, the user
// restarted the server with more tools, and merud kept the old list until
// merud itself restarted.
func TestRefreshFollowsARestartedServer(t *testing.T) {
	allow := []string{"search_gmail_messages", "get_gmail_attachment_content"}
	for _, tc := range restartKinds {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestartable(t, "search_gmail_messages")
			p := openPool(t, ServerConfig{Name: "google", URL: r.url(), Allow: allow}, nil, dialTransport)
			if st := p.Status()[0]; !slices.Equal(st.Unknown, []string{"get_gmail_attachment_content"}) {
				t.Fatalf("Unknown at startup = %v, want the missing tool", st.Unknown)
			}

			tc.prepare(r)
			if tc.down {
				r.stop()
				p.Refresh(context.Background())
				if st := p.Status()[0]; st.Connected || st.LastError == "" {
					t.Errorf("Status while stopped = %+v, want not connected with a reason", st)
				}
				if names := toolNames(p); len(names) != 0 {
					t.Errorf("Tools() while stopped = %v, want none", names)
				}
				r.load([]string{"search_gmail_messages", "get_gmail_attachment_content"})
				r.listen(r.addr)
			} else {
				r.restart("search_gmail_messages", "get_gmail_attachment_content")
			}

			p.Refresh(context.Background())
			want := []string{"google.get_gmail_attachment_content", "google.search_gmail_messages"}
			if got := toolNames(p); !slices.Equal(got, want) {
				t.Errorf("Tools() after the restart = %v, want %v", got, want)
			}
			st := p.Status()[0]
			if !st.Connected || st.Offered != 2 || st.Allowed != 2 || len(st.Unknown) != 0 || st.LastError != "" {
				t.Errorf("Status after the restart = %+v, want connected, 2 of 2, no unknown, no error", st)
			}
			callText(t, p, "google.get_gmail_attachment_content")
		})
	}
}

// A server that adds a tool while it runs shows it on the next turn,
// without a new session.
func TestRefreshKeepsTheSessionWhenTheServerStays(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "live", Version: "1"}, nil)
	add := func(name string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name},
			func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{}, nil, nil
			})
	}
	add("search_gmail_messages")
	var dials atomic.Int32
	dial := func(_ context.Context, _ ServerConfig, _ *slog.Logger) (mcp.Transport, error) {
		dials.Add(1)
		clientT, serverT := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(context.Background(), serverT, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = ss.Close() })
		return clientT, nil
	}
	cfg := ServerConfig{Name: "google", Command: "unused",
		Allow: []string{"search_gmail_messages", "get_gmail_attachment_content"}}
	p := openPool(t, cfg, nil, dial)

	add("get_gmail_attachment_content")
	p.Refresh(context.Background())
	if got := toolNames(p); len(got) != 2 {
		t.Errorf("Tools() after the server added a tool = %v, want both", got)
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("dials = %d, want 1: a working session isn't replaced", got)
	}
}

// A turn that ends while the listing waits keeps the session and the old
// list: a slow answer doesn't prove the session is gone.
func TestRefreshKeepsTheListWhenTheTurnEnds(t *testing.T) {
	r := newRestartable(t, "search_gmail_messages")
	p := openPool(t, ServerConfig{Name: "google", URL: r.url(),
		Allow: []string{"search_gmail_messages"}}, nil, dialTransport)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Refresh(ctx)
	if st := p.Status()[0]; !st.Connected || st.Allowed != 1 {
		t.Errorf("Status after a cancelled refresh = %+v, want still connected with its tool", st)
	}
	callText(t, p, "google.search_gmail_messages")
}

// A call on a session the server no longer has fails, marks the server not
// connected when the error says so, and never reaches the new server: the
// Pool doesn't send it again. The next turn reconnects.
func TestCallOnADeadSession(t *testing.T) {
	for _, tc := range restartKinds {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestartable(t, "send_gmail_message")
			p := openPool(t, ServerConfig{Name: "google", URL: r.url(),
				Allow: []string{"send_gmail_message"}}, nil, dialTransport)

			tc.prepare(r)
			if tc.down {
				r.stop()
			} else {
				r.restart("send_gmail_message")
			}
			if _, err := p.Call(context.Background(), "google.send_gmail_message", nil); err == nil {
				t.Fatal("Call on a dead session succeeded, want an error")
			}
			if got := r.calls.Load(); got != 0 {
				t.Errorf("tool ran %d times after the restart, want 0: the call must not be sent again", got)
			}
			// The JSON-RPC error body is a plain protocol error to the SDK,
			// so the call can't tell the session is gone; the next
			// Refresh finds out when its tools/list fails.
			wantDown := tc.name != "JSON-RPC error body"
			if st := p.Status()[0]; st.Connected == wantDown {
				t.Errorf("Connected after the failed call = %v, want %v", st.Connected, !wantDown)
			}

			if tc.down {
				r.listen(r.addr)
			}
			p.Refresh(context.Background())
			callText(t, p, "google.send_gmail_message")
			if got := r.calls.Load(); got != 1 {
				t.Errorf("tool ran %d times, want 1", got)
			}
		})
	}
}

func TestSessionGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"connection closed", mcp.ErrConnectionClosed, true},
		{"session not found", mcp.ErrSessionMissing, true},
		{"refused dial", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"read error", &net.OpError{Op: "read", Err: errors.New("reset")}, false},
		{"timeout", context.DeadlineExceeded, false},
		{"tool error", errors.New("invalid params"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionGone(tt.err); got != tt.want {
				t.Errorf("sessionGone(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// BenchmarkRefresh measures what Refresh adds to a turn with one connected
// server whose tools haven't changed: one tools/list round trip. Run it
// with `go test -run '^$' -bench Refresh ./internal/mcp`.
func BenchmarkRefresh(b *testing.B) {
	for _, tc := range []struct {
		name string
		cfg  func(tb testing.TB) ServerConfig
	}{
		{"http", func(tb testing.TB) ServerConfig { return httpConfig(tb, "t") }},
		{"stdio", func(tb testing.TB) ServerConfig { return stdioConfig(tb, "t") }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			cfg := tc.cfg(b)
			p, err := newPool(context.Background(), []ServerConfig{cfg}, nil, dialTransport)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(p.Close)
			b.ResetTimer()
			for b.Loop() {
				p.Refresh(context.Background())
			}
		})
	}
}
