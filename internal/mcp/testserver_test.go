// This file holds the test MCP server, built with the same SDK Meru uses,
// and the helpers that reach it over each transport: in memory, as a stdio
// child process, and over Streamable HTTP on 127.0.0.1.
//
// The stdio server is this test binary itself. TestMain checks an
// environment variable; when set, the binary serves MCP on stdin and stdout
// instead of running tests. Go's own os/exec tests use the same trick, and
// it needs no separate build step.

package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testServerEnv, when set to "1", turns the test binary into the stdio test
// server.
const testServerEnv = "MERU_MCP_TESTSERVER"

// TestMain runs before the package's tests. It either serves MCP (when the
// Pool started this binary as a child) or runs the tests as usual.
func TestMain(m *testing.M) {
	if os.Getenv(testServerEnv) == "1" {
		if err := newTestServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, "test server:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Argument and output types for the test tools. The struct tags name the
// JSON keys; the SDK derives each tool's input schema from them.
type (
	echoArgs struct {
		Text string `json:"text"`
	}
	addArgs struct{ A, B int }
	addOut  struct {
		Sum int `json:"sum"`
	}
	slowArgs struct {
		Millis int `json:"millis"`
	}
	getenvArgs struct {
		Name string `json:"name"`
	}
	noArgs struct{}
)

// newTestServer returns a server with the tools the tests call:
//
//   - echo returns its text; add returns a sum as text and structured content
//   - slow sleeps, stopping early when the call is cancelled
//   - fail reports a tool error; secret is never allowlisted
//   - getenv reads one environment variable; pid returns the process ID
//   - crash exits the process, to test what happens when a server dies
func newTestServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "meru-test", Version: "1"}, nil)

	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}

	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo the text back."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
			return text(in.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "add", Description: "Add two numbers."},
		func(_ context.Context, _ *mcp.CallToolRequest, in addArgs) (*mcp.CallToolResult, addOut, error) {
			return text(fmt.Sprint(in.A + in.B)), addOut{Sum: in.A + in.B}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "slow", Description: "Sleep, then answer."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in slowArgs) (*mcp.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Millis) * time.Millisecond):
				return text("done"), nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always fail."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			res := text("the tool broke")
			res.IsError = true
			return res, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "secret", Description: "Must never reach the model."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			return text("leaked"), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "getenv", Description: "Read an environment variable."},
		func(_ context.Context, _ *mcp.CallToolRequest, in getenvArgs) (*mcp.CallToolResult, any, error) {
			return text(os.Getenv(in.Name)), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "pid", Description: "Return the process ID."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			return text(fmt.Sprint(os.Getpid())), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "crash", Description: "Exit the process."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})
	return s
}

// allTestTools is every allowable test tool except secret.
func allTestTools() []string {
	return []string{"echo", "add", "slow", "fail", "getenv", "pid", "crash"}
}

// stdioConfig returns a config that starts this test binary as a stdio
// server named name.
func stdioConfig(t *testing.T, name string) ServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find test binary: %v", err)
	}
	return ServerConfig{
		Name:    name,
		Command: exe,
		// -test.run with a pattern that matches nothing is a guard: if the
		// env variable were lost, the child would run no tests and exit.
		Args: []string{"-test.run=^$"},
		// GORACE: a binary built with -race sleeps 1 s on exit by default,
		// which would add a second to every Close.
		Env:   map[string]string{testServerEnv: "1", "GORACE": "atexit_sleep_ms=0"},
		Allow: allTestTools(),
	}
}

// httpConfig starts the test server over Streamable HTTP on 127.0.0.1 and
// returns a config that points at it. The server stops when the test ends.
func httpConfig(t *testing.T, name string) ServerConfig {
	t.Helper()
	srv := newTestServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	// httptest.NewServer listens on 127.0.0.1 with a free port.
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ServerConfig{Name: name, URL: ts.URL + "/mcp", Allow: allTestTools()}
}

// memoryDial returns a dialFunc that connects every server to its own
// in-memory test server, for tests that don't need a real transport. Each
// connection gets a fresh server, as a restarted process would. calls
// counts the tools/call requests the servers receive, so a test can prove a
// refused call never reached one.
func memoryDial(t *testing.T, calls *atomic.Int32) dialFunc {
	return func(_ context.Context, _ ServerConfig, _ *slog.Logger) (mcp.Transport, error) {
		srv := newTestServer()
		// Middleware wraps every request the server receives.
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "tools/call" {
					calls.Add(1)
				}
				return next(ctx, method, req)
			}
		})
		clientT, serverT := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(context.Background(), serverT, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = ss.Close() })
		return clientT, nil
	}
}
