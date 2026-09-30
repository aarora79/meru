// This file holds Tools, the dispatch.Backend for merud's built-in tools,
// and the configure tool. The remember tool lives in remember.go,
// write_file in writefile.go, read_file, list_folder and grep in files.go,
// the past chats those three read in chats.go, AttachmentText in attachments.go, search_files in search.go, and
// web_search and web_fetch in web.go, webguard.go and webdownload.go.

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
	"time"

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

	// lists guards on and confirm, which SetLists replaces while merud
	// runs, when the desktop app changes a built-in tool's policy.
	lists   sync.RWMutex
	on      []string // [builtin] tools: the built-ins the model may use
	confirm []string // [builtin] confirm from config.toml

	memory     *memory.Store  // where remember saves; nil leaves remember out
	outputDir  string         // where write_file writes, absolute; "" leaves write_file out
	files      *index.Indexer // what the file tools read through; nil leaves them out
	search     FileSearcher   // what search_files searches with; nil leaves it out
	web        *webClients    // web_search and web_fetch, as [web] sets them
	onChange   func(context.Context) error
	onRemember func(context.Context)       // runs after remember saves; nil for none
	now        func() time.Time            // the clock datetime reads; time.Now outside tests
	about      func(context.Context) About // gathers about_meru's facts; nil leaves about_meru out
	// webReady says whether web search works now, and in one line why
	// not; nil means it works whenever [web] searxng_url is set. See
	// UseWebCheck.
	webReady func() (bool, string)

	// sessionsDir is the folder of past chats the file tools also read,
	// set by ReadSessions; "" when they don't read it. See chats.go.
	sessionsDir string

	// mu makes one configure call finish its write before the next starts
	// reading config.toml, so two calls can't both pass the duplicate check.
	mu sync.Mutex
}

// New returns the built-in tools. configPath is config.toml; secrets.toml
// sits next to it. cfg is the [builtin] section: a tool its tools list
// leaves out is never offered, listed or run. mem is the memory folder
// that remember saves to; a nil mem leaves remember out, for tests of
// configure alone. outputDir is the absolute folder write_file writes in,
// [skills] output_dir with "~" expanded; "" leaves write_file out. onChange
// runs after configure writes config.toml; merud passes a function that
// rebuilds the MCP pool. onRemember runs after remember saves a memory;
// merud passes a function that syncs the memory folder into the store, so
// the next turn can recall the new fact. A nil onChange or onRemember does
// nothing. files is merud's indexer, which read_file, list_folder and grep
// read through, so they see the [index] folders with the indexer's skip
// rules; a nil files, or one whose [index] folders list is empty, leaves
// them out. New also lets them read
// outputDir, through files.ReadAlso: what write_file wrote, what web_fetch
// downloaded, and the mail attachments the google server saves in its
// attachments folder. web is the [web] section:
// web_search needs its SearXNG URL. web_fetch saves downloads in outputDir's downloads folder,
// and answers a prompt only after UseModel.
func New(configPath string, cfg config.Builtin, web config.Web, mem *memory.Store, outputDir string, files *index.Indexer, onChange func(context.Context) error, onRemember func(context.Context)) *Tools {
	// merud calls New once at startup, before any tool call, as ReadAlso
	// asks. The indexer never indexes outputDir, so nothing in it reaches
	// search.
	if files != nil && outputDir != "" {
		files.ReadAlso(outputDir)
	}
	return &Tools{
		now:        time.Now,
		configPath: configPath,
		on:         slices.Clone(cfg.Tools),
		confirm:    slices.Clone(cfg.Confirm),
		memory:     mem,
		outputDir:  outputDir,
		files:      files,
		web:        newWebClients(web),
		onChange:   onChange,
		onRemember: onRemember,
	}
}

// Kind returns dispatch.KindBuiltin.
func (t *Tools) Kind() string { return dispatch.KindBuiltin }

// enabled reports whether [builtin] tools lists name.
func (t *Tools) enabled(name string) bool {
	t.lists.RLock()
	defer t.lists.RUnlock()
	return slices.Contains(t.on, name)
}

// asks reports whether [builtin] confirm lists name.
func (t *Tools) asks(name string) bool {
	t.lists.RLock()
	defer t.lists.RUnlock()
	return slices.Contains(t.confirm, name)
}

// SetLists replaces the [builtin] tools and confirm lists with cfg's, so a
// policy the desktop app changed in config.toml takes effect on the next
// call without a restart. merud has written and checked config first.
func (t *Tools) SetLists(cfg config.Builtin) {
	t.lists.Lock()
	defer t.lists.Unlock()
	t.on = slices.Clone(cfg.Tools)
	t.confirm = slices.Clone(cfg.Confirm)
}

// hasFiles reports whether the file tools may run: merud gave the tools
// its indexer, and config names at least one [index] folder. The folders
// can change while merud runs, so this asks the indexer each time.
func (t *Tools) hasFiles() bool {
	return t.files != nil && t.files.HasFolders()
}

// Off is one tool that [builtin] tools lists but merud can't offer,
// because the setting it works on is missing, and the reason, for the log.
type Off struct {
	Tool   string
	Reason string
}

// Off returns the listed tools that stay off for want of their setting,
// in [builtin] tools order. merud logs one info line for each at startup,
// so a user who listed a tool can see why the model doesn't get it.
func (t *Tools) Off() []Off {
	var off []Off
	t.lists.RLock()
	on := slices.Clone(t.on)
	t.lists.RUnlock()
	for _, name := range on {
		if reason := t.missing(name); reason != "" {
			off = append(off, Off{Tool: name, Reason: reason})
		}
	}
	return off
}

// missing returns why the tool name can't run, or "" when it can. Only
// remember, write_file, the file tools, web_search and about_meru need a
// setting.
func (t *Tools) missing(name string) string {
	switch name {
	case Remember:
		if t.memory == nil {
			return "merud has no memory folder"
		}
	case WriteFile:
		if t.outputDir == "" {
			return "[skills] output_dir is empty"
		}
	case ReadFile, ListFolder, Grep:
		if !t.hasFiles() {
			return "[index] folders is empty"
		}
	case SearchFiles:
		if !t.hasFiles() {
			return "[index] folders is empty"
		}
		if t.search == nil {
			return "merud has no search index"
		}
	case WebSearch:
		if t.web.searxngURL == "" {
			return "[web] searxng_url is empty"
		}
	case AboutMeru:
		if t.about == nil {
			return "merud gave it no facts to report"
		}
	}
	return ""
}

// Tools returns the specs of the built-ins the model may use: those
// [builtin] tools lists whose setting is there. It reads the memory
// folders on each call, so a kind folder the user adds shows up in
// remember's choices on the next turn.
func (t *Tools) Tools() []engine.ToolSpec {
	specs := []engine.ToolSpec{{
		Name:        Configure,
		Description: description(),
		Parameters:  schema(),
	}, datetimeSpec()}
	if t.about != nil {
		specs = append(specs, aboutSpec())
	}
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
	if t.hasFiles() {
		specs = append(specs, t.fileToolSpecs()...)
		if t.search != nil {
			specs = append(specs, t.searchSpec())
		}
	}
	specs = append(specs, t.web.toolSpecs(t.saveDir())...)
	search, _ := t.webSearchOn()
	// DeleteFunc drops, in place, each spec the function returns true for.
	return slices.DeleteFunc(specs, func(s engine.ToolSpec) bool {
		return !t.enabled(s.Name) || (s.Name == WebSearch && !search)
	})
}

// UseWebCheck makes web_search depend on ready, which reports whether web
// search works now and says why not in one line. merud passes its SearXNG
// connector's state (ARCHITECTURE.md, "SearXNG and Ollama"), so the model
// is offered web_search only while SearXNG answers. merud calls it once,
// before the first turn. Without it, web_search depends on [web]
// searxng_url alone.
func (t *Tools) UseWebCheck(ready func() (bool, string)) {
	t.webReady = ready
}

// webSearchOn reports whether web_search may run now, and why not: [web]
// searxng_url must be set, and the check UseWebCheck gave must pass.
// [builtin] tools is checked apart, as for every built-in.
func (t *Tools) webSearchOn() (bool, string) {
	if t.web.searxngURL == "" {
		return false, "[web] searxng_url is empty"
	}
	if t.webReady != nil {
		return t.webReady()
	}
	return true, ""
}

// Confirm says configure always asks, with no session approval. Any other
// built-in asks when [builtin] confirm lists it, and runs without asking
// otherwise. The shipped list holds write_file alone, so remember saves
// without asking, as ARCHITECTURE.md "Memory" says, write_file asks, and
// the read-only file tools and web_search run without asking. web_fetch
// also runs without asking, but only for a URL the session knows and only
// without save; ConfirmCall, in webguard.go, decides the rest per call.
func (t *Tools) Confirm(name string) dispatch.Confirm {
	switch {
	case name == Configure:
		return dispatch.ConfirmAlways
	case t.asks(name):
		return dispatch.ConfirmAsk
	default:
		return dispatch.ConfirmNever
	}
}

// saveDir returns the folder web_fetch saves downloads in, or "" when
// there is no output folder and so no save.
func (t *Tools) saveDir() string {
	if t.outputDir == "" {
		return ""
	}
	return filepath.Join(t.outputDir, downloadsFolder)
}

// Locate returns ("meru", name): a built-in belongs to no server, so rows
// and metrics name merud itself.
func (t *Tools) Locate(name string) (string, string) { return server, name }

// Status describes the built-ins the model may use for `meru tools`:
// always connected, configure, which always asks, and the others, which
// ask only when [builtin] confirm lists them. A tool [builtin] tools
// leaves out isn't listed.
func (t *Tools) Status() []rpc.ServerInfo {
	tools := []rpc.ToolInfo{{
		Name:        Configure,
		Description: "Adds an MCP server to config.toml.",
		Confirm:     true,
		AlwaysAsks:  true,
	}, {
		Name:        DateTime,
		Description: "Reads the clock: the date, the time, a date's weekday, another time zone.",
		Confirm:     t.Confirm(DateTime) != dispatch.ConfirmNever,
	}}
	if t.about != nil {
		tools = append(tools, rpc.ToolInfo{
			Name:        AboutMeru,
			Description: Summary(AboutMeru),
			Confirm:     t.Confirm(AboutMeru) != dispatch.ConfirmNever,
		})
	}
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
	if t.hasFiles() {
		for _, f := range []struct{ name, desc string }{
			{ReadFile, "Reads a whole file in the indexed folders or the output folder."},
			{ListFolder, "Lists a folder in the indexed folders or the output folder."},
			{Grep, "Finds matching lines in the indexed folders or the output folder."},
		} {
			tools = append(tools, rpc.ToolInfo{
				Name:        f.name,
				Description: f.desc,
				Confirm:     t.Confirm(f.name) != dispatch.ConfirmNever,
			})
		}
		if t.search != nil {
			tools = append(tools, rpc.ToolInfo{
				Name:        SearchFiles,
				Description: "Searches the indexed folders by meaning and by words.",
				Confirm:     t.Confirm(SearchFiles) != dispatch.ConfirmNever,
			})
		}
	}
	if on, _ := t.webSearchOn(); on {
		tools = append(tools, rpc.ToolInfo{
			Name:        WebSearch,
			Description: "Searches the web through SearXNG at " + t.web.searxngURL + ".",
			Confirm:     t.Confirm(WebSearch) != dispatch.ConfirmNever,
		})
	}
	tools = append(tools, rpc.ToolInfo{
		Name: WebFetch,
		Description: "Reads a public web page, or answers a question from it. " +
			"Asks first for a URL no search or question of yours gave, and before any download.",
		Confirm: t.Confirm(WebFetch) != dispatch.ConfirmNever,
	})
	tools = slices.DeleteFunc(tools, func(ti rpc.ToolInfo) bool { return !t.enabled(ti.Name) })
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
// error return is for a name that isn't a built-in, or one [builtin] tools
// leaves out.
func (t *Tools) Call(ctx context.Context, name string, args json.RawMessage) (dispatch.Result, error) {
	if !t.enabled(name) {
		return dispatch.Result{}, fmt.Errorf("%q is not a built-in tool that [builtin] tools turns on", name)
	}
	var text string
	var sources []rpc.Citation // only search_files sets these
	var err error
	switch {
	case name == Configure:
		text, err = t.configure(ctx, args)
	case name == DateTime:
		text, err = dateTime(t.now(), args)
	case name == AboutMeru && t.about != nil:
		text = t.aboutMeru(ctx)
	case name == Remember && t.memory != nil:
		text, err = t.remember(ctx, args)
	case name == WriteFile && t.outputDir != "":
		text, err = t.writeFile(args)
	case name == ReadFile && t.hasFiles():
		text, err = t.readFile(args)
	case name == ListFolder && t.hasFiles():
		text, err = t.listFolder(ctx, args)
	case name == Grep && t.hasFiles():
		text, err = t.grep(ctx, args)
	case name == SearchFiles && t.hasFiles() && t.search != nil:
		text, sources, err = t.searchFiles(ctx, args)
	case name == WebSearch:
		// The check can change between the turn's tool list and this
		// call, so a call that comes while web search is down says why.
		on, why := t.webSearchOn()
		if !on {
			return dispatch.Result{Text: "web_search is off now: " + why, IsError: true}, nil
		}
		text, err = t.webSearch(ctx, args)
	case name == WebFetch:
		text, err = t.webFetch(ctx, args)
	default:
		return dispatch.Result{}, fmt.Errorf("%q is not a built-in tool", name)
	}
	if err != nil {
		return dispatch.Result{Text: err.Error(), IsError: true}, nil
	}
	return dispatch.Result{Text: text, Sources: sources}, nil
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

// EditConfig runs edit while holding the lock configure holds when it
// writes config.toml. merud's settings ops write config.toml too, for the
// desktop app; sharing one lock means two writers never read the same
// file and each replace it with their own change, losing the other's. It
// returns edit's error.
func (t *Tools) EditConfig(edit func() error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return edit()
}

// EveryRoute reports whether the built-in tool name goes with every
// route and every scope that offers tools, "direct" included: datetime,
// because "what day is Christmas?" routes direct, and about_meru, because
// "which model are you?" does too. Both only read, and their schemas are
// short. The web tools join every route too, while web_search is on, but
// not every scope; see IsWebTool.
func EveryRoute(name string) bool {
	return name == DateTime || name == AboutMeru
}

// IsWebTool reports whether name is web_search or web_fetch. The agent
// offers both on every route while web_search is on, so a question the
// router sends direct, such as what a song means, can still look the facts
// up. The desktop app's "My files", "Mail and calendar" and "Just talk"
// scopes leave them out.
func IsWebTool(name string) bool {
	return name == WebSearch || name == WebFetch
}

// Summary says in one line what the built-in tool name does, for the
// desktop app's list of tools, which shows the tools [builtin] tools
// leaves out too. It returns "" for a name that isn't a built-in.
func Summary(name string) string {
	switch name {
	case Configure:
		return "Adds an MCP server to config.toml. It asks every time."
	case DateTime:
		return "Reads the clock: the date, the time, a date's weekday, another time zone."
	case AboutMeru:
		return "Tells the model which models, folders, tools and skills this setup has."
	case Remember:
		return "Saves one fact about you to ~/.meru/memory."
	case WriteFile:
		return "Saves a file in the output folder, [skills] output_dir."
	case ReadFile:
		return "Reads a whole file in the indexed folders or the output folder."
	case ListFolder:
		return "Lists a folder in the indexed folders or the output folder."
	case Grep:
		return "Finds matching lines in the indexed folders or the output folder."
	case SearchFiles:
		return "Searches the indexed folders by meaning and by words."
	case WebSearch:
		return "Searches the web through the SearXNG instance [web] searxng_url names."
	case WebFetch:
		return "Reads a public web page. It asks first for a URL no search or question of yours gave."
	}
	return ""
}
