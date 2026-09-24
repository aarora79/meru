// This file tests the built-in tools: the configure tool's answers, what it
// writes to config.toml, and the Backend methods dispatch relies on.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/secrets"
)

// The compiler checks that *Tools implements dispatch.Backend: assigning a
// value to a variable of the interface type fails to build otherwise.
var _ dispatch.Backend = (*Tools)(nil)

func TestConfigure(t *testing.T) {
	tests := []struct {
		name       string
		args       string
		secrets    map[string]string // entries to put in secrets.toml first
		wantErr    string            // "" means the call succeeds
		wantText   string
		wantServer string // the server config.toml must hold afterwards
	}{
		{
			name:       "catalog entry with no secret",
			args:       `{"action":"add_mcp_server","catalog":"fetch"}`,
			wantText:   "Allowed tools: fetch",
			wantServer: "fetch",
		},
		{
			name:       "catalog entry with its secret",
			args:       `{"action":"add_mcp_server","catalog":"brave"}`,
			secrets:    map[string]string{"brave_api_key": "fake-key-0123456789"},
			wantText:   "brave_web_search",
			wantServer: "brave",
		},
		{
			name:     "catalog entry that asks first",
			args:     `{"action":"add_mcp_server","catalog":"gmail"}`,
			secrets:  map[string]string{"google_oauth_client_id": "fake-id-0123456789", "google_oauth_client_secret": "fake-secret-0123456789"},
			wantText: "These ask the user before each call: draft_gmail_message, send_gmail_message",
		},
		{
			name:    "missing secret",
			args:    `{"action":"add_mcp_server","catalog":"brave"}`,
			wantErr: "meru mcp add brave",
		},
		{
			name:       "custom command",
			args:       `{"action":"add_mcp_server","name":"notes","command":"notes-mcp","args":["--dir","/tmp"]}`,
			wantText:   "meru tools",
			wantServer: "notes",
		},
		{
			name:       "custom url",
			args:       `{"action":"add_mcp_server","name":"cal","url":"http://127.0.0.1:8123/mcp"}`,
			wantText:   "allows no tools yet",
			wantServer: "cal",
		},
		{"unknown action", `{"action":"remove_mcp_server","catalog":"fetch"}`, nil, "unknown", "", ""},
		{"unknown key", `{"action":"add_mcp_server","catalog":"fetch","allow":["*"]}`, nil, "valid JSON", "", ""},
		{"not an object", `"fetch"`, nil, "valid JSON", "", ""},
		{"unknown catalog name", `{"action":"add_mcp_server","catalog":"slack"}`, nil, "not in the catalog", "", ""},
		{"catalog and custom", `{"action":"add_mcp_server","catalog":"fetch","name":"x","command":"y"}`, nil, "not both", "", ""},
		{"nothing to add", `{"action":"add_mcp_server"}`, nil, "give catalog", "", ""},
		{"command and url", `{"action":"add_mcp_server","name":"x","command":"y","url":"http://127.0.0.1:1/mcp"}`, nil, "exactly one", "", ""},
		{"neither command nor url", `{"action":"add_mcp_server","name":"x"}`, nil, "exactly one", "", ""},
		{"bad name", `{"action":"add_mcp_server","name":"a.b","command":"y"}`, nil, "letters, digits", "", ""},
		{"args with url", `{"action":"add_mcp_server","name":"x","url":"http://127.0.0.1:1/mcp","args":["a"]}`, nil, "args go with command", "", ""},
		{"url without scheme", `{"action":"add_mcp_server","name":"x","url":"127.0.0.1:1/mcp"}`, nil, "http://", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.toml")
			for name, value := range tt.secrets {
				if err := secrets.Set(secrets.Path(dir), name, value); err != nil {
					t.Fatal(err)
				}
			}
			changes := 0
			tools := New(configPath, config.Builtin{}, func(context.Context) error {
				changes++
				return nil
			})

			res, err := tools.Call(context.Background(), Configure, json.RawMessage(tt.args))
			if err != nil {
				t.Fatalf("Call returned error %v; refusals belong in the Result", err)
			}
			if tt.wantErr != "" {
				if !res.IsError || !strings.Contains(res.Text, tt.wantErr) {
					t.Fatalf("Result = %+v, want an error containing %q", res, tt.wantErr)
				}
				if _, err := os.Stat(configPath); err == nil {
					t.Error("a refused call wrote config.toml")
				}
				if changes != 0 {
					t.Error("a refused call ran onChange")
				}
				return
			}
			if res.IsError || !strings.Contains(res.Text, tt.wantText) {
				t.Fatalf("Result = %+v, want success containing %q", res, tt.wantText)
			}
			if changes != 1 {
				t.Errorf("onChange ran %d times, want 1", changes)
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				t.Fatalf("Load after configure: %v", err)
			}
			if len(cfg.MCP.Servers) != 1 {
				t.Fatalf("config holds %d servers, want 1", len(cfg.MCP.Servers))
			}
			if tt.wantServer != "" && cfg.MCP.Servers[0].Name != tt.wantServer {
				t.Errorf("server = %q, want %q", cfg.MCP.Servers[0].Name, tt.wantServer)
			}
			// No secret value may reach the model.
			for _, v := range tt.secrets {
				if strings.Contains(res.Text, v) {
					t.Errorf("result holds a secret value: %s", res.Text)
				}
			}
		})
	}
}

func TestConfigureTwiceRefuses(t *testing.T) {
	tools := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{}, nil)
	args := json.RawMessage(`{"action":"add_mcp_server","catalog":"fetch"}`)
	if res, _ := tools.Call(context.Background(), Configure, args); res.IsError {
		t.Fatalf("first call: %s", res.Text)
	}
	res, _ := tools.Call(context.Background(), Configure, args)
	if !res.IsError || !strings.Contains(res.Text, "already has") {
		t.Errorf("second call = %+v, want a refusal", res)
	}
}

func TestConfigureReloadFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	tools := New(path, config.Builtin{}, func(context.Context) error { return errors.New("pool broke") })
	res, _ := tools.Call(context.Background(), Configure, json.RawMessage(`{"action":"add_mcp_server","catalog":"fetch"}`))
	if !res.IsError || !strings.Contains(res.Text, "restart merud") || !strings.Contains(res.Text, "pool broke") {
		t.Errorf("Result = %+v, want an error that says to restart merud", res)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the server should stay written when only the reload fails")
	}
}

func TestBackend(t *testing.T) {
	tools := New("config.toml", config.Builtin{Confirm: []string{Configure, "write_file"}}, nil)

	confirms := []struct {
		name string
		want dispatch.Confirm
	}{
		{Configure, dispatch.ConfirmAlways}, // listed in confirm, still always
		{"write_file", dispatch.ConfirmAsk},
		{"remember", dispatch.ConfirmNever},
	}
	for _, c := range confirms {
		if got := tools.Confirm(c.name); got != c.want {
			t.Errorf("Confirm(%q) = %v, want %v", c.name, got, c.want)
		}
	}

	if tools.Kind() != dispatch.KindBuiltin {
		t.Errorf("Kind = %q", tools.Kind())
	}
	if s, tool := tools.Locate(Configure); s != "meru" || tool != Configure {
		t.Errorf("Locate = %q, %q", s, tool)
	}

	specs := tools.Tools()
	if len(specs) != 1 || specs[0].Name != Configure {
		t.Fatalf("Tools = %+v, want configure alone", specs)
	}
	if !strings.Contains(specs[0].Description, "gmail") {
		t.Error("the description doesn't list the catalog")
	}
	var schema map[string]any
	if err := json.Unmarshal(specs[0].Parameters, &schema); err != nil || schema["type"] != "object" {
		t.Errorf("Parameters isn't a JSON Schema object: %v %s", err, specs[0].Parameters)
	}

	st := tools.Status()
	if len(st) != 1 || st[0].Name != "meru" || !st[0].Connected || st[0].Kind != "builtin" ||
		len(st[0].Tools) != 1 || !st[0].Tools[0].AlwaysAsks {
		t.Errorf("Status = %+v", st)
	}

	if _, err := tools.Call(context.Background(), "remember", json.RawMessage(`{}`)); err == nil {
		t.Error("Call on an unknown built-in succeeded")
	}
}
