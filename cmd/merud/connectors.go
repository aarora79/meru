// This file joins the connector supervisors to the rest of merud. merud
// builds one supervisor per stdio connector and one for the SearXNG
// container at startup, from the embedded manifests and the
// [connectors.<id>] tables, and keeps them for its whole life: a reload
// of the MCP servers hands them the new config but never replaces them,
// so a running connector survives it. The MCP pool reaches each stdio
// connector through its Spawn hook, web_search asks the SearXNG one
// whether it works, and the connectors op reports them all, Ollama
// included (ollama.go). The clients' settings forms and Fix button reach
// them through connector_set and connector_fix, and Adopt through
// connector_adopt. See ARCHITECTURE.md, "The supervisor", "SearXNG and
// Ollama" and "Setting up a connector".

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
	// all holds every manifest, Ollama's included, for the ops that name
	// a connector.
	all []connectors.Manifest
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
	c := &connectorSet{all: manifests}
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

// settleWait bounds how long connector_set and connector_fix follow a
// connector that is still starting. A first install of Google downloads
// a Python and a package, which can take minutes on a slow line; past
// this the reply ends and the install goes on, which the connectors op
// then shows.
const settleWait = 5 * time.Minute

// followEvery is how often the reply looks at the connector while it
// follows it. A look reads what the supervisor holds and asks nothing of
// the connector.
const followEvery = 250 * time.Millisecond

// find returns the manifest of connector id, whatever its kind, and false
// when merud has none of that name.
func (c *connectorSet) find(id string) (connectors.Manifest, bool) {
	for _, m := range c.all {
		if m.ID == id {
			return m, true
		}
	}
	return connectors.Manifest{}, false
}

// row returns connector id's status in the protocol's shape: from its
// supervisor, the SearXNG one, or ollama's watch. ok is false for an ID
// merud doesn't run.
func (c *connectorSet) row(id string, ollama *ollamaWatch) (rpc.ConnectorStatus, bool) {
	switch {
	case c.web != nil && c.web.Manifest().ID == id:
		return connectorRow(c.web.Status()), true
	case ollama != nil && id == "ollama":
		return connectorRow(ollama.status()), true
	}
	st, ok := c.byID(id)
	if !ok {
		return rpc.ConnectorStatus{}, false
	}
	return connectorRow(st), true
}

// recheck runs connector id's check again (Fix's second half): a stdio
// or http connector installs and checks again, and the SearXNG one looks
// at its URL at once. It does nothing for an ID it doesn't run.
func (c *connectorSet) recheck(id string) {
	if c.web != nil && c.web.Manifest().ID == id {
		c.web.Recheck()
		return
	}
	for _, sup := range c.sups {
		if sup.Manifest().ID == id {
			sup.Recheck()
		}
	}
}

// handleConnectorSet answers OpConnectorSet. It checks req.Connector
// against the connector's manifest and the config and secrets as they
// stand (connectors.CheckChange), and refuses a connector set up by hand,
// which Adopt moves over first. Then, holding the lock every config.toml
// write takes, it saves each secret in secrets.toml as
// connector_<id>_<field> with mode 0600, writes [connectors.<id>] through
// the catalog's checked writer, and reloads, which hands the supervisor
// its new table. Last it follows the connector until it settles (see
// follow). A refused change writes nothing. No event ever holds a
// secret: the status carries only whether each one is saved.
func (s *toolService) handleConnectorSet(ctx context.Context, req rpc.Request, ollama *ollamaWatch, emit func(rpc.Event) error) error {
	if req.Connector == nil {
		return errors.New("connector_set needs a change")
	}
	m, ok := s.conns.find(req.ID)
	if !ok {
		return fmt.Errorf("merud has no connector called %q", req.ID)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find the home folder: %w", err)
	}
	ch := connectors.Change{Enabled: req.Connector.Enabled, Values: req.Connector.Values, Secrets: req.Connector.Secrets}
	err = s.bt.EditConfig(func() error {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return err
		}
		if byHand(cfg.MCP.Servers, m.ID) {
			return fmt.Errorf("%s is set up by hand, as the %s entry in [[mcp.servers]]; adopt it first (meru mcp adopt %s, or Adopt in Settings)", m.Name, m.ID, m.ID)
		}
		sec, err := secrets.Load(secrets.Path(s.dir))
		if err != nil {
			return err
		}
		if err := connectors.CheckChange(m, cfg.Connectors[m.ID], sec, home, ch); err != nil {
			return err
		}
		// Sorted, so the writes happen in the same order each time.
		names := make([]string, 0, len(ch.Secrets))
		for k := range ch.Secrets {
			names = append(names, k)
		}
		slices.Sort(names)
		for _, k := range names {
			if err := secrets.Set(secrets.Path(s.dir), connectors.SecretName(m.ID, k), ch.Secrets[k]); err != nil {
				return err
			}
		}
		if err := catalog.SetConnector(s.configPath, m.ID, ch.Enabled, ch.Values); err != nil {
			return err
		}
		// The reload must outlive a client that hangs up halfway, as
		// handleSecretSet's does.
		return s.reloadMCP(context.WithoutCancel(ctx))
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "connector set", "connector", m.ID, "values", len(ch.Values), "secrets", len(ch.Secrets))
	return s.follow(ctx, m.ID, ollama, emit)
}

// handleConnectorFix answers OpConnectorFix, the Fix button. A connector
// that needs fields gets one "connector" event whose Fix names them, and
// the client asks the user for those and sends them with
// OpConnectorSet. One that is off or set up by hand gets its status,
// whose sentence says what to do. Any other gets its check again
// (recheck; for Ollama, a check now), and the reply follows it until it
// settles.
func (s *toolService) handleConnectorFix(ctx context.Context, req rpc.Request, ollama *ollamaWatch, emit func(rpc.Event) error) error {
	row, ok := s.conns.row(req.ID, ollama)
	if !ok {
		return fmt.Errorf("merud has no connector called %q", req.ID)
	}
	switch {
	case row.State == rpc.ConnectorNeedsConfig && len(row.Fix) > 0,
		row.State == rpc.ConnectorOff, row.State == rpc.ConnectorByHand:
		return emit(rpc.Event{Type: rpc.EventConnector, Connector: &row})
	case req.ID == "ollama":
		ollama.recheck(ctx)
	default:
		s.conns.recheck(req.ID)
	}
	s.log.InfoContext(ctx, "connector fix", "connector", req.ID)
	return s.follow(ctx, req.ID, ollama, emit)
}

// follow sends connector id's status as a "connector" event now, and
// again each time its state, sentence or sign-in link changes, until it
// is no longer starting, settleWait passes, or the client hangs up. The
// last event says where it settled: ok, needs config (with the fields to
// ask, or a sign-in link), failed, or off.
func (s *toolService) follow(ctx context.Context, id string, ollama *ollamaWatch, emit func(rpc.Event) error) error {
	deadline := time.Now().Add(settleWait)
	// A Ticker sends on its channel C every followEvery until Stop.
	tick := time.NewTicker(followEvery)
	defer tick.Stop()
	var last *rpc.ConnectorStatus
	for {
		row, ok := s.conns.row(id, ollama)
		if !ok {
			return fmt.Errorf("merud has no connector called %q", id)
		}
		if last == nil || row.State != last.State || row.Sentence != last.Sentence || row.Link != last.Link {
			if err := emit(rpc.Event{Type: rpc.EventConnector, Connector: &row}); err != nil {
				return err
			}
			last = &row
		}
		if row.State != rpc.ConnectorStarting || time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
