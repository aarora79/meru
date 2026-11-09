// This file holds the `meru mcp` commands: status (also `meru mcp` alone),
// add, list and remove; adopt and unadopt live in adopt.go, and set and
// fix, for connectors, in connector.go. It reads the words on the command
// line and hands the real work to the shared flow in setup.go (add), to
// merud (the state of each server), and to internal/catalog (the file
// edits). See
// ARCHITECTURE.md, "MCP" and "Adding an MCP server".

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/tui"
)

// mcpUsage lists the `meru mcp` command shapes.
const mcpUsage = `usage:
  meru mcp [status] [--json]                       the state of each server and connector
  meru mcp list                                    the catalog and your servers
  meru mcp add <catalog-name>                      add a server from the catalog
  meru mcp add stdio <name> -- <command> [args...] add a server merud starts
  meru mcp add http <name> <url> [--remote]        add a Streamable HTTP server you run
  meru mcp remove [--yes] <name>                   take a server out of config.toml
  meru mcp login <name>                            sign in to a server whose entry has auth = "oauth"
  meru mcp adopt [--yes] <obsidian|google>         let Meru run a server you set up by hand
  meru mcp unadopt [--yes] <obsidian|google>       put the hand-added entry back
  meru mcp set <connector> [key=value...]          set a connector's fields, or enabled=true|false
  meru mcp fix <connector>                         ask for what a connector needs, or check it again`

// mcpCmd runs `meru mcp ...`. args are the words after "mcp". With no
// words, or "status", it prints each server's state and each connector's;
// --json prints the same as JSON. The older forms `add <name> -- <command>` and `add <name>
// --url <url>` still work; they mean `add stdio` and `add http`.
// `list-catalog`, and `add` with nothing after it, print only the catalog.
func mcpCmd(ctx context.Context, socket string, args []string, c *console) error {
	if len(args) > 0 && args[0] == "status" {
		args = args[1:]
	}
	switch {
	case len(args) == 0:
		return mcpStatus(ctx, socket, false, c.out)
	case len(args) == 1 && isJSONFlag(args[0]):
		return mcpStatus(ctx, socket, true, c.out)
	case len(args) == 1 && args[0] == "list":
		return mcpList(ctx, socket, c.out)
	case len(args) == 1 && (args[0] == "list-catalog" || args[0] == "add"):
		listCatalog(c.out)
		return nil
	case len(args) >= 2 && args[0] == "add":
		e, err := addEntry(args[1:])
		if err != nil {
			return err
		}
		_, err = c.offer(ctx, socket, e)
		return err
	case len(args) == 2 && args[0] == "login":
		return mcpLogin(ctx, socket, args[1], c.out, signInPoll)
	case len(args) >= 2 && args[0] == "remove":
		return c.remove(ctx, socket, args[1:])
	case len(args) >= 1 && (args[0] == "adopt" || args[0] == "unadopt"):
		return c.adopt(ctx, socket, args[1:], args[0] == "unadopt")
	case len(args) >= 1 && args[0] == "set":
		return c.setConnector(ctx, socket, args[1:])
	case len(args) >= 1 && args[0] == "fix":
		return c.fixConnector(ctx, socket, args[1:])
	}
	return errors.New(mcpUsage)
}

// mcpStatus runs `meru mcp` and `meru mcp status`: it asks merud for each
// server's state and prints the table, then the connectors. With asJSON it
// prints both as one JSON object: {"servers": [...], "connectors": [...]}.
// merud answers from what it holds, without asking any server, so
// this is quick while a server is down. It fails when merud can't be
// reached or answers with an error.
func mcpStatus(ctx context.Context, socket string, asJSON bool, out io.Writer) error {
	rows := []rpc.MCPStatus{} // [] rather than null in the JSON
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpMCPStatus}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventMCPStatus:
			rows = append(rows, ev.MCP...)
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	// Then every connector merud knows, the ones that are off or set up
	// by hand included. A merud from before the connectors op answers
	// with an error, and the servers are all there is to say.
	conns := connectorRows(ctx, socket)
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		// The struct tags name the keys; both lists come out as [] rather
		// than null when empty.
		return enc.Encode(struct {
			Servers    []rpc.MCPStatus       `json:"servers"`
			Connectors []rpc.ConnectorStatus `json:"connectors"`
		}{rows, conns})
	}
	// tui.MCPTable lays out the rows the way the chat's /mcp box does.
	fmt.Fprintln(out, strings.Join(tui.MCPTable(rows), "\n"))
	if lines := tui.ConnectorTable(conns); len(lines) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, strings.Join(lines, "\n"))
	}
	return nil
}

// connectorRows asks merud for its connectors (rpc.OpConnectors), and
// returns an empty list when merud can't say.
func connectorRows(ctx context.Context, socket string) []rpc.ConnectorStatus {
	rows := []rpc.ConnectorStatus{}
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpConnectors}, nil) {
		if err != nil || ev.Type == rpc.EventError {
			return []rpc.ConnectorStatus{}
		}
		if ev.Type == rpc.EventConnectors {
			rows = append(rows, ev.Connectors...)
		}
	}
	return rows
}

// isJSONFlag reports whether word is --json, with one dash or two.
func isJSONFlag(word string) bool {
	return word == "--json" || word == "-json"
}

// addEntry turns the words after `meru mcp add` into the entry to add. It
// fails on a shape it doesn't know, a bad name, words after a catalog
// name, and a URL off this machine without --remote.
func addEntry(words []string) (catalog.Entry, error) {
	var e catalog.Entry
	switch {
	case words[0] == "stdio":
		// stdio <name> -- <command> [args...]
		if len(words) < 4 || words[2] != "--" {
			return e, errors.New("usage: meru mcp add stdio <name> -- <command> [args...]")
		}
		return customStdio(words[1], words[3], words[4:])
	case words[0] == "http":
		// http <name> <url> [--remote]
		if len(words) < 3 || len(words) > 4 || (len(words) == 4 && !isRemoteFlag(words[3])) {
			return e, errors.New("usage: meru mcp add http <name> <url> [--remote]")
		}
		return customHTTP(words[1], words[2], len(words) == 4)
	case len(words) >= 3 && words[1] == "--":
		// The older form: <name> -- <command> [args...]
		return customStdio(words[0], words[2], words[3:])
	case len(words) >= 3 && (words[1] == "--url" || words[1] == "-url"):
		// The older form: <name> --url <url> [--remote]
		if len(words) > 4 || (len(words) == 4 && !isRemoteFlag(words[3])) {
			return e, errors.New("usage: meru mcp add http <name> <url> [--remote]")
		}
		return customHTTP(words[0], words[2], len(words) == 4)
	}

	// Anything else names a catalog entry, which takes nothing after it.
	name := words[0]
	e, ok := catalog.Find(name)
	if !ok {
		return e, fmt.Errorf("%q is not in the catalog; run meru mcp list, or add it by hand: meru mcp add stdio %s -- <command> [args...]", name, name)
	}
	if len(words) > 1 {
		return e, fmt.Errorf("%s takes no arguments; got %s", name, strings.Join(words[1:], " "))
	}
	return e, nil
}

// customStdio makes the entry for a stdio server of the user's own. It
// fails on a bad name, or when command is a URL.
func customStdio(name, command string, args []string) (catalog.Entry, error) {
	if err := catalog.CheckName(name); err != nil {
		return catalog.Entry{}, err
	}
	if strings.HasPrefix(command, "http://") || strings.HasPrefix(command, "https://") {
		return catalog.Entry{}, fmt.Errorf("%s is a URL; for a running server use: meru mcp add http %s %s", command, name, command)
	}
	return catalog.Custom(name, command, args), nil
}

// customHTTP makes the entry for a Streamable HTTP server of the user's
// own. A URL off this machine needs remote, because each tool call then
// sends data there. It fails on a bad name or URL.
func customHTTP(name, url string, remote bool) (catalog.Entry, error) {
	if err := catalog.CheckName(name); err != nil {
		return catalog.Entry{}, err
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return catalog.Entry{}, fmt.Errorf("url %q must start with http:// or https://", url)
	}
	e := catalog.Custom(name, url, nil)
	if e.Remote && !remote {
		return e, fmt.Errorf("%s is not on this machine. Each tool call would send your data there. "+
			"If you mean that, add --remote: meru mcp add http %s %s --remote", url, name, url)
	}
	return e, nil
}

// isRemoteFlag reports whether word is --remote, with one dash or two.
func isRemoteFlag(word string) bool {
	return word == "--remote" || word == "-remote"
}

// listCatalog prints each catalog entry: its name, what it gives the
// model, and what it needs.
func listCatalog(out io.Writer) {
	fmt.Fprintln(out, "Servers Meru knows (meru mcp add <name>):")
	for _, e := range catalog.Entries() {
		fmt.Fprintf(out, "  %-10s %s\n  %-10s   needs %s\n", e.Name, e.Description, "", e.Requires)
	}
	fmt.Fprintln(out, "Any other server: meru mcp add stdio <name> -- <command> [args...], or meru mcp add http <name> <url>")
}

// mcpList runs `meru mcp list`: the catalog, then the servers in
// config.toml with the state merud reports for each. Without merud it
// lists the servers in config.toml and says merud didn't answer. It fails
// when config.toml doesn't load.
func mcpList(ctx context.Context, socket string, out io.Writer) error {
	listCatalog(out)

	cfg, err := config.Load(configPathFor(socket))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\nYour servers:")
	if len(cfg.MCP.Servers) == 0 {
		fmt.Fprintln(out, "  none yet")
		return nil
	}
	state, err := mcpState(ctx, socket)
	if err != nil {
		fmt.Fprintf(out, "  (merud didn't answer, so their state is unknown: %v)\n", err)
	}
	for _, s := range cfg.MCP.Servers {
		fmt.Fprintf(out, "  %-10s %s\n", s.Name, serverLine(s, state))
	}
	return nil
}

// mcpState asks merud for its tool sources and returns the MCP servers by
// name. It fails when merud can't be reached or replies with an error.
func mcpState(ctx context.Context, socket string) (map[string]rpc.ServerInfo, error) {
	state := map[string]rpc.ServerInfo{}
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpTools}, nil) {
		if err != nil {
			return nil, err
		}
		switch ev.Type {
		case rpc.EventTools:
			for _, s := range ev.Servers {
				if s.Kind == "mcp" {
					state[s.Name] = s
				}
			}
		case rpc.EventError:
			return nil, errors.New(ev.Error)
		}
	}
	return state, nil
}

// serverLine sums up one server from config.toml: how merud reaches it, and
// from merud's state whether it is connected and how many of its tools the
// model may use. state is nil when merud isn't running.
func serverLine(s config.MCPServer, state map[string]rpc.ServerInfo) string {
	where := "stdio · " + s.Command
	if s.URL != "" {
		where = "http · " + s.URL
	}
	if state == nil {
		return fmt.Sprintf("%s · %d allowed in config", where, len(s.Allow))
	}
	info, ok := state[s.Name]
	switch {
	case !ok:
		return where + " · not loaded yet; restart merud"
	case !info.Connected:
		msg := where + " · not connected"
		if info.LastError != "" {
			msg += ": " + info.LastError
		}
		return fmt.Sprintf("%s · %d allowed in config", msg, len(s.Allow))
	}
	return fmt.Sprintf("%s · connected · offers %d, %d allowed", where, info.Offered, len(info.Tools))
}

// remove runs `meru mcp remove [--yes] <name>`: after a yes it takes the
// server's block out of config.toml and asks merud to reload. Keys in
// secrets.toml stay, since another server may use them. It fails when the
// name isn't in config.toml or the file can't be written.
func (c *console) remove(ctx context.Context, socket string, words []string) error {
	sure := false
	if len(words) == 2 && (words[0] == "--yes" || words[0] == "-yes") {
		sure, words = true, words[1:]
	}
	if len(words) != 1 || strings.HasPrefix(words[0], "-") {
		return errors.New("usage: meru mcp remove [--yes] <name>")
	}
	name := words[0]
	configPath := configPathFor(socket)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	var found *config.MCPServer
	for i := range cfg.MCP.Servers {
		if cfg.MCP.Servers[i].Name == name {
			found = &cfg.MCP.Servers[i]
		}
	}
	if found == nil {
		return fmt.Errorf("%s has no MCP server named %q; meru mcp list shows them", configPath, name)
	}
	if !sure {
		ok, err := c.yes(fmt.Sprintf("Remove %s (%s) from %s?", name, serverLine(*found, nil), configPath), false)
		if err != nil || !ok {
			if err == nil {
				fmt.Fprintln(c.out, "Nothing was changed.")
			}
			return err
		}
	}
	removed, err := catalog.RemoveServer(configPath, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Took this out of %s:\n\n%s\n", configPath, removed)
	up := ping(ctx, socket, io.Discard) == nil
	c.reload(ctx, socket, "", up)
	return nil
}

// reload asks merud to read config.toml again and swap in a new MCP pool,
// then prints the state of the server called name: connected or not, and
// the tools the model may use. An empty name prints only that merud
// reloaded. When merud isn't running (up is false), it says the change
// takes effect when merud starts. A failed reload isn't an error for the
// caller, since config.toml is already written; it prints how to restart.
func (c *console) reload(ctx context.Context, socket, name string, up bool) {
	if !up {
		fmt.Fprintln(c.out, "merud isn't running. The change takes effect when it starts: merud &")
		return
	}
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpMCPReload}, nil) {
		if err != nil {
			fmt.Fprintf(c.out, "merud didn't reload (%v).\n%s\n", err, restartHint)
			return
		}
		switch ev.Type {
		case rpc.EventError:
			fmt.Fprintf(c.out, "merud didn't reload: %s\n%s\n", ev.Error, restartHint)
			return
		case rpc.EventTools:
			c.printReloaded(ev.Servers, name)
		}
	}
}

// printReloaded prints what merud reports after a reload: the block for the
// server called name, as `meru tools` shows it, or a note that merud
// doesn't list it.
func (c *console) printReloaded(servers []rpc.ServerInfo, name string) {
	if name == "" {
		fmt.Fprintln(c.out, "merud reloaded its MCP servers.")
		return
	}
	for _, s := range servers {
		if s.Kind == "mcp" && s.Name == name {
			fmt.Fprintln(c.out, "merud reloaded. No restart needed:")
			fmt.Fprint(c.out, toolsText([]rpc.ServerInfo{s}, newLook(c.out)))
			return
		}
	}
	fmt.Fprintf(c.out, "merud reloaded, but doesn't list %s. Run meru tools to check.\n", name)
}

// signInPoll is how often `meru mcp login` asks merud whether the sign-in
// ended, and signInWait how long it waits in all: merud's own limit.
const (
	signInPoll = 2 * time.Second
	signInWait = 10 * time.Minute
)

// mcpLogin runs `meru mcp login <name>`: it asks merud to start a sign-in
// for the server, prints the link to open, then asks for the server's
// state every poll until it connects, the sign-in ends without it, or
// signInWait passes. It fails when merud refuses the sign-in, such as for
// a server without auth = "oauth", or when the sign-in ends unconnected.
func mcpLogin(ctx context.Context, socket, name string, out io.Writer, poll time.Duration) error {
	link := ""
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpMCPSignIn, ID: name}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventSignIn:
			link = ev.Text
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	if link == "" {
		return errors.New("merud gave no sign-in link")
	}
	// The link is printed first, so it can be copied even where no browser
	// opens, such as over SSH.
	fmt.Fprintf(out, "Open this link in your browser to sign in to %s:\n\n  %s\n\nWaiting for you to sign in…\n", name, link)
	deadline := time.Now().Add(signInWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		row, ok := serverRow(ctx, socket, name)
		switch {
		case !ok:
			continue
		case row.State == rpc.MCPConnected:
			// Allowed counts the names in the allow list, which may name a
			// tool the server doesn't offer; meru tools warns about those.
			fmt.Fprintf(out, "Signed in. %s offers %d tools, and your allow list names %d; meru tools shows which Meru can use.\n",
				name, row.Tools, row.Allowed)
			return nil
		case row.State == rpc.MCPSignIn && row.Link == "":
			// The sign-in ended and the server still needs one.
			return fmt.Errorf("the sign-in to %s didn't finish; merud.log says why. Run meru mcp login %s to try again", name, name)
		}
	}
	return fmt.Errorf("you didn't finish signing in within %v", signInWait)
}

// serverRow returns merud's mcp_status row for name, and false when merud
// can't be reached or has no such server.
func serverRow(ctx context.Context, socket, name string) (rpc.MCPStatus, bool) {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpMCPStatus}, nil) {
		if err != nil {
			return rpc.MCPStatus{}, false
		}
		for _, r := range ev.MCP {
			if r.Name == name {
				return r, true
			}
		}
	}
	return rpc.MCPStatus{}, false
}
