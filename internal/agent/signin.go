// This file holds the note a turn sends when a tool server waits for the
// user to sign in: an auth = "oauth" server whose tools stay out of every
// turn until the user signs in (ARCHITECTURE.md, "MCP"). Without the note,
// the model answers without those tools and never says why.

package agent

import (
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// signInNeeded sends one EventSignInNeeded naming every tool server that
// waits for the user to sign in, so the client can offer the sign-in. It
// sends nothing when no server waits. Handle calls it on a turn that
// offers tools, after Refresh, which leaves a signed-out server as it is.
// It fails only when the client has gone.
func (a *Agent) signInNeeded(t *turn) error {
	var waiting []rpc.ServerInfo
	var names []string
	for _, s := range a.tools.Servers() {
		if s.SignIn {
			// Only the name and kind go to the client: the link belongs
			// to a sign-in the user starts, never to a chat.
			waiting = append(waiting, rpc.ServerInfo{Name: s.Name, Kind: s.Kind})
			names = append(names, s.Name)
		}
	}
	if len(waiting) == 0 {
		return nil
	}
	return t.emit(rpc.Event{Type: rpc.EventSignInNeeded, Servers: waiting, Text: signInText(names)})
}

// signInText says in one sentence which servers wait for a sign-in, such
// as "github needs you to sign in before Meru can use its tools."
func signInText(names []string) string {
	if len(names) == 1 {
		return names[0] + " needs you to sign in before Meru can use its tools."
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " and " + names[last] +
		" need you to sign in before Meru can use their tools."
}
