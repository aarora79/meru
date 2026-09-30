// This file holds what the connectors op carries: one ConnectorStatus per
// connector merud's supervisor knows, with its state, a sentence that
// says it, and the fields it asks the user for. The clients can't import
// internal/connectors, so the states are spelled out here too. See
// ARCHITECTURE.md, "The supervisor".

package rpc

import "strings"

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

// FixHint tells the user where to set the fields a connector needs, until
// the clients can ask for them: "Set vault_path under [connectors.obsidian]
// in config.toml, then restart merud." It returns "" when fix is empty.
func FixHint(id string, fix []string) string {
	if len(fix) == 0 {
		return ""
	}
	return "Set " + strings.Join(fix, " and ") + " under [connectors." + id + "] in config.toml, then restart merud."
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
