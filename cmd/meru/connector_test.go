// This file tests `meru mcp set` and `meru mcp fix` against a fake merud:
// the words each reads, the secret asked for without echo and never
// printed, the fields Fix asks, and merud's progress on the screen.

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorMerud is a fake merud for the connector ops. It answers the
// connectors op with row, a connector_fix with fix, and a connector_set
// with a "starting" step and then settled, and records each change.
type connectorMerud struct {
	row, fix, settled rpc.ConnectorStatus
	refuse            string

	mu      sync.Mutex
	changes []rpc.ConnectorChange
}

// handle answers one request.
func (f *connectorMerud) handle(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	switch req.Op {
	case rpc.OpConnectors:
		return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: []rpc.ConnectorStatus{f.row}})
	case rpc.OpConnectorFix:
		return emit(rpc.Event{Type: rpc.EventConnector, Connector: &f.fix})
	case rpc.OpConnectorSet:
		if f.refuse != "" {
			return errors.New(f.refuse)
		}
		f.mu.Lock()
		f.changes = append(f.changes, *req.Connector)
		f.mu.Unlock()
		step := f.row
		step.State, step.Sentence = rpc.ConnectorStarting, "Meru is installing Obsidian 2.0.1 and checking it."
		if err := emit(rpc.Event{Type: rpc.EventConnector, Connector: &step}); err != nil {
			return err
		}
		return emit(rpc.Event{Type: rpc.EventConnector, Connector: &f.settled})
	}
	return nil
}

// only returns the one change the fake got.
func (f *connectorMerud) only(t *testing.T) rpc.ConnectorChange {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.changes) != 1 {
		t.Fatalf("merud got %d changes, want 1", len(f.changes))
	}
	return f.changes[0]
}

// googleFields are Google's fields as merud reports them, the secret not
// saved yet.
var googleFields = []rpc.ConnectorField{
	{ID: "email", Type: "email", Label: "Email address", Required: true},
	{ID: "client_id", Type: "text", Label: "OAuth client ID", Required: true},
	{ID: "client_secret", Type: "secret", Label: "OAuth client secret", Required: true},
	{ID: "sign_in", Type: "oauth", Label: "Sign in to Google", Required: true},
}

// obsidianFields are Obsidian's fields as merud reports them.
var obsidianFields = []rpc.ConnectorField{
	{ID: "vault_path", Type: "folder", Label: "Vault folder", Help: "The folder that holds your Obsidian notes.", Required: true},
	{ID: "vault_name", Type: "text", Label: "Vault name"},
}

func TestMCPSet(t *testing.T) {
	f := &connectorMerud{
		row: rpc.ConnectorStatus{ID: "google", Name: "Google", State: rpc.ConnectorOff, Fields: googleFields},
		settled: rpc.ConnectorStatus{ID: "google", Name: "Google", State: rpc.ConnectorNeedsConfig,
			Sentence: "Google needs you to sign in.", Link: "https://accounts.example.test/o/oauth2/auth?client_id=x"},
	}
	sock := startServer(t, f.handle)
	c, out, _ := scripted("invented-secret-9\n")
	args := []string{"set", "google", "enabled=true", "email=dana@example.com", "client_id=123.apps.googleusercontent.com", "client_secret"}
	if err := mcpCmd(context.Background(), sock, args, c); err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	ch := f.only(t)
	if ch.Enabled == nil || !*ch.Enabled || ch.Values["email"] != "dana@example.com" || ch.Secrets["client_secret"] != "invented-secret-9" {
		t.Errorf("change = %+v", ch)
	}
	if _, ok := ch.Values["client_secret"]; ok {
		t.Error("the secret went as a plain value")
	}
	text := out.String()
	if strings.Contains(text, "invented-secret-9") {
		t.Error("the output shows the secret")
	}
	for _, want := range []string{"OAuth client secret (it doesn't show as you type)", "  Meru is installing Obsidian 2.0.1 and checking it.",
		"Google: needs config. Google needs you to sign in.", "Sign in here: https://accounts.example.test/"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestMCPSetRefuses(t *testing.T) {
	f := &connectorMerud{row: rpc.ConnectorStatus{ID: "google", Name: "Google", Fields: googleFields}, refuse: "Google needs your OAuth client ID."}
	sock := startServer(t, f.handle)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"set", "google"}, "usage:"},
		{[]string{"set", "google", "client_secret=abc"}, "is a secret"},
		{[]string{"set", "google", "enabled=maybe"}, "true or false"},
		{[]string{"set", "google", "loose-word"}, "usage:"},
		{[]string{"set", "nothing", "enabled=true"}, `no connector called "nothing"`},
		{[]string{"set", "google", "enabled=true"}, "Google needs your OAuth client ID."},
		{[]string{"fix"}, "usage:"},
	}
	for _, tt := range tests {
		c, _, _ := scripted("")
		if err := mcpCmd(context.Background(), sock, tt.args, c); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("mcp %v = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestMCPFix(t *testing.T) {
	ready := rpc.ConnectorStatus{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorOK, Sentence: "Obsidian is ready. It starts when a question needs it."}
	tests := []struct {
		name    string
		fix     rpc.ConnectorStatus
		input   string
		changes int
		values  map[string]string
		enable  bool
		want    []string
	}{
		{"asks the field Fix names", rpc.ConnectorStatus{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorNeedsConfig,
			Sentence: "Obsidian needs your vault folder.", Fix: []string{"vault_path"}, Fields: obsidianFields},
			"~/Notes\n", 1, map[string]string{"vault_path": "~/Notes"}, false,
			[]string{"Vault folder\n  The folder that holds your Obsidian notes.", "Obsidian: ok. Obsidian is ready."}},
		{"turns an off connector on", rpc.ConnectorStatus{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorOff,
			Sentence: "Obsidian is off.", Fields: obsidianFields},
			"y\n~/Notes\n\n", 1, map[string]string{"vault_path": "~/Notes"}, true,
			[]string{"Turn Obsidian on?", "Vault name (optional)"}},
		{"leaves an off connector off", rpc.ConnectorStatus{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorOff, Fields: obsidianFields},
			"n\n", 0, nil, false, []string{"Nothing was changed."}},
		{"a check needs no fields", rpc.ConnectorStatus{ID: "obsidian", Name: "Obsidian", State: rpc.ConnectorFailed,
			Sentence: "Obsidian keeps stopping: out of memory"}, "", 0, nil, false,
			[]string{"Obsidian: failed. Obsidian keeps stopping: out of memory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &connectorMerud{row: tt.fix, fix: tt.fix, settled: ready}
			sock := startServer(t, f.handle)
			c, out, _ := scripted(tt.input)
			if err := mcpCmd(context.Background(), sock, []string{"fix", "obsidian"}, c); err != nil {
				t.Fatalf("fix: %v\n%s", err, out)
			}
			f.mu.Lock()
			n := len(f.changes)
			f.mu.Unlock()
			if n != tt.changes {
				t.Fatalf("merud got %d changes, want %d", n, tt.changes)
			}
			if n == 1 {
				ch := f.only(t)
				if len(ch.Values) != len(tt.values) || ch.Values["vault_path"] != tt.values["vault_path"] {
					t.Errorf("values = %v, want %v", ch.Values, tt.values)
				}
				if got := ch.Enabled != nil && *ch.Enabled; got != tt.enable {
					t.Errorf("enabled = %v, want %v", got, tt.enable)
				}
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}
