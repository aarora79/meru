// This file joins the connector supervisors to the rest of merud. merud
// builds one supervisor per stdio connector at startup, from the embedded
// manifests and the [connectors.<id>] tables, and keeps them for its whole
// life: a reload of the MCP servers hands them the new config but never
// replaces them, so a running connector survives it. The MCP pool reaches
// each one through its Spawn hook, and the connectors op reports them.
// See ARCHITECTURE.md, "The supervisor".

package main

import (
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/connectors"
	"github.com/aarora79/meru/internal/mcp"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// connectorSet holds merud's supervisors, in manifest order. The slice
// never changes after newConnectorSet; each supervisor guards its own
// state.
type connectorSet struct {
	sups []*connectors.Supervisor
}

// newConnectorSet loads the manifests and builds a supervisor for each
// stdio connector, installing under meruDir/runtime. Each stands off until
// configure hands it config (newPool does, at startup and on each
// reload). It fails when the manifests don't load, which only a broken
// build can cause, or when the home folder is unknown.
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
		// The supervisor runs stdio connectors so far; SearXNG, Ollama
		// and Google join in later steps of issue #87.
		if m.Kind != connectors.KindStdio {
			continue
		}
		sup, err := connectors.New(m, in, log)
		if err != nil {
			c.Close()
			return nil, err
		}
		c.sups = append(c.sups, sup)
	}
	return c, nil
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
}

// byHand reports whether servers has an entry named id.
func byHand(servers []config.MCPServer, id string) bool {
	return slices.ContainsFunc(servers, func(s config.MCPServer) bool { return s.Name == id })
}

// serverConfigs returns the pool entries for the connectors that are on
// and not set up by hand: one managed server each, named for the
// connector, so its tools stay <id>.<tool>, with the manifest's tool
// lists and its supervisor as the Spawn hook. A connector that is off
// gets no entry, so it stays out of mcp_status.
func (c *connectorSet) serverConfigs() []mcp.ServerConfig {
	var out []mcp.ServerConfig
	for _, sup := range c.sups {
		state, _ := sup.State()
		if state == connectors.StateOff || state == connectors.StateByHand {
			continue
		}
		m := sup.Manifest()
		out = append(out, mcp.ServerConfig{
			Name: m.ID, Spawn: sup,
			Allow: m.MCP.Allow, Confirm: m.MCP.Confirm, AlwaysConfirm: m.MCP.AlwaysConfirm,
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

// statuses reports every connector for the connectors op, in manifest
// order. A secret field's value never goes in; only whether it is saved.
func (c *connectorSet) statuses() []rpc.ConnectorStatus {
	out := make([]rpc.ConnectorStatus, 0, len(c.sups))
	for _, sup := range c.sups {
		st := sup.Status()
		row := rpc.ConnectorStatus{
			ID: st.ID, Name: st.Name, Kind: st.Kind, State: st.State, Sentence: st.Sentence,
			Required: st.Required, Fix: st.Fix,
			Fields: make([]rpc.ConnectorField, 0, len(st.Fields)), // [] rather than null in the JSON
		}
		for _, f := range st.Fields {
			row.Fields = append(row.Fields, rpc.ConnectorField{
				ID: f.ID, Type: f.Type, Label: f.Label, Help: f.Help, Required: f.Required,
				Pattern: f.Pattern, Default: f.Default, Choices: f.Choices, Value: f.Value, Saved: f.Saved,
			})
		}
		out = append(out, row)
	}
	return out
}

// byID returns the status of connector id, and false when merud runs no
// connector of that name.
func (c *connectorSet) byID(id string) (connectors.Status, bool) {
	for _, sup := range c.sups {
		if sup.Manifest().ID == id {
			return sup.Status(), true
		}
	}
	return connectors.Status{}, false
}

// Close stops every connector's program and waits for them.
func (c *connectorSet) Close() {
	for _, sup := range c.sups {
		sup.Close()
	}
}

// handleConnectors answers OpConnectors with one "connectors" event. It
// reads what each supervisor holds and starts nothing, so it answers at
// once while a connector installs or restarts.
func (s *toolService) handleConnectors(emit func(rpc.Event) error) error {
	return emit(rpc.Event{Type: rpc.EventConnectors, Connectors: s.conns.statuses()})
}
