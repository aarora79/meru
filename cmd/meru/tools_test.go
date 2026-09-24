// This file tests `meru tools` and `meru log` against an in-process rpc
// server that plays merud.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// fakeAudit answers OpTools and OpLog with fixed replies, and remembers the
// last request.
type fakeAudit struct {
	req     rpc.Request
	servers []rpc.ServerInfo
	log     []rpc.LogEntry
}

func (f *fakeAudit) handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	f.req = req
	switch req.Op {
	case rpc.OpTools:
		return emit(rpc.Event{Type: rpc.EventTools, Servers: f.servers})
	case rpc.OpLog:
		return emit(rpc.Event{Type: rpc.EventLog, Log: f.log})
	}
	return nil
}

func TestToolsCommand(t *testing.T) {
	servers := []rpc.ServerInfo{
		{Name: "notes", Kind: "mcp", Transport: "stdio", Connected: true, Offered: 5,
			Tools:   []rpc.ToolInfo{{Name: "notes.search", Confirm: true}, {Name: "notes.read"}},
			Unknown: []string{"serch"}},
		{Name: "mail", Kind: "mcp", Transport: "http", LastError: "connection refused", Offered: 3,
			Tools: []rpc.ToolInfo{{Name: "mail.send", Confirm: true}}},
		{Name: "meru", Kind: "builtin", Connected: true, Offered: 3,
			Tools: []rpc.ToolInfo{{Name: "remember"}, {Name: "configure", Confirm: true, AlwaysAsks: true}}},
		{Name: "commands", Kind: "command", Connected: true, Offered: 2,
			Tools: []rpc.ToolInfo{
				{Name: "cmd.git-log", Argv: []string{"git", "-C", "{repo}", "log", "--since=1.week"}},
				{Name: "cmd.say", Confirm: true, Argv: []string{"say", "hello there"}},
			}},
	}
	want := `notes  mcp · stdio · connected
  notes.search  asks first
  notes.read
  2 of 5 tools allowed
  warning: allow lists "serch", but notes offers no such tool

mail  mcp · http · not connected: connection refused
  mail.send     asks first
  1 of 3 tools allowed

meru  builtin · connected
  remember
  configure     always asks
  2 of 3 tools allowed

commands  command · connected
  cmd.git-log
    runs: git -C {repo} log --since=1.week
  cmd.say       asks first
    runs: say "hello there"
  2 of 2 tools allowed
`
	tests := []struct {
		name     string
		args     []string
		servers  []rpc.ServerInfo
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"sources", []string{"tools"}, servers, 0, want, ""},
		{"list is the same", []string{"tools", "list"}, servers, 0, want, ""},
		{"no sources", []string{"tools"}, nil, 0, noSources + "\n", ""},
		{"unknown word", []string{"tools", "add"}, servers, 1, "", "usage: meru tools [list]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAudit{servers: tt.servers}
			sock := startServer(t, f.handle)
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}

func TestLogCommand(t *testing.T) {
	// The test builds the times in the local zone, so the printed times
	// match whatever zone the machine runs in.
	at := func(hms string) string {
		tm, err := time.ParseInLocation(time.DateTime, "2026-09-24 "+hms, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return tm.Format(time.RFC3339)
	}
	entries := []rpc.LogEntry{
		{Time: at("10:17:21"), Session: "2026-09-24T101500-ab12", Kind: "mcp", Server: "mail", Tool: "send",
			Args: json.RawMessage(`{"to":"sam@example.com"}`), Outcome: "declined", Approval: "deny"},
		{Time: at("10:16:02"), Session: "2026-09-24T101500-ab12", Kind: "mcp", Server: "notes", Tool: "search",
			Args: json.RawMessage(`{"query":"garden"}`), Result: "3 notes\nmatch", Outcome: "ok", DurationMillis: 120},
	}
	// A command's row shows the argv it ran, as a reader would type it.
	cmdEntry := rpc.LogEntry{Time: at("10:18:40"), Session: "2026-09-24T101500-ab12", Kind: "command", Server: "meru",
		Tool: "cmd.git-log", Outcome: "ok", DurationMillis: 31,
		Args: json.RawMessage(`{"argv":["git","-C","/home/sam/repos/meru","log","--since=1.week","--oneline"],"params":{"repo":"meru"}}`)}
	cmdRow := "2026-09-24 10:18:40  101500-ab12  command  meru.cmd.git-log  ok  -  31 ms  " +
		"git -C /home/sam/repos/meru log --since=1.week --oneline\n"
	rows := "2026-09-24 10:17:21  101500-ab12  mcp  mail.send     declined  deny  0 ms    {\"to\":\"sam@example.com\"}\n" +
		"2026-09-24 10:16:02  101500-ab12  mcp  notes.search  ok        -     120 ms  {\"query\":\"garden\"}\n"
	verbose := "2026-09-24 10:17:21  101500-ab12  mcp  mail.send     declined  deny  0 ms    {\"to\":\"sam@example.com\"}\n" +
		"    (no result)\n" +
		"2026-09-24 10:16:02  101500-ab12  mcp  notes.search  ok        -     120 ms  {\"query\":\"garden\"}\n" +
		"    3 notes match\n"
	tests := []struct {
		name      string
		args      []string
		log       []rpc.LogEntry
		wantCode  int
		wantLimit int
		wantOut   string
		wantErr   string
	}{
		{"default", []string{"log"}, entries, 0, 20, rows, ""},
		{"with -n", []string{"log", "-n", "5"}, entries, 0, 5, rows, ""},
		{"verbose", []string{"log", "-v"}, entries, 0, 20, verbose, ""},
		{"empty", []string{"log"}, nil, 0, 20, "No tool calls yet.\n", ""},
		{"command", []string{"log"}, []rpc.LogEntry{cmdEntry}, 0, 20, cmdRow, ""},
		{"bad -n", []string{"log", "-n", "0"}, entries, 1, 0, "", "usage: meru log"},
		{"extra word", []string{"log", "mail"}, entries, 1, 0, "", "usage: meru log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAudit{log: tt.log}
			sock := startServer(t, f.handle)
			var out, errOut bytes.Buffer
			code := run(context.Background(), append([]string{"-socket", sock}, tt.args...), &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if f.req.Limit != tt.wantLimit {
				t.Errorf("request limit = %d, want %d", f.req.Limit, tt.wantLimit)
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}
