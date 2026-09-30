// This file holds the Bridge methods for the rail's status block, Status,
// and for opening links, OpenURL and OpenSource.

package desktop

import (
	"context"
	"errors"
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
	// Hint how to start it. Busy is true when merud took the connection
	// but didn't answer in time: it runs, but a long answer or a Mac short
	// of memory slowed it, so the page mustn't tell the user to start it.
	Up   bool `json:"up"`
	Busy bool `json:"busy,omitempty"`
	// Waiting is true when merud runs but can't answer yet, because
	// Ollama is down, too old or short of a model; Problem then holds
	// Ollama's sentence, such as "Ollama isn't running at
	// http://127.0.0.1:11434.", and the page mustn't tell the user to
	// start merud.
	Waiting bool   `json:"waiting,omitempty"`
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
	// "On this Mac" panel in Settings.
	Chunks int `json:"chunks"`
	// Folders counts the [index] folders, and Profile the memories that go
	// into every prompt. With both at zero Meru knows nothing yet, and the
	// page opens Setup on its own.
	Folders int `json:"folders"`
	Profile int `json:"profile"`
	// Connections names the MCP servers merud holds a connection to, and
	// every connector merud runs, with its state in brackets unless it is
	// ok: "google, obsidian (needs config)".
	Connections []string `json:"connections"`
	// Connectors are the rail's dots: each connector that isn't off, with
	// its state. A click on one opens its card in Settings.
	Connectors []ConnectorDot `json:"connectors"`
}

// Status asks merud what the index holds, which MCP servers are
// connected, and how web search and Ollama stand. It never fails: a merud
// that doesn't answer gives a Status with Up false and the reason in
// Problem, and one that waits for Ollama also sets Waiting. A merud too
// old to know mcp_status or the connectors op still reports its index.
func (b *Bridge) Status(ctx context.Context) Status {
	// Settings can change the answer model, so b.model sits under b.mu.
	b.mu.Lock()
	model := b.model
	b.mu.Unlock()
	s := Status{Socket: b.socket, Machine: machineName(runtime.GOOS), Model: model, Fast: b.fast, Embed: b.embed,
		Connections: []string{}, Connectors: []ConnectorDot{}}
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpIndexStatus}, rpc.EventStatus)
	if err != nil {
		// A timeout means merud took the connection and then didn't answer
		// in time; nothing listening fails the connection at once instead.
		// errors.Is looks through the wrapped errors for the timeout.
		if errors.Is(err, context.DeadlineExceeded) {
			s.Busy = true
			s.Problem = "merud didn't answer within a few seconds. It may be busy with a long answer, " +
				"or the Mac may be short of memory. Meru checks again every 15 seconds."
			return s
		}
		// merud may run but wait for Ollama: then it answers the
		// connectors op, and Ollama's row says why it can't answer yet.
		if why, ok := b.waitingFor(ctx); ok {
			s.Waiting, s.Problem = true, why
			return s
		}
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
			switch {
			case m.Connector != "" && m.Connector != rpc.ConnectorOK:
				// A connector that is starting, needs config or failed
				// still shows, so the rail says what is wrong.
				s.Connections = append(s.Connections, m.Name+" ("+rpc.ConnectorWords(m.Connector)+")")
			case m.State == rpc.MCPConnected:
				s.Connections = append(s.Connections, m.Name)
			}
		}
	}
	// The connectors mcp_status doesn't list: web search, which the rail
	// names when it is on, and Ollama, when it isn't ok. A merud from
	// before the connectors op answers with an error, and adds nothing.
	if ev, err := b.one(ctx, rpc.Request{Op: rpc.OpConnectors}, rpc.EventConnectors); err == nil {
		s.Connectors = connectorDots(ev.Connectors)
		for _, c := range ev.Connectors {
			switch {
			case c.Kind == "container" && c.State == rpc.ConnectorOK:
				s.Connections = append(s.Connections, c.Name)
			case c.Kind == "container" && c.State != rpc.ConnectorOff,
				c.Kind == "dependency" && c.State != rpc.ConnectorOK:
				s.Connections = append(s.Connections, c.Name+" ("+rpc.ConnectorWords(c.State)+")")
			}
		}
	}
	return s
}

// waitingFor asks merud for its connectors after a status request failed.
// When merud answers and Ollama isn't ok, merud runs but waits for
// Ollama: waitingFor returns Ollama's sentence and true. Otherwise it
// returns false.
func (b *Bridge) waitingFor(ctx context.Context) (string, bool) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpConnectors}, rpc.EventConnectors)
	if err != nil {
		return "", false
	}
	for _, c := range ev.Connectors {
		if c.Kind == "dependency" && c.State != rpc.ConnectorOK {
			return c.Sentence, true
		}
	}
	return "", false
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
