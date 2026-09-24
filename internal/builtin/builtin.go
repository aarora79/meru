// This file holds Tools, the dispatch.Backend for merud's built-in tools,
// and the configure tool.

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/secrets"
)

// Configure is the configure tool's name, as the model sees it.
const Configure = "configure"

// server is what Locate and Status report as the server for every
// built-in, as ARCHITECTURE.md "Approving a tool call" says.
const server = "meru"

// actionAddServer is the one action configure knows in v0.3.
const actionAddServer = "add_mcp_server"

// Tools is merud's set of built-in tools. Build it with New.
type Tools struct {
	configPath string
	confirm    []string // [builtin] confirm from config.toml
	onChange   func(context.Context) error

	// mu makes one configure call finish its write before the next starts
	// reading config.toml, so two calls can't both pass the duplicate check.
	mu sync.Mutex
}

// New returns the built-in tools. configPath is config.toml; secrets.toml
// sits next to it. cfg is the [builtin] section. onChange runs after
// configure writes config.toml; merud passes a function that rebuilds the
// MCP pool. A nil onChange does nothing.
func New(configPath string, cfg config.Builtin, onChange func(context.Context) error) *Tools {
	return &Tools{
		configPath: configPath,
		confirm:    slices.Clone(cfg.Confirm),
		onChange:   onChange,
	}
}

// Kind returns dispatch.KindBuiltin.
func (t *Tools) Kind() string { return dispatch.KindBuiltin }

// Tools returns the configure tool's spec for the model.
func (t *Tools) Tools() []engine.ToolSpec {
	return []engine.ToolSpec{{
		Name:        Configure,
		Description: description(),
		Parameters:  schema(),
	}}
}

// Confirm says configure always asks, with no session approval. Any other
// built-in asks when [builtin] confirm lists it, and runs without asking
// otherwise.
func (t *Tools) Confirm(name string) dispatch.Confirm {
	switch {
	case name == Configure:
		return dispatch.ConfirmAlways
	case slices.Contains(t.confirm, name):
		return dispatch.ConfirmAsk
	default:
		return dispatch.ConfirmNever
	}
}

// Locate returns ("meru", name): a built-in belongs to no server, so rows
// and metrics name merud itself.
func (t *Tools) Locate(name string) (string, string) { return server, name }

// Status describes the built-ins for `meru tools`: always connected, one
// tool that always asks.
func (t *Tools) Status() []rpc.ServerInfo {
	return []rpc.ServerInfo{{
		Name:      server,
		Kind:      dispatch.KindBuiltin,
		Connected: true,
		Tools: []rpc.ToolInfo{{
			Name:        Configure,
			Description: "Adds an MCP server to config.toml.",
			Confirm:     true,
			AlwaysAsks:  true,
		}},
		Offered: 1,
	}}
}

// Call runs the named built-in with args, a JSON object. A request the tool
// refuses, such as bad arguments or a missing API key, comes back as a
// Result with IsError set, so the model can read why and tell the user. The
// error return is for a name that isn't a built-in.
func (t *Tools) Call(ctx context.Context, name string, args json.RawMessage) (dispatch.Result, error) {
	if name != Configure {
		return dispatch.Result{}, fmt.Errorf("%q is not a built-in tool", name)
	}
	text, err := t.configure(ctx, args)
	if err != nil {
		return dispatch.Result{Text: err.Error(), IsError: true}, nil
	}
	return dispatch.Result{Text: text}, nil
}

// configureArgs is the JSON object the model sends to configure. The
// `json:"..."` struct tags name the JSON keys.
type configureArgs struct {
	Action  string   `json:"action"`
	Catalog string   `json:"catalog"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	URL     string   `json:"url"`
}

// configure checks args, builds the entry and appends it to config.toml. It
// returns the text for the model on success, and an error whose text the
// model reads on refusal.
func (t *Tools) configure(ctx context.Context, raw json.RawMessage) (string, error) {
	var a configureArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	// An unknown key is most likely a mistake by the model; refusing it
	// beats writing config from arguments it didn't mean.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("configure: the arguments aren't a valid JSON object for this tool: %v", err)
	}
	if a.Action != actionAddServer {
		return "", fmt.Errorf("configure: action %q is unknown; use %q", a.Action, actionAddServer)
	}
	e, err := entryFor(a)
	if err != nil {
		return "", err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if missing, err := t.missingSecrets(e); err != nil {
		return "", err
	} else if len(missing) > 0 {
		// Keys never pass through the model, so the user adds them in a
		// terminal, where meru reads them without echo.
		return "", fmt.Errorf("configure: %s needs %s, and Meru never takes keys in chat. "+
			"Ask the user to run `meru mcp add %s` in a terminal; it asks for the key and adds the server. Nothing was written",
			e.Title, strings.Join(missing, " and "), e.Name)
	}

	if err := catalog.AppendServer(t.configPath, catalog.Block(e)); err != nil {
		return "", fmt.Errorf("configure: %w. Nothing was written", err)
	}

	msg := summary(e, t.configPath)
	if t.onChange != nil {
		if err := t.onChange(ctx); err != nil {
			return "", fmt.Errorf("configure: added %q to %s, but merud couldn't reload its MCP servers: %v. "+
				"Ask the user to restart merud", e.Name, t.configPath, err)
		}
	}
	return msg, nil
}

// entryFor builds the entry that args describe: a catalog entry by name, or
// a custom entry from a name and a command or URL. It fails when the
// arguments mix the two forms or leave out what a form needs.
func entryFor(a configureArgs) (catalog.Entry, error) {
	custom := a.Name != "" || a.Command != "" || a.URL != "" || len(a.Args) > 0
	switch {
	case a.Catalog != "" && custom:
		return catalog.Entry{}, errors.New("configure: give either catalog, or name with command or url, not both")
	case a.Catalog != "":
		e, ok := catalog.Find(a.Catalog)
		if !ok {
			return catalog.Entry{}, fmt.Errorf("configure: %q is not in the catalog; the catalog has %s",
				a.Catalog, strings.Join(catalog.Names(), ", "))
		}
		return e, nil
	case a.Name == "":
		return catalog.Entry{}, errors.New("configure: give catalog, or name with command or url")
	}
	if err := catalog.CheckName(a.Name); err != nil {
		return catalog.Entry{}, fmt.Errorf("configure: %w", err)
	}
	switch {
	case (a.Command == "") == (a.URL == ""):
		return catalog.Entry{}, errors.New("configure: give exactly one of command and url")
	case a.URL != "" && len(a.Args) > 0:
		return catalog.Entry{}, errors.New("configure: args go with command, not url")
	case a.URL != "" && !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://"):
		return catalog.Entry{}, fmt.Errorf("configure: url %q must start with http:// or https://", a.URL)
	case a.URL != "":
		return catalog.Custom(a.Name, a.URL, nil), nil
	case strings.HasPrefix(a.Command, "http://") || strings.HasPrefix(a.Command, "https://"):
		return catalog.Entry{}, errors.New("configure: that command is a URL; pass it as url")
	}
	return catalog.Custom(a.Name, a.Command, a.Args), nil
}

// missingSecrets returns the secrets e refers to that secrets.toml lacks.
// It fails when secrets.toml can't be read, for example when other users
// can read it.
func (t *Tools) missingSecrets(e catalog.Entry) ([]string, error) {
	s, err := secrets.Load(secrets.Path(filepath.Dir(t.configPath)))
	if err != nil {
		return nil, fmt.Errorf("configure: %w", err)
	}
	var missing []string
	for _, name := range e.SecretNames() {
		if !s.Has(name) {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// summary says what configure added: which tools the model may use, which
// ask first, what the user still has to do, and how to see the tools.
func summary(e catalog.Entry, configPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Added the MCP server %q (%s) to %s.\n", e.Name, e.Title, configPath)
	if len(e.Allow) == 0 {
		b.WriteString("It allows no tools yet, because Meru doesn't know this server's tool names. " +
			"Tell the user to run `meru tools` to see what it offers, then list the tools to allow in config.toml.\n")
	} else {
		fmt.Fprintf(&b, "Allowed tools: %s.\n", strings.Join(e.Allow, ", "))
		if len(e.Confirm) > 0 {
			fmt.Fprintf(&b, "These ask the user before each call: %s.\n", strings.Join(e.Confirm, ", "))
		}
	}
	for _, n := range e.Needs {
		if n.Kind == catalog.NeedNote {
			b.WriteString(n.Prompt + "\n")
		}
	}
	if e.Install != "" {
		b.WriteString(e.Install + "\n")
	}
	return b.String()
}

// description is the configure tool's description for the model. It names
// the catalog entries, so the model can pick one without guessing.
func description() string {
	var names []string
	for _, e := range catalog.Entries() {
		names = append(names, fmt.Sprintf("%s (%s)", e.Name, e.Title))
	}
	return "Adds an MCP server to ~/.meru/config.toml, which gives you new tools. " +
		"The user must approve every call. " +
		"For a known server, pass action \"add_mcp_server\" and catalog, one of: " + strings.Join(names, ", ") + ". " +
		"For another server, pass action, name, and either command with args or url. " +
		"API keys never go through this tool: if a server needs one, the result says which terminal command the user runs."
}

// schema returns the JSON Schema for configure's arguments. json.Marshal
// turns the Go map into JSON. It can't fail on these plain values, so
// schema drops the error.
func schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{actionAddServer},
				"description": "What to do. Only add_mcp_server exists.",
			},
			"catalog": map[string]any{
				"type":        "string",
				"enum":        catalog.Names(),
				"description": "A known server to add. Leave out name, command, args and url when you set this.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "For a server outside the catalog: its name, letters, digits, - and _ only.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "For a server outside the catalog: the program that starts it.",
			},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments for command.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "For a server outside the catalog that already runs: its Streamable HTTP URL.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(s)
	return b
}
