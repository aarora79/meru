// This file holds Tools, the dispatch.Backend for merud's built-in tools,
// and the configure tool. The remember tool lives in remember.go,
// write_file in writefile.go, and read_file, list_folder and grep in
// files.go.

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/memory"
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
	confirm    []string       // [builtin] confirm from config.toml
	memory     *memory.Store  // where remember saves; nil leaves remember out
	outputDir  string         // where write_file writes, absolute; "" leaves write_file out
	files      *index.Indexer // what the file tools read through; nil leaves them out
	onChange   func(context.Context) error
	onRemember func(context.Context) // runs after remember saves; nil for none

	// mu makes one configure call finish its write before the next starts
	// reading config.toml, so two calls can't both pass the duplicate check.
	mu sync.Mutex
}

// New returns the built-in tools. configPath is config.toml; secrets.toml
// sits next to it. cfg is the [builtin] section. mem is the memory folder
// that remember saves to; a nil mem leaves remember out, for tests of
// configure alone. outputDir is the absolute folder write_file writes in,
// [skills] output_dir with "~" expanded; "" leaves write_file out. onChange
// runs after configure writes config.toml; merud passes a function that
// rebuilds the MCP pool. onRemember runs after remember saves a memory;
// merud passes a function that syncs the memory folder into the store, so
// the next turn can recall the new fact. A nil onChange or onRemember does
// nothing. files is merud's indexer, which read_file, list_folder and grep
// read through, so they see the [index] folders with the indexer's skip
// rules; a nil files leaves the three out.
func New(configPath string, cfg config.Builtin, mem *memory.Store, outputDir string, files *index.Indexer, onChange func(context.Context) error, onRemember func(context.Context)) *Tools {
	return &Tools{
		configPath: configPath,
		confirm:    slices.Clone(cfg.Confirm),
		memory:     mem,
		outputDir:  outputDir,
		files:      files,
		onChange:   onChange,
		onRemember: onRemember,
	}
}

// Kind returns dispatch.KindBuiltin.
func (t *Tools) Kind() string { return dispatch.KindBuiltin }

// Tools returns the specs of configure, remember, write_file and the three
// file tools for the model. It reads the memory folders on each call, so a
// kind folder the user adds shows up in remember's choices on the next
// turn.
func (t *Tools) Tools() []engine.ToolSpec {
	specs := []engine.ToolSpec{{
		Name:        Configure,
		Description: description(),
		Parameters:  schema(),
	}}
	if t.memory != nil {
		specs = append(specs, engine.ToolSpec{
			Name:        Remember,
			Description: rememberDescription,
			Parameters:  rememberSchema(t.kinds()),
		})
	}
	if t.outputDir != "" {
		specs = append(specs, engine.ToolSpec{
			Name:        WriteFile,
			Description: writeFileDescription(t.outputDir),
			Parameters:  writeFileSchema(),
		})
	}
	if t.files != nil {
		specs = append(specs, t.fileToolSpecs()...)
	}
	return specs
}

// Confirm says configure always asks, with no session approval. Any other
// built-in asks when [builtin] confirm lists it, and runs without asking
// otherwise. The shipped list holds write_file alone, so remember saves
// without asking, as ARCHITECTURE.md "Memory" says, write_file asks, and
// the read-only file tools run without asking.
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

// Status describes the built-ins for `meru tools`: always connected,
// configure, which always asks, and the others, which ask only when
// [builtin] confirm lists them.
func (t *Tools) Status() []rpc.ServerInfo {
	tools := []rpc.ToolInfo{{
		Name:        Configure,
		Description: "Adds an MCP server to config.toml.",
		Confirm:     true,
		AlwaysAsks:  true,
	}}
	if t.memory != nil {
		tools = append(tools, rpc.ToolInfo{
			Name:        Remember,
			Description: "Saves one fact about you to ~/.meru/memory.",
			Confirm:     t.Confirm(Remember) != dispatch.ConfirmNever,
		})
	}
	if t.outputDir != "" {
		tools = append(tools, rpc.ToolInfo{
			Name:        WriteFile,
			Description: "Saves a file in " + t.outputDir + ".",
			Confirm:     t.Confirm(WriteFile) != dispatch.ConfirmNever,
		})
	}
	if t.files != nil {
		for _, f := range []struct{ name, desc string }{
			{ReadFile, "Reads a whole file in the indexed folders."},
			{ListFolder, "Lists a folder in the indexed folders."},
			{Grep, "Finds matching lines in the indexed folders."},
		} {
			tools = append(tools, rpc.ToolInfo{
				Name:        f.name,
				Description: f.desc,
				Confirm:     t.Confirm(f.name) != dispatch.ConfirmNever,
			})
		}
	}
	return []rpc.ServerInfo{{
		Name:      server,
		Kind:      dispatch.KindBuiltin,
		Connected: true,
		Tools:     tools,
		Offered:   len(tools),
	}}
}

// Call runs the named built-in with args, a JSON object. A request the tool
// refuses, such as bad arguments or a missing API key, comes back as a
// Result with IsError set, so the model can read why and tell the user. The
// error return is for a name that isn't a built-in.
func (t *Tools) Call(ctx context.Context, name string, args json.RawMessage) (dispatch.Result, error) {
	var text string
	var err error
	switch {
	case name == Configure:
		text, err = t.configure(ctx, args)
	case name == Remember && t.memory != nil:
		text, err = t.remember(ctx, args)
	case name == WriteFile && t.outputDir != "":
		text, err = t.writeFile(args)
	case name == ReadFile && t.files != nil:
		text, err = t.readFile(args)
	case name == ListFolder && t.files != nil:
		text, err = t.listFolder(ctx, args)
	case name == Grep && t.files != nil:
		text, err = t.grep(ctx, args)
	default:
		return dispatch.Result{}, fmt.Errorf("%q is not a built-in tool", name)
	}
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
		if !e.RunsOn(runtime.GOOS) {
			return catalog.Entry{}, fmt.Errorf("configure: %s runs only on %s", e.Name, e.OS)
		}
		// An entry that takes folders, such as filesystem, gets them on the
		// command line; configure has no field for them, so WithArgs(nil)
		// fails and names the terminal command.
		if _, err := e.WithArgs(nil); err != nil {
			return catalog.Entry{}, fmt.Errorf("configure: %w. Ask the user to run that in a terminal", err)
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
