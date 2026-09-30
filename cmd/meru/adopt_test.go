// This file tests `meru mcp adopt` and `meru mcp unadopt` against a fake
// merud: the command shows the plan, asks, and only then sends the
// request that makes the changes; a no or a refusal changes nothing; and
// Google's values from the flags, the secret among them, reach merud.

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// adoptMerud is a fake merud for the adopt ops. It records each request,
// answers with a plan, and refuses when refuse is set.
type adoptMerud struct {
	mu     sync.Mutex
	reqs   []rpc.Request
	refuse string
}

// handle answers one request.
func (f *adoptMerud) handle(_ context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	switch req.Op {
	case rpc.OpConnectorAdopt, rpc.OpConnectorUnadopt:
		if f.refuse != "" {
			return errors.New(f.refuse)
		}
		applied := req.Adopt != nil && req.Adopt.Apply
		return emit(rpc.Event{Type: rpc.EventAdopt, Adopted: &rpc.AdoptResult{
			ID:      req.ID,
			Changes: []string{"Comment out the google entry.", "Add this after it:\n    [connectors.google]\n    enabled = true"},
			Applied: applied,
		}})
	case rpc.OpConnectors:
		return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: []rpc.ConnectorStatus{{
			ID: "google", Name: "Google", State: rpc.ConnectorNeedsConfig, Sentence: "Google needs you to sign in.",
			Link: "https://accounts.example.test/o/oauth2/auth?client_id=x",
		}}})
	}
	return nil
}

// applies counts the requests that asked merud to make the changes.
func (f *adoptMerud) applies() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Adopt != nil && r.Adopt.Apply {
			n++
		}
	}
	return n
}

// TestMCPAdopt checks the ask-then-apply flow and its answers.
func TestMCPAdopt(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		input   string
		applies int
		want    []string
	}{
		{"yes", []string{"adopt", "google"}, "y\n", 1,
			[]string{"To adopt google, merud will:", "  1. Comment out the google entry.", "     [connectors.google]", "Go ahead?",
				"Done. meru mcp unadopt google puts the old entry back.", "Google: needs config Google needs you to sign in.",
				"Sign in here: https://accounts.example.test/o/oauth2/auth?client_id=x"}},
		{"no", []string{"adopt", "google"}, "n\n", 0, []string{"Nothing was changed."}},
		{"--yes asks nothing", []string{"adopt", "--yes", "obsidian"}, "", 1, []string{"Done."}},
		{"unadopt", []string{"unadopt", "-y", "google"}, "", 1, []string{"To restore the google entry, merud will:", "Done: the google entry is back"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &adoptMerud{}
			sock := startServer(t, f.handle)
			c, out, _ := scripted(tt.input)
			if err := mcpCmd(context.Background(), sock, tt.args, c); err != nil {
				t.Fatalf("mcp %v: %v\n%s", tt.args, err, out)
			}
			if got := f.applies(); got != tt.applies {
				t.Errorf("merud got %d requests to apply, want %d", got, tt.applies)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestMCPAdoptValues checks that --email and --client-id make the command
// ask for the client secret, without echo, and send all three to merud.
func TestMCPAdoptValues(t *testing.T) {
	f := &adoptMerud{}
	sock := startServer(t, f.handle)
	c, out, _ := scripted("invented-secret\ny\n")
	args := []string{"adopt", "--email", "dana@example.com", "--client-id", "123-invented.apps.googleusercontent.com", "google"}
	if err := mcpCmd(context.Background(), sock, args, c); err != nil {
		t.Fatalf("adopt: %v\n%s", err, out)
	}
	if strings.Contains(out.String(), "invented-secret") {
		t.Error("the output shows the secret")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	first := f.reqs[0].Adopt.Values
	if first["email"] != "dana@example.com" || first["client_id"] != "123-invented.apps.googleusercontent.com" || first["client_secret"] != "invented-secret" {
		t.Errorf("values = %v", first)
	}
}

// TestMCPAdoptErrors checks the words the command refuses, and that
// merud's refusal reaches the user and nothing is applied.
func TestMCPAdoptErrors(t *testing.T) {
	f := &adoptMerud{refuse: "another program listens on 127.0.0.1:8000"}
	sock := startServer(t, f.handle)
	for _, args := range [][]string{{"adopt"}, {"adopt", "google", "obsidian"}, {"adopt", "--force", "google"}, {"unadopt", "--email", "a@b", "google"}} {
		c, _, _ := scripted("")
		if err := mcpCmd(context.Background(), sock, args, c); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("mcp %v = %v, want the usage", args, err)
		}
	}
	c, _, _ := scripted("y\n")
	err := mcpCmd(context.Background(), sock, []string{"adopt", "google"}, c)
	if err == nil || !strings.Contains(err.Error(), "another program listens") {
		t.Errorf("adopt = %v, want merud's refusal", err)
	}
	if f.applies() != 0 {
		t.Error("a refused adopt was applied")
	}
}
