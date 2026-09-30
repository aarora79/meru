// This file holds what the connectors op carries: one ConnectorStatus per
// connector merud's supervisor knows, with its state, a sentence that
// says it, and the fields it asks the user for. The clients can't import
// internal/connectors, so the states are spelled out here too. See
// ARCHITECTURE.md, "The supervisor".

package rpc

import (
	"slices"
	"strings"
)

// The states a connector reports, in ConnectorStatus.State and
// MCPStatus.Connector. internal/connectors defines the same words.
const (
	// ConnectorOK: running, or installed and checked, and it starts when
	// a question needs it. Its tools are offered.
	ConnectorOK = "ok"
	// ConnectorOff: config turns it off, or never turned it on.
	ConnectorOff = "off"
	// ConnectorNeedsConfig: a field is missing or wrong; Fix names it.
	ConnectorNeedsConfig = "needs_config"
	// ConnectorStarting: installing, starting, or waiting to start again
	// after a crash.
	ConnectorStarting = "starting"
	// ConnectorFailed: stopped until config changes or merud restarts;
	// the sentence says why.
	ConnectorFailed = "failed"
	// ConnectorByHand: an [[mcp.servers]] entry with the same name runs
	// it instead, and the supervisor leaves it alone.
	ConnectorByHand = "by_hand"
)

// ConnectorStatus is one connector, as OpConnectors reports it.
type ConnectorStatus struct {
	// ID names it in config ([connectors.<id>]) and in tool names
	// (<id>.<tool>); Name is what the user sees, such as "Obsidian".
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is "stdio", "http", "container" or "dependency".
	Kind string `json:"kind"`
	// State is one of the Connector states above, and Sentence says it in
	// one line, such as "Obsidian needs your vault folder."
	State    string `json:"state"`
	Sentence string `json:"sentence"`
	// Required is true for a connector Meru can't answer without.
	Required bool `json:"required,omitempty"`
	// Fields are what the connector asks the user, in order, with what
	// config holds now.
	Fields []ConnectorField `json:"fields"`
	// Fix lists the IDs of the fields to ask again when State is
	// ConnectorNeedsConfig.
	Fix []string `json:"fix,omitempty"`
	// Link is the sign-in link a connector's server gave, while the
	// connector waits for the user to sign in ("Google needs you to sign
	// in."). A client shows it for the user to open.
	Link string `json:"link,omitempty"`
}

// AdoptRequest says how OpConnectorAdopt or OpConnectorUnadopt runs.
type AdoptRequest struct {
	// Apply makes the changes; without it merud only works them out, for
	// the client to show the user before it asks.
	Apply bool `json:"apply,omitempty"`
	// Values gives a connector's field values the old entry doesn't
	// hold, by field ID, such as Google's email, client_id and
	// client_secret. A secret goes to secrets.toml, never to config.toml,
	// and merud never sends it back.
	Values map[string]string `json:"values,omitempty"`
}

// AdoptResult is what an adopt or unadopt changes, or would change.
type AdoptResult struct {
	ID string `json:"id"`
	// Changes says each change in one line, in order. A line may hold a
	// table of config lines after a line break.
	Changes []string `json:"changes"`
	// Applied is true once merud has made the changes.
	Applied bool `json:"applied,omitempty"`
	// Nothing is true when there is nothing to change; Changes says why.
	Nothing bool `json:"nothing,omitempty"`
}

// ConnectorField is one thing a connector asks the user for, from its
// manifest, with its value. A secret's value never leaves merud: Value is
// empty and Saved says whether secrets.toml holds it.
type ConnectorField struct {
	ID string `json:"id"`
	// Type is "text", "folder", "secret", "email", "choice" or "oauth".
	Type     string   `json:"type"`
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Required bool     `json:"required,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
	Default  string   `json:"default,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Value    string   `json:"value,omitempty"`
	Saved    bool     `json:"saved,omitempty"`
}

// ConnectorChange is what OpConnectorSet changes in one connector.
// Enabled turns it on or off; nil leaves that as it is. Values holds new
// field values by field ID, such as {"vault_path": "~/Notes"}; an empty
// value clears the field. Secrets holds new values for secret fields, by
// field ID: merud saves each in secrets.toml as
// connector_<id>_<field>, never in config.toml, and never sends one back.
type ConnectorChange struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Values  map[string]string `json:"values,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

// FieldTypes returns every type a ConnectorField can have, in the order
// the manifests document them. Each client draws an input for each one;
// the tests hold the connectors package and the clients to this list.
func FieldTypes() []string {
	return []string{"text", "folder", "secret", "email", "choice", "oauth"}
}

// AskFields returns the fields a client asks the user for after Fix
// (OpConnectorFix) answers with c: the ones c.Fix names; for a connector
// that is off, every field but an oauth one, whose server signs the user
// in, since turning it on needs them; and none otherwise, when Fix runs
// the check again and asks nothing. Every client asks the same fields in
// the same order.
func AskFields(c ConnectorStatus) []ConnectorField {
	var out []ConnectorField
	for _, f := range c.Fields {
		switch {
		case f.Type == "oauth":
		case c.State == ConnectorNeedsConfig && slices.Contains(c.Fix, f.ID):
			out = append(out, f)
		case c.State == ConnectorOff:
			out = append(out, f)
		}
	}
	return out
}

// FixHint tells a terminal user how to give the fields a connector needs:
// "Run meru mcp fix obsidian to set vault_path." It returns "" when fix is
// empty.
func FixHint(id string, fix []string) string {
	if len(fix) == 0 {
		return ""
	}
	return "Run meru mcp fix " + id + " to set " + strings.Join(fix, " and ") + "."
}

// ConnectorWords says a connector state the way the clients show it:
// "ok", "off", "needs config", "starting", "failed" or "set up by hand".
func ConnectorWords(state string) string {
	switch state {
	case ConnectorNeedsConfig:
		return "needs config"
	case ConnectorByHand:
		return "set up by hand"
	}
	return state
}
