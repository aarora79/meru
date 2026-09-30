// This file tests the health check against an in-memory MCP server, the
// tool list saved to the state file, a secret field's check, and the
// error-output tail.

package connectors

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// healthSession connects to an in-memory server whose "check" tool
// answers text, as an error when isError is set.
func healthSession(t *testing.T, text string, isError bool) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "health", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "check"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}, nil, nil
		})
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs
}

// TestRunHealthCheck checks each kind of expect against good and bad
// answers.
func TestRunHealthCheck(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		isError bool
		expect  string
		wantErr string // "" for a pass
	}{
		{"any answer", "", false, "", ""},
		{"nonempty passes", "notes", false, "nonempty", ""},
		{"nonempty fails", "  ", false, "nonempty", "empty answer"},
		{"contains passes", "vault notes", false, "contains:notes", ""},
		{"contains fails", "vault work", false, "contains:notes", `doesn't mention "notes"`},
		{"json key passes", `{"vaults":[]}`, false, "json_key:vaults", ""},
		{"json key fails", `{"other":1}`, false, "json_key:vaults", `has no "vaults"`},
		{"not json", "vaults", false, "json_key:vaults", `has no "vaults"`},
		{"a tool error fails", "vault is locked\nmore", true, "", "reported an error: vault is locked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runHealthCheck(context.Background(), healthSession(t, tt.answer, tt.isError), Health{Tool: "check", Expect: tt.expect}, false)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("err = %v, want a pass", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want one holding %q", err, tt.wantErr)
			}
		})
	}
}

// TestToolCache checks that the saved list comes back only for the same
// connector and version.
func TestToolCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "obsidian.json")
	if err := writeToolCache(path, "obsidian", "2.0.1", []*mcp.Tool{{Name: "obsidian_read_note"}}); err != nil {
		t.Fatal(err)
	}
	if tools, ok := readToolCache(path, "obsidian", "2.0.1"); !ok || len(tools) != 1 || tools[0].Name != "obsidian_read_note" {
		t.Errorf("read back %v, %v", tools, ok)
	}
	if _, ok := readToolCache(path, "obsidian", "2.0.2"); ok {
		t.Error("a list from another version came back")
	}
	if _, ok := readToolCache(path, "google", "2.0.1"); ok {
		t.Error("a list from another connector came back")
	}
	if _, ok := readToolCache(filepath.Join(t.TempDir(), "none.json"), "obsidian", "2.0.1"); ok {
		t.Error("a missing file gave a list")
	}
}

// TestSecretField checks a secret field: its value comes from
// secrets.toml under connector_<id>_<field>, never from config, and
// Status says only whether it is saved.
func TestSecretField(t *testing.T) {
	m := Manifest{ID: "mail", Name: "Mail", Fields: []Field{
		{ID: "token", Type: FieldSecret, Label: "API token", Required: true},
		{ID: "folder", Type: FieldFolder, Label: "Folder", Default: "~/mail"},
	}}
	dir := t.TempDir()
	path := secrets.Path(dir)
	st := checkSettings(m, config.Connector{"enabled": true}, nil, dir)
	// A label that starts with an acronym keeps its capitals.
	if st.problem != "Mail needs your API token." {
		t.Errorf("problem = %q", st.problem)
	}
	if err := secrets.Set(path, SecretName("mail", "token"), "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st = checkSettings(m, config.Connector{"enabled": true}, sec, dir)
	if st.values["token"] != "s3cret-value" || !st.saved["token"] || st.shown["token"] != "" {
		t.Errorf("token: value %q saved %v shown %q", st.values["token"], st.saved["token"], st.shown["token"])
	}
	// The folder's default, "~/mail", doesn't exist under dir.
	if want := "Mail can't find the folder ~/mail."; st.problem != want {
		t.Errorf("problem = %q, want %q", st.problem, want)
	}
}

// TestTailLog checks that the tail keeps the last line that isn't blank,
// cuts a long line, and wraps an error with it.
func TestTailLog(t *testing.T) {
	var w tailLog
	w.log = slog.New(slog.DiscardHandler)
	_, _ = w.Write([]byte("starting\nvault is lo"))
	if got := w.last(); got != "vault is lo" {
		t.Errorf("last = %q, want the unfinished line", got)
	}
	_, _ = w.Write([]byte("cked\n\n"))
	if got := w.last(); got != "vault is locked" {
		t.Errorf("last = %q", got)
	}
	if got := w.wrap(errors.New("connect: EOF")).Error(); got != "connect: EOF (vault is locked)" {
		t.Errorf("wrap = %q", got)
	}
	_, _ = w.Write([]byte(strings.Repeat("x", 1000) + "\n"))
	if got := w.last(); len(got) != tailLineCap {
		t.Errorf("a long line kept %d bytes, want %d", len(got), tailLineCap)
	}
	var none *tailLog
	if none.last() != "" {
		t.Error("a nil tail should say nothing")
	}
}
