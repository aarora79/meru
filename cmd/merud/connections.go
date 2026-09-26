// This file answers the desktop app's connection ops: the list of tool
// sources with each tool's policy (OpConnections), a policy change
// (OpToolPolicy), adding and removing a catalog server (OpMCPAdd,
// OpMCPRemove) and saving an API key (OpSecretSet). Every change is
// written by merud, to config.toml or secrets.toml, with the same safe
// edits `meru mcp add` and the configure tool use (internal/catalog and
// internal/secrets), and then the tools reload. ARCHITECTURE.md, "Desktop
// app", says why the app never writes these files itself.

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// commandsNote tells the user how to change a local command, which the app
// shows but can't change: each one is a [[commands]] entry the user wrote.
const commandsNote = "Local commands are the [[commands]] entries in config.toml. " +
	"To change one, or whether it asks first (confirm), edit config.toml and restart merud."

// handleConnections answers OpConnections with one "connections" event.
func (s *toolService) handleConnections(emit func(rpc.Event) error) error {
	ev, err := s.connectionsEvent()
	if err != nil {
		return err
	}
	return emit(ev)
}

// connectionsEvent builds the "connections" event: config.toml as it is on
// disk, joined with what the backends report now. Built-in tools come
// first, then the MCP servers and A2A agents in config order, then the
// local commands. It fails when config.toml or secrets.toml can't be read.
func (s *toolService) connectionsEvent() (rpc.Event, error) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return rpc.Event{}, err
	}
	sec, err := secrets.Load(secrets.Path(s.dir))
	if err != nil {
		return rpc.Event{}, err
	}
	live := map[string]rpc.ServerInfo{} // by kind and name, such as "mcp:google"
	for _, info := range s.dispatcher.Servers() {
		live[info.Kind+":"+info.Name] = info
	}

	conns := []rpc.Connection{builtinConnection(cfg)}
	for _, srv := range cfg.MCP.Servers {
		c := rpc.Connection{Name: srv.Name, Kind: dispatch.KindMCP, Transport: "stdio", Remote: srv.Remote}
		if srv.URL != "" {
			c.Transport, c.URL = "http", srv.URL
		}
		fillPolicies(&c, live[dispatch.KindMCP+":"+srv.Name], srv.Name+".", srv.Allow, srv.Confirm, srv.AlwaysConfirm)
		conns = append(conns, c)
	}
	for _, ag := range cfg.A2A.Agents {
		c := rpc.Connection{Name: ag.Name, Kind: dispatch.KindA2A, Transport: "http", URL: ag.URL, Remote: ag.Remote}
		fillPolicies(&c, live[dispatch.KindA2A+":"+ag.Name], "a2a."+ag.Name+".", ag.Allow, ag.Confirm, nil)
		conns = append(conns, c)
	}
	if len(cfg.Commands) > 0 {
		c := rpc.Connection{Name: "commands", Kind: dispatch.KindCommand, State: rpc.MCPConnected,
			Offered: len(cfg.Commands), Fixed: true, Note: commandsNote}
		for _, cmd := range cfg.Commands {
			p := rpc.PolicyAllow
			if cmd.Confirm {
				p = rpc.PolicyAsk
			}
			c.Tools = append(c.Tools, rpc.ToolPolicy{Name: cmd.Name, Description: cmd.Description, Policy: p})
		}
		conns = append(conns, c)
	}
	return rpc.Event{Type: rpc.EventConnections, Connections: conns, Catalog: catalogEntries(cfg, sec)}, nil
}

// builtinConnection lists merud's own tools, every one of the ten, with
// its policy from [builtin] tools and confirm. configure always asks.
// Note says why web_search does nothing yet when [web] searxng_url is
// empty.
func builtinConnection(cfg config.Config) rpc.Connection {
	c := rpc.Connection{Name: "meru", Kind: dispatch.KindBuiltin, State: rpc.MCPConnected}
	for _, name := range config.BuiltinTools() {
		p := policyOf(name, cfg.Builtin.Tools, cfg.Builtin.Confirm, nil)
		if name == builtin.Configure && p != rpc.PolicyOff {
			p = rpc.PolicyAlways
		}
		c.Tools = append(c.Tools, rpc.ToolPolicy{Name: name, Description: builtin.Summary(name), Policy: p})
	}
	c.Offered = len(c.Tools)
	if cfg.Web.SearXNGURL == "" {
		c.Note = "web_search needs a SearXNG instance on this machine: set [web] searxng_url in config.toml, or run meru setup."
	}
	return c
}

// fillPolicies fills in c's state and tools from info, what the backend
// reports, and the source's lists from config. The tools are the ones the
// source offers plus any config allows that it doesn't (marked Missing
// when the source is connected), allowed ones first, each group by name.
// prefix is what the full tool names start with, such as "google.".
func fillPolicies(c *rpc.Connection, info rpc.ServerInfo, prefix string, allow, confirm, always []string) {
	c.State, c.Offered, c.Err = rpc.MCPNotConnected, -1, info.LastError
	if info.Connected {
		c.State, c.Offered, c.Err = rpc.MCPConnected, info.Offered, ""
	}
	desc := map[string]string{}
	for _, t := range info.OfferedTools {
		desc[strings.TrimPrefix(t.Name, prefix)] = t.Description
	}
	names := slices.Collect(maps.Keys(desc))
	for _, a := range allow {
		if !slices.Contains(names, a) {
			names = append(names, a)
		}
	}
	for _, n := range names {
		_, offered := desc[n]
		c.Tools = append(c.Tools, rpc.ToolPolicy{
			Name: n, Description: desc[n], Policy: policyOf(n, allow, confirm, always),
			Missing: info.Connected && !offered,
		})
	}
	// Allowed tools first, then by name, so the tools that are on lead the
	// card and the list reads the same each time.
	slices.SortFunc(c.Tools, func(a, b rpc.ToolPolicy) int {
		aOff, bOff := a.Policy == rpc.PolicyOff, b.Policy == rpc.PolicyOff
		if aOff != bOff {
			if aOff {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// policyOf maps config's lists onto one tool's policy: off when allow
// leaves it out, always when always_confirm names it, ask when confirm
// does, and allow otherwise.
func policyOf(tool string, allow, confirm, always []string) string {
	switch {
	case !slices.Contains(allow, tool):
		return rpc.PolicyOff
	case slices.Contains(always, tool):
		return rpc.PolicyAlways
	case slices.Contains(confirm, tool):
		return rpc.PolicyAsk
	}
	return rpc.PolicyAllow
}

// catalogEntries turns the catalog into the protocol's shape, marking the
// entries config already has and the keys secrets.toml already holds.
func catalogEntries(cfg config.Config, sec *secrets.Secrets) []rpc.CatalogEntry {
	var out []rpc.CatalogEntry
	for _, e := range catalog.Entries() {
		ce := rpc.CatalogEntry{
			Name: e.Name, Title: e.Title, Description: e.Description, Transport: e.Transport,
			Command: strings.TrimSpace(e.Command + " " + strings.Join(e.Args, " ")), URL: e.URL, Start: e.Start,
			Requires: e.Requires, Install: e.Install, Docs: e.Docs, Allow: e.Allow, Confirm: e.Confirm,
		}
		for _, srv := range cfg.MCP.Servers {
			if srv.Name == e.Name {
				ce.Added = true
			}
		}
		for _, n := range e.Needs {
			switch n.Kind {
			case catalog.NeedAPIKey:
				ce.Needs = append(ce.Needs, rpc.CatalogNeed{Kind: n.Kind, Prompt: n.Prompt, Help: n.Help,
					Secret: n.SecretName, Saved: sec.Has(n.SecretName)})
			case catalog.NeedNote:
				ce.Needs = append(ce.Needs, rpc.CatalogNeed{Kind: n.Kind, Prompt: n.Prompt, Help: n.Help})
			}
		}
		out = append(out, ce)
	}
	return out
}

// handleToolPolicy answers OpToolPolicy: it checks the change against
// config and what the source offers, writes the new lists to config.toml,
// reloads that kind of source, and replies with the new "connections"
// event. It refuses a tool the source neither offers nor config names, a
// policy other than off, ask and allow, and allow for a tool that always
// asks. Deny-by-default holds: a tool the user never switched stays off.
func (s *toolService) handleToolPolicy(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	p := req.Policy
	if p == nil {
		return errors.New("tool_policy needs a policy change")
	}
	switch p.Policy {
	case rpc.PolicyOff, rpc.PolicyAsk, rpc.PolicyAllow:
	default:
		return fmt.Errorf("policy %q isn't one of off, ask and allow", p.Policy)
	}
	var err error
	switch p.Kind {
	case dispatch.KindMCP, dispatch.KindA2A:
		err = s.setEntryPolicy(ctx, *p)
	case dispatch.KindBuiltin:
		err = s.setBuiltinPolicy(*p)
	default:
		err = fmt.Errorf("the app can't change a tool of kind %q; edit config.toml", p.Kind)
	}
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "tool policy set", "kind", p.Kind, "server", p.Server, "tool", p.Tool, "policy", p.Policy)
	return s.handleConnections(emit)
}

// setEntryPolicy sets one MCP or A2A tool's policy in config.toml and
// reloads the MCP servers or the A2A agents.
func (s *toolService) setEntryPolicy(ctx context.Context, p rpc.PolicyChange) error {
	return s.bt.EditConfig(func() error {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return err
		}
		var allow, confirm, always []string
		table, prefix, found := catalog.TableMCP, p.Server+".", false
		if p.Kind == dispatch.KindA2A {
			table, prefix = catalog.TableA2A, "a2a."+p.Server+"."
			for _, a := range cfg.A2A.Agents {
				if a.Name == p.Server {
					allow, confirm, found = a.Allow, a.Confirm, true
				}
			}
		} else {
			for _, srv := range cfg.MCP.Servers {
				if srv.Name == p.Server {
					allow, confirm, always, found = srv.Allow, srv.Confirm, srv.AlwaysConfirm, true
				}
			}
		}
		if !found {
			return fmt.Errorf("config has no %s source named %q", p.Kind, p.Server)
		}
		if !slices.Contains(allow, p.Tool) && !s.offers(p.Kind, p.Server, prefix+p.Tool) {
			return fmt.Errorf("%s doesn't offer a tool named %q", p.Server, p.Tool)
		}
		if p.Policy == rpc.PolicyAllow && slices.Contains(always, p.Tool) {
			return fmt.Errorf("%s.%s always asks: config lists it in always_confirm, because it runs commands; "+
				"it can be on and ask, or off", p.Server, p.Tool)
		}
		lists := map[string][]string{}
		lists["allow"], lists["confirm"] = newLists(p.Tool, p.Policy, allow, confirm)
		if p.Policy == rpc.PolicyOff && slices.Contains(always, p.Tool) {
			// always_confirm must stay inside allow; the tool comes back
			// as ask when turned on again.
			lists["always_confirm"] = slices.DeleteFunc(slices.Clone(always), func(t string) bool { return t == p.Tool })
		}
		if slices.Contains(always, p.Tool) && p.Policy == rpc.PolicyAsk {
			// It asks every time already; confirm needn't name it.
			lists["confirm"] = slices.Clone(confirm)
		}
		if err := catalog.SetEntryLists(s.configPath, table, p.Server, lists); err != nil {
			return err
		}
		// The reload runs to the end even if the app hangs up, so merud
		// never holds half-built tools.
		if p.Kind == dispatch.KindA2A {
			return s.reloadA2A(context.WithoutCancel(ctx))
		}
		return s.reloadMCP(context.WithoutCancel(ctx))
	})
}

// setBuiltinPolicy sets one built-in tool's policy in [builtin] tools and
// confirm, and hands the built-in tools the new lists.
func (s *toolService) setBuiltinPolicy(p rpc.PolicyChange) error {
	if !slices.Contains(config.BuiltinTools(), p.Tool) {
		return fmt.Errorf("%q isn't a built-in tool; they are %s", p.Tool, strings.Join(config.BuiltinTools(), ", "))
	}
	if p.Tool == builtin.Configure && p.Policy == rpc.PolicyAllow {
		return errors.New("configure always asks: it changes config.toml, so the model can never grant itself a tool; it can be on and ask, or off")
	}
	return s.bt.EditConfig(func() error {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return err
		}
		tools, confirm := newLists(p.Tool, p.Policy, cfg.Builtin.Tools, cfg.Builtin.Confirm)
		if p.Tool == builtin.Configure {
			confirm = slices.DeleteFunc(confirm, func(t string) bool { return t == builtin.Configure })
		}
		check := func(next config.Config) error {
			if !slices.Equal(next.Builtin.Tools, tools) || !slices.Equal(next.Builtin.Confirm, confirm) {
				return errors.New("the [builtin] lists didn't come out as asked; edit config.toml by hand")
			}
			return nil
		}
		err = catalog.SetTableLists(s.configPath, "builtin", map[string][]string{"tools": tools, "confirm": confirm}, check)
		if err != nil {
			return err
		}
		return s.reloadBuiltin()
	})
}

// newLists returns allow and confirm with tool set to policy: off takes it
// out of both; ask puts it in both; allow puts it in allow and takes it
// out of confirm. An entry already in place keeps its place in the list,
// and a new one goes at the end, so the file changes as little as it can.
func newLists(tool, policy string, allow, confirm []string) (newAllow, newConfirm []string) {
	without := func(list []string) []string {
		return slices.DeleteFunc(slices.Clone(list), func(t string) bool { return t == tool })
	}
	with := func(list []string) []string {
		if slices.Contains(list, tool) {
			return slices.Clone(list)
		}
		return append(slices.Clone(list), tool)
	}
	switch policy {
	case rpc.PolicyAsk:
		return with(allow), with(confirm)
	case rpc.PolicyAllow:
		return with(allow), without(confirm)
	}
	return without(allow), without(confirm)
}

// offers reports whether the source of kind named server offers the tool
// with this full name, by its last listing.
func (s *toolService) offers(kind, server, full string) bool {
	for _, info := range s.dispatcher.Servers() {
		if info.Kind != kind || info.Name != server {
			continue
		}
		for _, t := range info.OfferedTools {
			if t.Name == full {
				return true
			}
		}
	}
	return false
}

// handleMCPAdd answers OpMCPAdd: the flow `meru mcp add <name>` runs, for a
// catalog server. The app has saved any API key the entry needs with
// OpSecretSet first, as `meru mcp add` saves it before it tries the
// server. handleMCPAdd appends the entry's block to config.toml with the
// catalog's allow and confirm lists, reloads the MCP servers and replies
// with the new "connections" event.
//
// It refuses a name the catalog doesn't have, a server config already
// has, and an entry whose key secrets.toml lacks. Unlike `meru mcp add`
// it doesn't try the server first: the catalog's lists name real tools,
// and a url server may not be running yet; the Library shows the server's
// state after the reload.
func (s *toolService) handleMCPAdd(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	e, ok := catalog.Find(req.ID)
	if !ok {
		return fmt.Errorf("%q isn't in the catalog; it has %s", req.ID, strings.Join(catalog.Names(), ", "))
	}
	err := s.bt.EditConfig(func() error {
		sec, err := secrets.Load(secrets.Path(s.dir))
		if err != nil {
			return err
		}
		for _, n := range e.Needs {
			if n.Kind == catalog.NeedAPIKey && !sec.Has(n.SecretName) {
				return fmt.Errorf("%s needs a key first: %s", e.Title, n.Prompt)
			}
		}
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return err
		}
		for _, srv := range cfg.MCP.Servers {
			if srv.Name == e.Name {
				return fmt.Errorf("config already has a server named %q", e.Name)
			}
		}
		if err := catalog.AppendServer(s.configPath, catalog.Block(e)); err != nil {
			return err
		}
		return s.reloadMCP(context.WithoutCancel(ctx))
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "mcp server added from the catalog", "server", e.Name)
	return s.handleConnections(emit)
}

// handleMCPRemove answers OpMCPRemove: it takes the server named req.ID
// out of config.toml, as `meru mcp remove` does, reloads the MCP servers,
// which stops the removed one, and replies with the new "connections"
// event. Its keys stay in secrets.toml, as they do after `meru mcp
// remove`.
func (s *toolService) handleMCPRemove(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	if req.ID == "" {
		return errors.New("mcp_remove needs a server name")
	}
	err := s.bt.EditConfig(func() error {
		if _, err := catalog.RemoveServer(s.configPath, req.ID); err != nil {
			return err
		}
		return s.reloadMCP(context.WithoutCancel(ctx))
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "mcp server removed", "server", req.ID)
	return s.handleConnections(emit)
}

// handleSecretSet answers OpSecretSet: it saves req.Text as the secret
// named req.ID, then reloads the MCP servers, which read their keys when
// they start. It accepts only a name that config.toml refers to with
// "secret:<name>" or that a catalog entry asks for, so a client can't use
// it to fill secrets.toml with anything else. The reply is "done" alone:
// merud never sends a secret back.
func (s *toolService) handleSecretSet(ctx context.Context, req rpc.Request) error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	if !slices.Contains(secretNames(cfg), req.ID) {
		return fmt.Errorf("no server uses a secret named %q", req.ID)
	}
	if strings.TrimSpace(req.Text) == "" {
		return errors.New("the key is empty")
	}
	err = s.bt.EditConfig(func() error {
		if err := secrets.Set(secrets.Path(s.dir), req.ID, req.Text); err != nil {
			return err
		}
		return s.reloadMCP(context.WithoutCancel(ctx))
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "secret saved", "secret", req.ID)
	return nil
}

// secretNames lists every secret name config.toml refers to, in an MCP
// server's env or headers or an agent's headers, and every one a catalog
// entry asks for.
func secretNames(cfg config.Config) []string {
	var names []string
	add := func(values map[string]string) {
		for _, v := range values {
			if name, ok := secrets.Name(v); ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	for _, srv := range cfg.MCP.Servers {
		add(srv.Env)
		add(srv.Headers)
	}
	for _, a := range cfg.A2A.Agents {
		add(a.Headers)
	}
	for _, e := range catalog.Entries() {
		for _, name := range e.SecretNames() {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}
