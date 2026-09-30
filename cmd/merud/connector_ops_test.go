// This file tests connector_set and connector_fix over the socket,
// against a whole merud on a fake engine: what each refuses, what it
// writes to config.toml and secrets.toml, that a secret never comes back,
// and the "connector" events that follow the connector. No test here
// installs a connector: each change leaves it off, or short of a field,
// so nothing downloads. test/e2e takes a connector all the way to ok.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// connectorEvents returns the "connector" events in evs.
func connectorEvents(evs []rpc.Event) []rpc.ConnectorStatus {
	var out []rpc.ConnectorStatus
	for _, ev := range evs {
		if ev.Type == rpc.EventConnector && ev.Connector != nil {
			out = append(out, *ev.Connector)
		}
	}
	return out
}

// lastConnector returns the last "connector" event's status, which says
// where the connector settled.
func lastConnector(t *testing.T, evs []rpc.Event) rpc.ConnectorStatus {
	t.Helper()
	all := connectorEvents(evs)
	if len(all) == 0 {
		t.Fatalf("no connector event in %+v", evs)
	}
	return all[len(all)-1]
}

// boolPtr returns a pointer to b, for ConnectorChange.Enabled.
func boolPtr(b bool) *bool { return &b }

func TestConnectorSetOp(t *testing.T) {
	dir, home := meruHome(t)
	vault := filepath.Join(home, "Notes")
	if err := os.Mkdir(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, dir, settingsHeader+`
[[mcp.servers]]
name    = "google"
url     = "http://127.0.0.1:1/mcp"
`, &fakeEngine{version: "0.13.0"})
	cfgPath := filepath.Join(dir, "config.toml")

	refusals := []struct {
		name string
		id   string
		ch   rpc.ConnectorChange
		want string
	}{
		{"on without the vault", "obsidian", rpc.ConnectorChange{Enabled: boolPtr(true)}, "Obsidian needs your vault folder."},
		{"a vault that isn't there", "obsidian", rpc.ConnectorChange{Enabled: boolPtr(true), Values: map[string]string{"vault_path": "~/Nowhere"}},
			"can't find the vault folder ~/Nowhere"},
		{"a field the manifest lacks", "obsidian", rpc.ConnectorChange{Values: map[string]string{"colour": "blue"}}, "no setting called colour"},
		{"a connector set up by hand", "google", rpc.ConnectorChange{Enabled: boolPtr(true)}, "adopt it first"},
		{"Ollama", "ollama", rpc.ConnectorChange{Enabled: boolPtr(false)}, "not run by Meru"},
		{"no such connector", "nothing", rpc.ConnectorChange{Enabled: boolPtr(true)}, `no connector called "nothing"`},
	}
	for _, r := range refusals {
		t.Run(r.name, func(t *testing.T) {
			before, _ := os.ReadFile(cfgPath)
			ch := r.ch
			_, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpConnectorSet, ID: r.id, Connector: &ch}, rpc.ChoiceOnce)
			if !strings.Contains(msg, r.want) {
				t.Errorf("error = %q, want it to hold %q", msg, r.want)
			}
			if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
				t.Error("a refused change wrote config.toml")
			}
		})
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpConnectorSet, ID: "obsidian"}, rpc.ChoiceOnce); !strings.Contains(msg, "needs a change") {
		t.Errorf("no change: %q", msg)
	}

	t.Run("a vault, left off", func(t *testing.T) {
		ch := rpc.ConnectorChange{Enabled: boolPtr(false), Values: map[string]string{"vault_path": "~/Notes"}}
		evs := mustCall(t, d.sock, rpc.Request{Op: rpc.OpConnectorSet, ID: "obsidian", Connector: &ch})
		st := lastConnector(t, evs)
		if st.ID != "obsidian" || st.State != rpc.ConnectorOff {
			t.Errorf("status = %+v, want obsidian off", st)
		}
		for _, f := range st.Fields {
			if f.ID == "vault_path" && f.Value != "~/Notes" {
				t.Errorf("vault_path shows %q, want ~/Notes as written", f.Value)
			}
		}
		tab := loadConfig(t, dir).Connectors["obsidian"]
		if on, ok := tab.Enabled(); !ok || on {
			t.Errorf("enabled = %v, %v; want false", on, ok)
		}
		if v, _ := tab.Value("vault_path"); v != "~/Notes" {
			t.Errorf("vault_path = %q", v)
		}
	})

	t.Run("web search off", func(t *testing.T) {
		ch := rpc.ConnectorChange{Enabled: boolPtr(false)}
		st := lastConnector(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpConnectorSet, ID: "searxng", Connector: &ch}))
		if st.State != rpc.ConnectorOff || st.Sentence != "Web search is off." {
			t.Errorf("status = %+v", st)
		}
		if on, ok := loadConfig(t, dir).Connectors["searxng"].Enabled(); !ok || on {
			t.Errorf("[connectors.searxng] enabled = %v, %v", on, ok)
		}
	})
}

// TestConnectorSetSecret checks the secret's path: it lands in
// secrets.toml under connector_<id>_<field>, with mode 0600, never in
// config.toml, and no event carries it.
func TestConnectorSetSecret(t *testing.T) {
	dir, _ := meruHome(t)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.13.0"})
	const secret = "fake-client-secret-5678"
	ch := rpc.ConnectorChange{
		Enabled: boolPtr(false),
		Values:  map[string]string{"email": "dana@example.com", "client_id": "123-abc.apps.googleusercontent.com"},
		Secrets: map[string]string{"client_secret": secret},
	}
	evs := mustCall(t, d.sock, rpc.Request{Op: rpc.OpConnectorSet, ID: "google", Connector: &ch})
	for _, ev := range evs {
		if strings.Contains(ev.Error, secret) || (ev.Connector != nil && strings.Contains(ev.Connector.Sentence, secret)) {
			t.Errorf("an event holds the secret: %+v", ev)
		}
		if ev.Connector == nil {
			continue
		}
		for _, f := range ev.Connector.Fields {
			if f.Value == secret {
				t.Errorf("field %s carries the secret", f.ID)
			}
			if f.ID == "client_secret" && !f.Saved {
				t.Error("client_secret doesn't show as saved")
			}
		}
	}
	path := filepath.Join(dir, "secrets.toml")
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "connector_google_client_secret") || !strings.Contains(string(raw), secret) {
		t.Errorf("secrets.toml = %q, %v", raw, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secrets.toml mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	cfgRaw, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(cfgRaw), secret) {
		t.Error("config.toml holds the secret")
	}
	if v, _ := loadConfig(t, dir).Connectors["google"].Value("email"); v != "dana@example.com" {
		t.Errorf("email = %q", v)
	}
}

// TestConnectorFixOp checks Fix's first half: a connector short of a
// field gets its status with the fields to ask; one that is off gets its
// status and nothing else happens.
func TestConnectorFixOp(t *testing.T) {
	dir, _ := meruHome(t)
	d := startDaemon(t, dir, settingsHeader+"\n[connectors.obsidian]\nenabled = true\n", &fakeEngine{version: "0.13.0"})

	evs := mustCall(t, d.sock, rpc.Request{Op: rpc.OpConnectorFix, ID: "obsidian"})
	st := lastConnector(t, evs)
	if st.State != rpc.ConnectorNeedsConfig || len(st.Fix) != 1 || st.Fix[0] != "vault_path" {
		t.Errorf("fix = %+v, want needs_config asking for vault_path", st)
	}
	if n := len(connectorEvents(evs)); n != 1 {
		t.Errorf("%d connector events, want one", n)
	}

	st = lastConnector(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpConnectorFix, ID: "google"}))
	if st.State != rpc.ConnectorOff {
		t.Errorf("google = %+v, want off", st)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpConnectorFix, ID: "nothing"}, rpc.ChoiceOnce); !strings.Contains(msg, "no connector called") {
		t.Errorf("an unknown connector: %q", msg)
	}
}
