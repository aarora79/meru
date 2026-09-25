// This file tests the MCP probe and reload ops over the socket, against a
// real MCP server over Streamable HTTP and against this test binary run as
// a stdio server, and the [[commands]] check at startup.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// stdioServerEnv, when set to "1", turns this test binary into a stdio MCP
// server with one tool, echo. stdioPIDEnv names a file it writes its
// process ID to, so a test can check the process ended.
const (
	stdioServerEnv = "MERUD_TEST_MCPSERVER"
	stdioPIDEnv    = "MERUD_TEST_PIDFILE"
)

// TestMain runs before the package's tests. It serves MCP when merud
// started this binary as a server, and runs the tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) != "1" {
		os.Exit(m.Run())
	}
	if err := os.WriteFile(os.Getenv(stdioPIDEnv), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "test server:", err)
		os.Exit(1)
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "stdio-test", Version: "2"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "echo", Description: "Say it back."},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}}, nil, nil
		})
	if err := srv.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "test server:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// stdioServerEntry returns an [[mcp.servers]] entry that runs this test
// binary as the stdio server name, writing its pid to pidFile.
func stdioServerEntry(t *testing.T, name, pidFile, allow string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// GORACE: a -race binary sleeps a second on exit unless told not to.
	return fmt.Sprintf(`
[[mcp.servers]]
name    = %q
command = %q
args    = ["-test.run=^$"]
env     = { %s = "1", %s = %q, GORACE = "atexit_sleep_ms=0" }
allow   = [%s]
`, name, exe, stdioServerEnv, stdioPIDEnv, pidFile, allow)
}

// mcpServers asks merud for its sources and keeps the MCP servers.
func mcpServers(t *testing.T, evs []rpc.Event) []rpc.ServerInfo {
	t.Helper()
	var out []rpc.ServerInfo
	for _, ev := range evs {
		if ev.Type != rpc.EventTools {
			continue
		}
		for _, s := range ev.Servers {
			if s.Kind == "mcp" {
				out = append(out, s)
			}
		}
	}
	return out
}

// toolNamesOf lists the tool names of the servers.
func toolNamesOf(servers []rpc.ServerInfo) []string {
	var names []string
	for _, s := range servers {
		for _, tool := range s.Tools {
			names = append(names, tool.Name)
		}
	}
	return names
}

// lastEvent returns the closing event of a reply.
func lastEvent(evs []rpc.Event) rpc.Event {
	return evs[len(evs)-1]
}

func TestMCPProbeOp(t *testing.T) {
	dir := shortDir(t)
	url, lastAuth := startMCPServerAuth(t)
	const token = "Bearer probe-token-1234"
	if err := os.WriteFile(filepath.Join(dir, "secrets.toml"), []byte(fmt.Sprintf("tok = %q\n", token)), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, dir, "", &fakeEngine{version: "0.13.0"})

	evs := call(t, d.sock, rpc.Request{Op: rpc.OpMCPProbe, Server: &rpc.ProbeServer{
		Name: "files", URL: url, Headers: map[string]string{"Authorization": "secret:tok"},
	}})
	if last := lastEvent(evs); last.Type != rpc.EventDone {
		t.Fatalf("probe closed with %+v", last)
	}
	var res *rpc.ProbeResult
	for _, ev := range evs {
		if ev.Type == rpc.EventProbe {
			res = ev.Probe
		}
	}
	if res == nil {
		t.Fatalf("no probe event in %+v", evs)
	}
	if res.ServerName != "test" {
		t.Errorf("server name = %q, want test", res.ServerName)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		wantDestructive := tool.Name == "delete"
		if (tool.Destructive != nil && *tool.Destructive) != wantDestructive {
			t.Errorf("%s: Destructive = %v, want %v", tool.Name, tool.Destructive, wantDestructive)
		}
	}
	if !slices.Equal(names, []string{"delete", "echo", "fail"}) {
		t.Errorf("tools = %v, want delete, echo, fail", names)
	}
	if got := lastAuth(); got != token {
		t.Errorf("Authorization = %q, want the value from secrets.toml", got)
	}
	// A probe saves nothing: merud still has no MCP servers.
	if got := mcpServers(t, call(t, d.sock, rpc.Request{Op: rpc.OpTools})); len(got) != 0 {
		t.Errorf("after a probe merud has servers %+v, want none", got)
	}

	errs := []struct {
		name   string
		server *rpc.ProbeServer
		want   string
	}{
		{"no server", nil, "needs a server"},
		{"missing secret", &rpc.ProbeServer{Name: "files", URL: url, Headers: map[string]string{"Authorization": "secret:nope"}}, `headers "Authorization"`},
		{"off this machine", &rpc.ProbeServer{Name: "far", URL: "http://example.com/mcp"}, "remote = true"},
		{"bad command", &rpc.ProbeServer{Name: "gone", Command: filepath.Join(dir, "no-such-server")}, `mcp server "gone"`},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			last := lastEvent(call(t, d.sock, rpc.Request{Op: rpc.OpMCPProbe, Server: tt.server}))
			if last.Type != rpc.EventError || !strings.Contains(last.Error, tt.want) {
				t.Errorf("last event = %+v, want an error containing %q", last, tt.want)
			}
		})
	}
}

func TestMCPReloadOp(t *testing.T) {
	dir := shortDir(t)
	url := startMCPServer(t)
	d := startDaemon(t, dir, "", &fakeEngine{version: "0.13.0"})
	cfgPath := filepath.Join(dir, "config.toml")

	steps := []struct {
		name   string
		config string
		want   []string // the MCP tools after the reload
		err    string   // a piece of the error; empty means success
	}{
		{"add", fmt.Sprintf("[[mcp.servers]]\nname = \"files\"\nurl = %q\nallow = [\"echo\"]\n", url),
			[]string{"files.echo"}, ""},
		{"change", fmt.Sprintf("[[mcp.servers]]\nname = \"files\"\nurl = %q\nallow = [\"echo\", \"fail\"]\n", url),
			[]string{"files.echo", "files.fail"}, ""},
		// A bad entry fails the reload, and the old pool stays.
		{"bad entry", fmt.Sprintf("[[mcp.servers]]\nname = \"files\"\nurl = %q\nallow = [\"*\"]\n", url),
			[]string{"files.echo", "files.fail"}, "wildcards"},
		{"bad toml", "[[mcp.servers]\n", []string{"files.echo", "files.fail"}, "config.toml"},
		{"remove", "", nil, ""},
	}
	for _, s := range steps {
		if err := os.WriteFile(cfgPath, []byte(s.config), 0o600); err != nil {
			t.Fatal(err)
		}
		evs := call(t, d.sock, rpc.Request{Op: rpc.OpMCPReload})
		last := lastEvent(evs)
		if s.err == "" && last.Type != rpc.EventDone {
			t.Fatalf("%s: reload closed with %+v", s.name, last)
		}
		if s.err != "" && (last.Type != rpc.EventError || !strings.Contains(last.Error, s.err)) {
			t.Fatalf("%s: reload closed with %+v, want an error containing %q", s.name, last, s.err)
		}
		if s.err == "" {
			if got := toolNamesOf(mcpServers(t, evs)); !slices.Equal(got, s.want) {
				t.Errorf("%s: reload reply lists %v, want %v", s.name, got, s.want)
			}
		}
		if got := toolNamesOf(mcpServers(t, call(t, d.sock, rpc.Request{Op: rpc.OpTools}))); !slices.Equal(got, s.want) {
			t.Errorf("%s: tools = %v, want %v", s.name, got, s.want)
		}
		// The status op, over the socket, reports the pool the reload left:
		// one connected row while files is configured, none after remove.
		var rows []rpc.MCPStatus
		for _, ev := range call(t, d.sock, rpc.Request{Op: rpc.OpMCPStatus}) {
			if ev.Type == rpc.EventMCPStatus {
				rows = ev.MCP
			}
		}
		if want := min(len(s.want), 1); len(rows) != want || (want == 1 && (rows[0].Name != "files" || rows[0].State != rpc.MCPConnected)) {
			t.Errorf("%s: mcp_status rows = %+v, want %d connected", s.name, rows, want)
		}
	}
}

// TestCommandsAtStartup checks that merud lists a declared command as the
// "commands" source, and refuses to start on a bad [[commands]] entry,
// naming it.
func TestCommandsAtStartup(t *testing.T) {
	dir := shortDir(t)
	good := "[[commands]]\nname = \"disk-free\"\nargv = [\"df\", \"-h\"]\n"
	d := startDaemon(t, dir, good, &fakeEngine{version: "0.13.0"})
	var found bool
	for _, ev := range call(t, d.sock, rpc.Request{Op: rpc.OpTools}) {
		for _, s := range ev.Servers {
			if s.Name == "commands" && s.Kind == "command" && len(s.Tools) == 1 &&
				s.Tools[0].Name == "cmd.disk-free" && slices.Equal(s.Tools[0].Argv, []string{"df", "-h"}) {
				found = true
			}
		}
	}
	if !found {
		t.Error("the tools op doesn't list cmd.disk-free with its argv")
	}
	d.stop()

	bad := "[[commands]]\nname = \"shell\"\nargv = [\"bash\", \"-c\", \"{script}\"]\n" +
		"[commands.params.script]\ntype = \"string\"\n"
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func(config.Config, *slog.Logger) (engine.Engine, error) { return &fakeEngine{version: "0.13.0"}, nil }
	err := run(context.Background(), []string{"-config", cfgPath, "-socket", filepath.Join(dir, "d.sock")}, io.Discard, build)
	if err == nil || !strings.Contains(err.Error(), `command "shell"`) || !strings.Contains(err.Error(), "runs code given as text") {
		t.Errorf("run = %v, want a refusal naming the command", err)
	}
}
