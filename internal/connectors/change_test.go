// This file tests CheckChange, the check merud runs on a connector_set
// before it writes anything, and Recheck, the second half of Fix, for the
// stdio supervisor and the container one.

package connectors

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// manifestByID returns the real manifest id.
func manifestByID(t *testing.T, id string) Manifest {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no %s manifest", id)
	return Manifest{}
}

func TestCheckChange(t *testing.T) {
	home := t.TempDir()
	vault := filepath.Join(home, "Notes")
	if err := os.Mkdir(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	saved := (&secrets.Secrets{}).With(SecretName("google", "client_secret"), "fake-secret-0000")
	googleOK := map[string]string{"email": "dana@example.com", "client_id": "123-abc.apps.googleusercontent.com"}

	tests := []struct {
		name  string
		id    string
		table config.Connector
		sec   *secrets.Secrets
		ch    Change
		want  string // part of the error; "" for none
	}{
		{"obsidian on with a vault", "obsidian", nil, nil,
			Change{Enabled: &on, Values: map[string]string{"vault_path": vault}}, ""},
		{"obsidian on with the vault written ~/", "obsidian", nil, nil,
			Change{Enabled: &on, Values: map[string]string{"vault_path": "~/Notes"}}, ""},
		{"obsidian on with no vault", "obsidian", nil, nil,
			Change{Enabled: &on}, "Obsidian needs your vault folder."},
		{"obsidian on with a missing vault", "obsidian", nil, nil,
			Change{Enabled: &on, Values: map[string]string{"vault_path": "/no/such/vault"}}, "can't find the vault folder /no/such/vault"},
		{"obsidian already on, new vault only", "obsidian", config.Connector{"enabled": true}, nil,
			Change{Values: map[string]string{"vault_path": vault}}, ""},
		{"obsidian off may lack its vault", "obsidian", nil, nil,
			Change{Enabled: &off}, ""},
		{"off, but a value that breaks its rule", "obsidian", nil, nil,
			Change{Enabled: &off, Values: map[string]string{"vault_name": "My Notes"}}, "doesn't fit the form"},
		{"a field the manifest lacks", "obsidian", nil, nil,
			Change{Values: map[string]string{"vault": vault}}, "has no setting called vault"},
		{"a value on two lines", "obsidian", nil, nil,
			Change{Values: map[string]string{"vault_path": vault + "\nx"}}, "must fit on one line"},
		{"google on with every field", "google", nil, nil,
			Change{Enabled: &on, Values: googleOK, Secrets: map[string]string{"client_secret": "fake-secret-1111"}}, ""},
		{"google on with the secret saved already", "google", nil, saved,
			Change{Enabled: &on, Values: googleOK}, ""},
		{"google on without the secret", "google", nil, nil,
			Change{Enabled: &on, Values: googleOK}, "Google needs your OAuth client secret."},
		{"a secret sent as a value", "google", nil, nil,
			Change{Values: map[string]string{"client_secret": "x"}}, "is a secret"},
		{"a value sent as a secret", "google", nil, nil,
			Change{Secrets: map[string]string{"email": "x"}}, "has no secret called email"},
		{"an empty secret", "google", nil, nil,
			Change{Secrets: map[string]string{"client_secret": "  "}}, "is empty"},
		{"a value for the sign-in", "google", nil, nil,
			Change{Values: map[string]string{"sign_in": "yes"}}, "the server signs you in"},
		{"a client ID of the wrong form", "google", nil, saved,
			Change{Enabled: &on, Values: map[string]string{"email": "dana@example.com", "client_id": "nope"}}, "doesn't fit the form"},
		{"web search on", "searxng", nil, nil, Change{Enabled: &on}, ""},
		{"web search takes no values", "searxng", nil, nil,
			Change{Values: map[string]string{"url": "x"}}, "has no setting called url"},
		{"ollama isn't Meru's to run", "ollama", nil, nil, Change{Enabled: &on}, "not run by Meru"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := tt.table
			before := len(table)
			err := CheckChange(manifestByID(t, tt.id), table, tt.sec, home, tt.ch)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("error = %v, want none", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("error = %v, want one holding %q", err, tt.want)
			}
			if len(table) != before {
				t.Error("CheckChange changed the caller's table")
			}
		})
	}
}

// TestFieldTypesMatchRPC fails when the field types a manifest may use
// drift from rpc.FieldTypes, the list every client draws an input for.
// The clients can't import this package, so each side names them.
func TestFieldTypesMatchRPC(t *testing.T) {
	ours := []string{FieldText, FieldFolder, FieldSecret, FieldEmail, FieldChoice, FieldOAuth}
	if !slices.Equal(ours, rpc.FieldTypes()) {
		t.Errorf("field types = %v, rpc.FieldTypes = %v", ours, rpc.FieldTypes())
	}
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		for _, f := range m.Fields {
			if !slices.Contains(rpc.FieldTypes(), f.Type) {
				t.Errorf("%s field %s has type %q, which no client draws", m.ID, f.ID, f.Type)
			}
		}
	}
}

// TestRecheckRunsTheCheckAgain checks Fix's second half on a ready
// connector: the saved tool list would let it skip the check, but Recheck
// starts the program and checks it again.
func TestRecheckRunsTheCheckAgain(t *testing.T) {
	s, f, _ := readyConnector(t)
	if n := f.dials.Load(); n != 1 {
		t.Fatalf("starts = %d before Recheck, want 1", n)
	}
	s.Recheck()
	waitFor(t, "the second check", func() bool { return f.dials.Load() == 2 })
	waitPhase(t, s, phaseReady)
}

// TestRecheckLeavesConfigProblems checks that Recheck can't mend a
// missing field: the connector stays at needs_config and nothing starts.
func TestRecheckLeavesConfigProblems(t *testing.T) {
	f := newFakeConnector(t)
	s, _ := testSupervisor(t, f)
	s.Recheck() // before Configure: nothing to do
	s.Configure(config.Connector{"enabled": true}, nil, false)
	s.Recheck()
	if st := s.Status(); st.State != StateNeedsConfig || len(st.Fix) != 1 {
		t.Errorf("status = %+v, want needs_config naming vault_path", st)
	}
	if f.installs.Load() != 0 || f.dials.Load() != 0 {
		t.Error("Recheck installed or started a connector short of a field")
	}
}

// TestContainerRecheck checks that Recheck on a watched SearXNG that
// wasn't answering looks again at once, and finds it once it answers.
func TestContainerRecheck(t *testing.T) {
	d := &fakeDocker{}
	c, _, _ := testContainer(t, d, false)
	c.Recheck() // before Configure: nothing to do
	c.Configure(nil, searxURL, true)
	waitStatus(t, c, StateNeedsConfig, "nothing answers there")
	d.set(func(d *fakeDocker) { d.serves = true })
	c.Recheck()
	waitStatus(t, c, StateOK, "uses the SearXNG already running")
}
