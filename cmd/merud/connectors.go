// This file joins the connector supervisors to the rest of merud. merud
// builds one supervisor per stdio connector and one for the SearXNG
// container at startup, from the embedded manifests and the
// [connectors.<id>] tables, and keeps them for its whole life: a reload
// of the MCP servers hands them the new config but never replaces them,
// so a running connector survives it. The MCP pool reaches each stdio
// connector through its Spawn hook, web_search asks the SearXNG one
// whether it works, and the connectors op reports them all, Ollama
// included (ollama.go). See ARCHITECTURE.md, "The supervisor" and
// "SearXNG and Ollama".

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/mcp"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// connectorSet holds merud's supervisors: one per MCP connector, stdio
// (Obsidian) or http (Google), in manifest order, and web, the SearXNG
// container's. None of the fields changes after newConnectorSet; each
// supervisor guards its own state.
type connectorSet struct {
	sups []*connectors.Supervisor
	web  *connectors.Container
}

// newConnectorSet loads the manifests and builds a supervisor for each
// MCP connector and for the SearXNG container, installing under
// meruDir/runtime. Each stands off until configure hands it config
// (newPool does, at startup and on each reload). Ollama, the one
// dependency, has its own watcher (ollama.go). It fails when the
// manifests don't load, which only a broken build can cause, or when the
// home folder is unknown.
func newConnectorSet(meruDir string, log *slog.Logger) (*connectorSet, error) {
	manifests, err := connectors.Load()
	if err != nil {
		return nil, fmt.Errorf("connectors: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("connectors: find the home folder: %w", err)
	}
	in := connectors.NewInstaller(meruDir, home)
	c := &connectorSet{}
	for _, m := range manifests {
		// A switch with no value runs the first case that is true.
		switch {
		case m.Kind == connectors.KindStdio || m.Kind == connectors.KindHTTP:
			sup, err := connectors.New(m, in, log)
			if err != nil {
				c.Close()
				return nil, err
			}
			c.sups = append(c.sups, sup)
		case m.Kind == connectors.KindContainer && m.ID == "searxng":
			settings := filepath.Join(meruDir, "searxng")
			// prepare writes settings.yml, with JSON on and a new secret,
			// before Meru's container first starts; a file already there,
			// from the Mac installer or the user, stays as it is.
			prepare := func() error {
				_, err := catalog.WriteSearXNGSettings(settings)
				return err
			}
			web, err := connectors.NewContainer(m, in, checkSearXNG, prepare, log)
			if err != nil {
				c.Close()
				return nil, err
			}
			c.web = web
		}
	}
	if c.web == nil {
		c.Close()
		return nil, errors.New("connectors: no searxng manifest")
	}
	return c, nil
}

// checkSearXNG is the SearXNG connector's health check:
// catalog.CheckSearXNG, the empty search with format=json that asks no
// search engine. It says why a check failed in words that fit the
// connector's sentence, and wraps connectors.ErrNothingListens when
// nothing accepts the connection, which is the one case where Meru may
// start its own container at the URL.
func checkSearXNG(ctx context.Context, url string) error {
	err := catalog.CheckSearXNG(ctx, url)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, catalog.ErrSearXNGDown):
		return connectors.ErrNothingListens
	case errors.Is(err, catalog.ErrSearXNGNoJSON):
		return errors.New("it answers web pages, not JSON; add json under search: formats: in its settings.yml")
	}
	return err
}

// configure hands each supervisor its [connectors.<id>] table and the
// secrets, and says whether an [[mcp.servers]] entry has the connector's
// name. That entry wins: the user set the server up by hand, so the
// supervisor leaves it alone and reports "set up by hand". A connector
// that is on and set up starts installing in the background; nothing
// waits for it.
func (c *connectorSet) configure(cfg config.Config, sec *secrets.Secrets) {
	for _, sup := range c.sups {
		id := sup.Manifest().ID
		sup.Configure(cfg.Connectors[id], sec, byHand(cfg.MCP.Servers, id))
	}
	c.configureWeb(cfg)
}

// configureWeb hands the SearXNG connector its part of cfg: its table,
// [web] searxng_url, and whether [builtin] tools lists web_search, which
// together pick its mode (connectors.ContainerMode). A reload of the
// built-in tools' lists calls it too, since turning web_search on in
// Settings can turn the connector on.
func (c *connectorSet) configureWeb(cfg config.Config) {
	c.web.Configure(cfg.Connectors[c.web.Manifest().ID], cfg.Web.SearXNGURL, slices.Contains(cfg.Builtin.Tools, builtin.WebSearch))
}

// webOK reports whether web search works now, with the SearXNG
// connector's sentence. The built-in tools offer web_search only while it
// is true.
func (c *connectorSet) webOK() (bool, string) {
	return c.web.OK()
}

// byHand reports whether servers has an entry named id.
func byHand(servers []config.MCPServer, id string) bool {
	return slices.ContainsFunc(servers, func(s config.MCPServer) bool { return s.Name == id })
}

// serverConfigs returns the pool entries for the connectors that are on
// and not set up by hand: one managed server each, named for the
// connector, so its tools stay <id>.<tool>, with its tool lists (the
// manifest's, or those its [connectors.<id>] table sets, as Adopt writes
// them) and its supervisor as the Spawn hook. A connector that is off
// gets no entry, so it stays out of mcp_status.
func (c *connectorSet) serverConfigs() []mcp.ServerConfig {
	var out []mcp.ServerConfig
	for _, sup := range c.sups {
		state, _ := sup.State()
		if state == connectors.StateOff || state == connectors.StateByHand {
			continue
		}
		allow, confirm, always := sup.Lists()
		out = append(out, mcp.ServerConfig{
			Name: sup.Manifest().ID, Spawn: sup,
			Allow: allow, Confirm: confirm, AlwaysConfirm: always,
		})
	}
	return out
}

// routerServers returns cfg's [[mcp.servers]] with an entry added for each
// connector the pool runs, holding only its name and allow list. The
// router's prompt names what is connected from that list
// (agent.ConnectedTools), and a connector belongs in it like a server
// added by hand.
func (c *connectorSet) routerServers(cfg config.Config) config.Config {
	out := cfg
	out.MCP.Servers = slices.Clone(cfg.MCP.Servers)
	for _, sc := range c.serverConfigs() {
		out.MCP.Servers = append(out.MCP.Servers, config.MCPServer{Name: sc.Name, Allow: sc.Allow})
	}
	return out
}

// statuses reports every connector for the connectors op, sorted by ID,
// which is manifest order: the stdio ones, the SearXNG container, and
// extra, the rows merud builds elsewhere (Ollama's). A secret field's
// value never goes in; only whether it is saved.
func (c *connectorSet) statuses(extra ...connectors.Status) []rpc.ConnectorStatus {
	all := make([]connectors.Status, 0, len(c.sups)+1+len(extra))
	for _, sup := range c.sups {
		all = append(all, sup.Status())
	}
	all = append(all, c.web.Status())
	all = append(all, extra...)
	slices.SortFunc(all, func(a, b connectors.Status) int { return strings.Compare(a.ID, b.ID) })
	out := make([]rpc.ConnectorStatus, 0, len(all))
	for _, st := range all {
		out = append(out, connectorRow(st))
	}
	return out
}

// connectorRow turns one connector's status into the protocol's shape.
func connectorRow(st connectors.Status) rpc.ConnectorStatus {
	row := rpc.ConnectorStatus{
		ID: st.ID, Name: st.Name, Kind: st.Kind, State: st.State, Sentence: st.Sentence,
		Required: st.Required, Fix: st.Fix, Link: st.Link,
		Fields: make([]rpc.ConnectorField, 0, len(st.Fields)), // [] rather than null in the JSON
	}
	for _, f := range st.Fields {
		row.Fields = append(row.Fields, rpc.ConnectorField{
			ID: f.ID, Type: f.Type, Label: f.Label, Help: f.Help, Required: f.Required,
			Pattern: f.Pattern, Default: f.Default, Choices: f.Choices, Value: f.Value, Saved: f.Saved,
		})
	}
	return row
}

// byID returns the status of MCP connector id, and false when merud runs
// no MCP connector of that name.
func (c *connectorSet) byID(id string) (connectors.Status, bool) {
	for _, sup := range c.sups {
		if sup.Manifest().ID == id {
			return sup.Status(), true
		}
	}
	return connectors.Status{}, false
}

// manifest returns the manifest of MCP connector id, and false when merud
// runs no MCP connector of that name.
func (c *connectorSet) manifest(id string) (connectors.Manifest, bool) {
	for _, sup := range c.sups {
		if sup.Manifest().ID == id {
			return sup.Manifest(), true
		}
	}
	return connectors.Manifest{}, false
}

// Close stops every connector's program and the SearXNG connector's
// checks, and waits for them. Meru's SearXNG container keeps running, so
// the next merud uses it at once.
func (c *connectorSet) Close() {
	for _, sup := range c.sups {
		sup.Close()
	}
	if c.web != nil {
		c.web.Close()
	}
}

// handleConnectors answers OpConnectors with one "connectors" event,
// Ollama's row included. It reads what each supervisor holds and starts
// nothing, so it answers at once while a connector installs or restarts.
func (s *toolService) handleConnectors(ollama *ollamaWatch, emit func(rpc.Event) error) error {
	return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: s.conns.statuses(ollama.status())})
}

// adopter returns the Adopter for this merud: its config.toml and
// secrets.toml, the user's home folder, and launchctl run through the
// connectors package's one exec site.
func (s *toolService) adopter() (*connectors.Adopter, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home folder: %w", err)
	}
	return &connectors.Adopter{
		ConfigPath: s.configPath, SecretsPath: secrets.Path(s.dir), Home: home,
		Run: connectors.ExecRunner(), UID: os.Getuid(), OS: runtime.GOOS, Now: time.Now,
	}, nil
}

// handleAdopt answers OpConnectorAdopt and, with undo, OpConnectorUnadopt.
// It works out the changes for the connector named req.ID; without
// req.Adopt.Apply it only sends them, so the client can ask the user
// first. With Apply it makes them and reloads the MCP servers, all while
// it holds the lock every config.toml write takes. The reply is one
// "adopt" event. A secret in req.Adopt.Values goes to secrets.toml only;
// the event never holds it.
func (s *toolService) handleAdopt(ctx context.Context, req rpc.Request, undo bool, emit func(rpc.Event) error) error {
	m, ok := s.conns.manifest(req.ID)
	if !ok || !connectors.Adoptable(m) {
		return fmt.Errorf("meru mcp adopt takes obsidian or google, not %q", req.ID)
	}
	a, err := s.adopter()
	if err != nil {
		return err
	}
	var in rpc.AdoptRequest
	if req.Adopt != nil {
		in = *req.Adopt
	}
	// A reload here must outlive a client that hangs up halfway, as
	// handleSecretSet's does.
	reload := func() error { return s.reloadMCP(context.WithoutCancel(ctx)) }

	var plan connectors.AdoptPlan
	err = s.bt.EditConfig(func() error {
		var err error
		if undo {
			plan, err = a.PlanUnadopt(ctx, m)
		} else {
			plan, err = a.PlanAdopt(ctx, m, in.Values)
		}
		if err != nil || !in.Apply {
			return err
		}
		if undo {
			return a.Unadopt(ctx, plan, reload)
		}
		return a.Adopt(ctx, plan, reload)
	})
	if err != nil {
		return err
	}
	if in.Apply && !plan.Nothing {
		verb := "adopted"
		if undo {
			verb = "unadopted"
		}
		s.log.InfoContext(ctx, "connector "+verb, "connector", m.ID)
	}
	return emit(rpc.Event{Type: rpc.EventAdopt, Adopted: &rpc.AdoptResult{
		ID: m.ID, Changes: plan.Changes, Applied: in.Apply && !plan.Nothing, Nothing: plan.Nothing,
	}})
}
