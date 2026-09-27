// This file holds the Bridge methods for the rail's status block, Status,
// and for opening links, OpenURL and OpenSource.

package desktop

import (
	"context"
	"fmt"
	"runtime"

	"github.com/aarora79/meru/internal/opener"
	"github.com/aarora79/meru/internal/rpc"
)

// StartHint tells the user how to start merud, for the status block when
// merud doesn't answer.
const StartHint = "Start it in a terminal with: merud"

// Status is what the rail's status block shows.
type Status struct {
	// Up is true when merud answered. Problem says why it didn't, and
	// Hint how to start it.
	Up      bool   `json:"up"`
	Problem string `json:"problem,omitempty"`
	Hint    string `json:"hint,omitempty"`
	// Socket is where the app looks for merud, and Machine names this
	// computer for the page: "this Mac" on macOS, "this computer"
	// elsewhere.
	Socket  string `json:"socket"`
	Machine string `json:"machine"`
	// Model is the main model from config.toml, the one that writes the
	// answers; Fast is the router's and Embed the search index's. Setup
	// shows all three, with the `ollama pull` each needs, when merud is
	// down.
	Model string `json:"model,omitempty"`
	Fast  string `json:"fast,omitempty"`
	Embed string `json:"embed,omitempty"`
	// Documents counts the files the index holds, and Scanning is true
	// while merud scans the [index] folders.
	Documents int  `json:"documents"`
	Scanning  bool `json:"scanning,omitempty"`
	// Chunks counts the pieces those files were cut into, for the
	// Library's "On this Mac" panel.
	Chunks int `json:"chunks"`
	// Folders counts the [index] folders, and Profile the memories that go
	// into every prompt. With both at zero Meru knows nothing yet, and the
	// page opens Setup on its own.
	Folders int `json:"folders"`
	Profile int `json:"profile"`
	// Connections names the MCP servers merud holds a connection to.
	Connections []string `json:"connections"`
}

// Status asks merud what the index holds and which MCP servers are
// connected. It never fails: a merud that doesn't answer gives a Status
// with Up false and the reason in Problem. A merud too old to know
// mcp_status still reports its index.
func (b *Bridge) Status(ctx context.Context) Status {
	// The Library can change the answer model, so b.model sits under b.mu.
	b.mu.Lock()
	model := b.model
	b.mu.Unlock()
	s := Status{Socket: b.socket, Machine: machineName(runtime.GOOS), Model: model, Fast: b.fast, Embed: b.embed,
		Connections: []string{}}
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpIndexStatus}, rpc.EventStatus)
	if err != nil {
		s.Problem = fmt.Sprintf("merud isn't answering at %s.", b.socket)
		s.Hint = StartHint
		return s
	}
	s.Up = true
	if st := ev.Status; st != nil {
		s.Documents, s.Chunks, s.Scanning = st.Documents, st.Chunks, st.Scanning
		s.Folders, s.Profile = len(st.Folders), st.Profile
	}
	if ev, err := b.one(ctx, rpc.Request{Op: rpc.OpMCPStatus}, rpc.EventMCPStatus); err == nil {
		for _, m := range ev.MCP {
			if m.State == rpc.MCPConnected {
				s.Connections = append(s.Connections, m.Name)
			}
		}
	}
	return s
}

// machineName names the computer merud runs on, as the page says it:
// "this Mac" on macOS and "this computer" on every other system. merud
// listens on a Unix socket, so it runs on the same machine as the app.
func machineName(goos string) string {
	if goos == "darwin" {
		return "this Mac"
	}
	return "this computer"
}

// OpenURL opens u, a link the user clicked in an answer, in the browser or
// the file's app. Only http, https and file URLs open (see
// internal/opener); the page never opens a link itself. It fails when u
// isn't one of those, or the opener fails.
func (b *Bridge) OpenURL(u string) error {
	if err := opener.Check(u); err != nil {
		return err
	}
	return b.open(u)
}

// OpenSource opens the file a source chip names, given as merud shows it,
// such as "~/Notes/lisbon.md", in the app the system picks for it. It
// fails when the path can't become a file URL or the opener fails.
func (b *Bridge) OpenSource(path string) error {
	u := rpc.FileURL(path, b.home)
	if u == "" {
		return fmt.Errorf("can't find %s: the home folder is unknown", path)
	}
	return b.OpenURL(u)
}
