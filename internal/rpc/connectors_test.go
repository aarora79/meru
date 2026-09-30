// This file tests the connector helpers every client shares: which
// fields Fix asks for, and the hint a terminal prints.

package rpc

import (
	"slices"
	"testing"
)

func TestAskFields(t *testing.T) {
	fields := []ConnectorField{
		{ID: "email", Type: "email"}, {ID: "client_secret", Type: "secret"}, {ID: "sign_in", Type: "oauth"},
	}
	tests := []struct {
		name string
		c    ConnectorStatus
		want []string
	}{
		{"the fields Fix names", ConnectorStatus{State: ConnectorNeedsConfig, Fix: []string{"client_secret"}, Fields: fields}, []string{"client_secret"}},
		{"off: every field but the sign-in", ConnectorStatus{State: ConnectorOff, Fields: fields}, []string{"email", "client_secret"}},
		{"a sign-in to wait for: none", ConnectorStatus{State: ConnectorNeedsConfig, Fields: fields, Link: "https://example.test/"}, nil},
		{"failed: none, Fix checks again", ConnectorStatus{State: ConnectorFailed, Fields: fields}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, f := range AskFields(tt.c) {
				got = append(got, f.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("AskFields = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFixHint(t *testing.T) {
	if got, want := FixHint("obsidian", []string{"vault_path"}), "Run meru mcp fix obsidian to set vault_path."; got != want {
		t.Errorf("FixHint = %q, want %q", got, want)
	}
	if FixHint("obsidian", nil) != "" {
		t.Error("no fields should give no hint")
	}
}
