// This file tests the note a turn sends when a tool server waits for the
// user to sign in.

package agent

import (
	"context"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

func TestSignInNeeded(t *testing.T) {
	tests := []struct {
		name     string
		route    string
		servers  []rpc.ServerInfo
		wantText string // "" when no note should go
	}{
		{"a tools turn names the server", "tools",
			[]rpc.ServerInfo{{Name: "google", Kind: "mcp", Connected: true}, {Name: "github", Kind: "mcp", SignIn: true, Link: "https://example.com/authorize"}},
			"github needs you to sign in before Meru can use its tools."},
		{"two servers", "search+tools",
			[]rpc.ServerInfo{{Name: "github", Kind: "mcp", SignIn: true}, {Name: "slack", Kind: "mcp", SignIn: true}},
			"github and slack need you to sign in before Meru can use their tools."},
		{"every server signed in", "tools", []rpc.ServerInfo{{Name: "github", Kind: "mcp", Connected: true}}, ""},
		// A turn that offers no server's tools has nothing to miss.
		{"a search turn", "search", []rpc.ServerInfo{{Name: "github", Kind: "mcp", SignIn: true}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &fakeTools{specs: []engine.ToolSpec{spec("read_file"), spec("google.search_gmail_messages")}, servers: tt.servers}
			eng := &fakeEngine{rounds: []fakeRound{{pieces: []string{"ok"}}}}
			evs, err := run(context.Background(), toolsAgent(t, tt.route, eng, tools), rpc.Request{Text: "how many stars does meru have?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			var got *rpc.Event
			for i := range evs {
				if evs[i].Type == rpc.EventSignInNeeded {
					got = &evs[i]
				}
			}
			switch {
			case tt.wantText == "" && got != nil:
				t.Errorf("sent %+v, want no sign-in note", *got)
			case tt.wantText != "" && got == nil:
				t.Fatal("sent no sign-in note")
			case tt.wantText != "":
				if got.Text != tt.wantText {
					t.Errorf("text = %q, want %q", got.Text, tt.wantText)
				}
				// The note names the servers and never carries a link.
				for _, s := range got.Servers {
					if s.Link != "" || s.Name == "" {
						t.Errorf("server %+v carries a link or no name", s)
					}
				}
			}
		})
	}
}
