// This file tests the built-in tools: the configure tool's answers, what it
// writes to config.toml, and the Backend methods dispatch relies on.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/memory"
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
			name:       "catalog entry with no secret, which the user starts",
			args:       `{"action":"add_mcp_server","catalog":"google"}`,
			wantText:   "Meru only connects to it",
			wantServer: "google",
		},
		{
			name:       "catalog entry with its secret",
			args:       `{"action":"add_mcp_server","catalog":"obsidian"}`,
			secrets:    map[string]string{"obsidian_api_key": "fake-key-0123456789"},
			wantText:   "obsidian_simple_search",
			wantServer: "obsidian",
		},
		{
			name:     "catalog entry that asks first",
			args:     `{"action":"add_mcp_server","catalog":"google"}`,
			wantText: "These ask the user before each call: send_gmail_message, manage_event",
		},
		{
			name:    "missing secret",
			args:    `{"action":"add_mcp_server","catalog":"obsidian"}`,
			wantErr: "meru mcp add obsidian",
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
		{"unknown action", `{"action":"remove_mcp_server","catalog":"google"}`, nil, "unknown", "", ""},
		{"unknown key", `{"action":"add_mcp_server","catalog":"google","allow":["*"]}`, nil, "valid JSON", "", ""},
		{"not an object", `"google"`, nil, "valid JSON", "", ""},
		{"unknown catalog name", `{"action":"add_mcp_server","catalog":"slack"}`, nil, "not in the catalog", "", ""},
		{"catalog and custom", `{"action":"add_mcp_server","catalog":"google","name":"x","command":"y"}`, nil, "not both", "", ""},
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
			tools := New(configPath, config.Builtin{}, config.Web{}, nil, "", nil, func(context.Context) error {
				changes++
				return nil
			}, nil)

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
	tools := New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{}, config.Web{}, nil, "", nil, nil, nil)
	args := json.RawMessage(`{"action":"add_mcp_server","catalog":"google"}`)
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
	tools := New(path, config.Builtin{}, config.Web{}, nil, "", nil, func(context.Context) error { return errors.New("pool broke") }, nil)
	res, _ := tools.Call(context.Background(), Configure, json.RawMessage(`{"action":"add_mcp_server","catalog":"google"}`))
	if !res.IsError || !strings.Contains(res.Text, "restart merud") || !strings.Contains(res.Text, "pool broke") {
		t.Errorf("Result = %+v, want an error that says to restart merud", res)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the server should stay written when only the reload fails")
	}
}

func TestBackend(t *testing.T) {
	tools := New("config.toml", config.Builtin{Confirm: []string{Configure, "write_file"}}, config.Web{}, nil, "", nil, nil, nil)

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
	if !strings.Contains(specs[0].Description, "google") {
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

// rememberTools returns built-in tools over a fresh memory folder, with
// config.toml and secrets.toml in a temp directory, and the memory store.
func rememberTools(t *testing.T, cfg config.Builtin) (*Tools, *memory.Store, string) {
	t.Helper()
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(dir, "config.toml"), cfg, config.Web{}, mem, "", nil, nil, nil), mem, dir
}

// TestRemember runs each call through a real Dispatcher, the only path the
// model has to a tool, so the session reaches remember as it does in merud.
func TestRemember(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		session  string // the call's session; "" for none
		wantErr  string // "" means the call succeeds
		wantText string
		wantKind string
		wantSrc  string
	}{
		{
			name:     "a fact about the user",
			args:     `{"kind":"me","text":"Works on the AI registry team at Example Corp"}`,
			session:  "2026-09-24T144512-cdc3",
			wantText: "Saved to me/works-on-the-ai-registry-team-at-example-corp.md.",
			wantKind: "me",
			wantSrc:  "session 2026-09-24T144512-cdc3",
		},
		{
			name:     "a preference, with no session",
			args:     `{"kind":"preferences","text":"Likes short answers."}`,
			wantText: "Saved to preferences/likes-short-answers.md.",
			wantKind: "preferences",
		},
		{"unknown kind", `{"kind":"secrets","text":"x"}`, "", `kind "secrets" is unknown`, "", "", ""},
		{"a path as the kind", `{"kind":"../me","text":"x"}`, "", "is unknown", "", "", ""},
		{"no kind", `{"text":"x"}`, "", "is unknown", "", "", ""},
		{"empty text", `{"kind":"me","text":"  "}`, "", "text is empty", "", "", ""},
		{"text over 4 KiB", `{"kind":"me","text":"` + strings.Repeat("a", 4097) + `"}`, "", "over the 4096-byte limit", "", "", ""},
		{"a secret in the text", `{"kind":"reference","text":"The obsidian key is fake-key-0123456789"}`, "", "holds a secret", "", "", ""},
		{"unknown key", `{"kind":"me","text":"x","folder":"me"}`, "", "valid JSON", "", "", ""},
		{"not an object", `"x"`, "", "valid JSON", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, mem, dir := rememberTools(t, config.Builtin{})
			if err := secrets.Set(secrets.Path(dir), "obsidian_api_key", "fake-key-0123456789"); err != nil {
				t.Fatal(err)
			}
			d := dispatch.New([]dispatch.Backend{tools}, nil, dispatch.Options{})
			res, out := d.Dispatch(context.Background(), dispatch.Call{
				ID: "c1", Name: Remember, Args: json.RawMessage(tt.args), Session: tt.session,
			})
			all, err := mem.List()
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" {
				if !res.IsError || out.Outcome != dispatch.OutcomeError || !strings.Contains(res.Text, tt.wantErr) {
					t.Fatalf("Result = %+v (%s), want an error containing %q", res, out.Outcome, tt.wantErr)
				}
				if len(all) != 0 {
					t.Errorf("a refused call saved %+v", all)
				}
				return
			}
			if res.IsError || out.Outcome != dispatch.OutcomeOK || res.Text != tt.wantText {
				t.Fatalf("Result = %+v (%s), want %q", res, out.Outcome, tt.wantText)
			}
			if len(all) != 1 || all[0].Kind != tt.wantKind || all[0].Source != tt.wantSrc {
				t.Errorf("memories = %+v, want one of kind %q from %q", all, tt.wantKind, tt.wantSrc)
			}
		})
	}
}

func TestRememberSpecAndConfirm(t *testing.T) {
	tools, mem, _ := rememberTools(t, config.Builtin{})
	// A folder the user made by hand joins the kinds.
	if err := os.Mkdir(filepath.Join(mem.Dir(), "recipes"), 0o700); err != nil {
		t.Fatal(err)
	}
	specs := tools.Tools()
	if len(specs) != 2 || specs[1].Name != Remember {
		t.Fatalf("Tools = %+v, want configure and remember", specs)
	}
	// The struct below names only the part of the schema the test reads;
	// json.Unmarshal skips the rest.
	var schema struct {
		Properties struct {
			Kind struct {
				Enum []string `json:"enum"`
			} `json:"kind"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(specs[1].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	for _, k := range append(memory.DefaultKinds(), "recipes") {
		if !slices.Contains(schema.Properties.Kind.Enum, k) {
			t.Errorf("kind enum %v lacks %q", schema.Properties.Kind.Enum, k)
		}
	}
	for _, want := range []string{"third person", `"me"`, `"preferences"`, "secrets"} {
		if !strings.Contains(specs[1].Description, want) {
			t.Errorf("description lacks %q: %s", want, specs[1].Description)
		}
	}

	// Memories save without asking, unless [builtin] confirm lists remember.
	if got := tools.Confirm(Remember); got != dispatch.ConfirmNever {
		t.Errorf("Confirm(remember) = %v, want ConfirmNever by default", got)
	}
	if st := tools.Status(); st[0].Offered != 2 || st[0].Tools[1].Confirm {
		t.Errorf("Status = %+v, want remember listed without confirm", st)
	}
	asking, _, _ := rememberTools(t, config.Builtin{Confirm: []string{Remember}})
	if got := asking.Confirm(Remember); got != dispatch.ConfirmAsk {
		t.Errorf("Confirm(remember) = %v, want ConfirmAsk when listed", got)
	}
	if st := asking.Status(); !st[0].Tools[1].Confirm {
		t.Errorf("Status = %+v, want remember to show confirm", st)
	}
}

// TestRememberRunsHook checks onRemember runs once after a save, so merud
// can sync the new memory into the store, and not at all after a refusal.
func TestRememberRunsHook(t *testing.T) {
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	tools := New(filepath.Join(dir, "config.toml"), config.Builtin{}, config.Web{}, mem, "", nil, nil, func(context.Context) { runs++ })
	ctx := context.Background()
	if res, _ := tools.Call(ctx, Remember, json.RawMessage(`{"kind":"people","text":"Sam is the user's manager"}`)); res.IsError {
		t.Fatalf("remember failed: %s", res.Text)
	}
	if res, _ := tools.Call(ctx, Remember, json.RawMessage(`{"kind":"people","text":" "}`)); !res.IsError {
		t.Fatal("an empty text was saved")
	}
	if runs != 1 {
		t.Errorf("onRemember ran %d times, want 1", runs)
	}
}
