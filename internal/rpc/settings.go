// This file holds the types of the settings ops the desktop app sends:
// where a question may look (Scope), each tool's policy and the change
// OpToolPolicy makes, the tool sources and catalog servers OpConnections
// lists, the server of the user's own that OpMCPAdd adds, the folders
// OpFolders lists, the two kinds of file OpSaveFile saves, and the models
// and model sets OpModels reports and OpModelUse and OpModelSet pick from.
// merud makes every change these ops ask for; a client only sends the
// request.

package rpc

// The scopes a question can take, for Request.Scope: where Meru may look
// for the answer. Only ScopeAuto runs the router; the others pick the
// route and the tools themselves. The set is fixed, because merud logs the
// scope and a log field must hold a small set of values.
const (
	// ScopeAuto lets the router pick the route, as a question with no
	// scope does.
	ScopeAuto = "auto"
	// ScopeFiles searches the user's [index] folders and offers the file
	// tools.
	ScopeFiles = "files"
	// ScopeMail offers the tools of the connected mail and calendar
	// servers, and no others.
	ScopeMail = "mail"
	// ScopeWeb offers web_search and web_fetch, and no others.
	ScopeWeb = "web"
	// ScopeTalk answers from the model alone: no search and no tools.
	ScopeTalk = "talk"
)

// Scopes returns the scopes in the order a client shows them. It returns a
// new slice each call.
func Scopes() []string { return []string{ScopeAuto, ScopeFiles, ScopeMail, ScopeWeb, ScopeTalk} }

// The policies a tool can have, in ToolPolicy.Policy and
// PolicyChange.Policy. They map onto config.toml: off means the tool isn't
// in allow; ask means it is in allow and in confirm; allow means it is in
// allow and not in confirm.
const (
	PolicyOff   = "off"
	PolicyAsk   = "ask"
	PolicyAllow = "allow"
	// PolicyAlways marks a tool that asks before every call and offers no
	// approval for the session: configure, and a server's always_confirm
	// tools. It can be turned off, but never to allow. Only merud reports
	// it; OpToolPolicy doesn't take it.
	PolicyAlways = "always"
)

// PolicyChange is what OpToolPolicy changes: the tool named Tool, of the
// source named Server, whose Kind is "mcp", "a2a" or "builtin", gets
// Policy (PolicyOff, PolicyAsk or PolicyAllow). For a built-in tool Server
// is "meru".
type PolicyChange struct {
	Kind   string `json:"kind"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Policy string `json:"policy"`
}

// CustomServer is an MCP server outside the catalog, as the Settings screen's "Add
// your own MCP server" form describes it for OpMCPAdd. Exactly one of
// Command and URL is set: Command, with Args, is a program merud starts
// (a stdio server), and URL is a Streamable HTTP server the user runs.
// Remote says the user means a URL on another computer; merud refuses
// such a URL without it, as `meru mcp add http` refuses one without
// --remote. Env applies only to a stdio server.
type CustomServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
	Remote  bool     `json:"remote,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`
}

// EnvVar is one environment variable for a custom server. When Secret is
// true, Value is a secret, such as an API key: merud saves it in
// secrets.toml and config.toml holds only "secret:<name>". merud never
// sends a secret back.
type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// Connection is one tool source as the app's Settings shows it: an MCP
// server, an A2A agent, merud's built-in tools, or the local commands.
type Connection struct {
	Name string `json:"name"`
	// Kind is "mcp", "a2a", "builtin" or "command".
	Kind string `json:"kind"`
	// Transport is "stdio" or "http" for an MCP server, "http" for an
	// agent, and "" otherwise. URL is where merud connects, and Remote is
	// true when config lets it leave this machine.
	Transport string `json:"transport,omitempty"`
	URL       string `json:"url,omitempty"`
	Remote    bool   `json:"remote,omitempty"`
	// State is MCPConnected or MCPNotConnected, and Err says in one line
	// why a source isn't connected.
	State string `json:"state"`
	Err   string `json:"err,omitempty"`
	// Connector is set for a connector merud's supervisor runs, and for
	// a server added by hand that takes a connector's place
	// (ConnectorByHand): the connector's state, with Sentence saying it
	// in one line. Fix names the [connectors.<id>] keys to set when the
	// state is ConnectorNeedsConfig.
	Connector string   `json:"connector,omitempty"`
	Sentence  string   `json:"sentence,omitempty"`
	Fix       []string `json:"fix,omitempty"`
	// Link is the sign-in link a connector's server gave, while it waits
	// for the user to sign in; the app shows it as a button.
	Link string `json:"link,omitempty"`
	// Web and WebSentence are set on the built-in tools' connection only:
	// the SearXNG connector's state, one of the Connector states, and its
	// sentence, such as "Web search can't start: Docker isn't running."
	// The desktop app's Web search card and /mcp show them.
	Web         string `json:"web,omitempty"`
	WebSentence string `json:"web_sentence,omitempty"`
	// Tools lists each tool the source offers or config names, with its
	// policy, allowed tools first. Offered counts the tools the source
	// offers, or is -1 when merud doesn't know, as for a server that never
	// connected.
	Tools   []ToolPolicy `json:"tools"`
	Offered int          `json:"offered"`
	// Fixed is true when the app can't change these policies, as for the
	// local commands, which config.toml declares one by one; Note says
	// how to change them instead.
	Fixed bool   `json:"fixed,omitempty"`
	Note  string `json:"note,omitempty"`
}

// ToolPolicy is one tool and its policy.
type ToolPolicy struct {
	// Name is the tool's own name, without the server: "search_gmail_messages".
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Policy is PolicyOff, PolicyAsk, PolicyAllow or PolicyAlways.
	Policy string `json:"policy"`
	// Missing is true for a tool config allows but the connected source
	// doesn't offer, usually a typo or a tool the server dropped.
	Missing bool `json:"missing,omitempty"`
}

// CatalogEntry is one server from Meru's catalog (internal/catalog), with
// what it needs from the user, for the Settings screen's "Add a connection".
type CatalogEntry struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Transport is "stdio", where merud starts Command, or "http", where
	// the user starts the server with Start and merud connects to URL.
	Transport string `json:"transport"`
	Command   string `json:"command,omitempty"`
	URL       string `json:"url,omitempty"`
	Start     string `json:"start,omitempty"`
	Requires  string `json:"requires,omitempty"`
	Install   string `json:"install,omitempty"`
	Docs      string `json:"docs,omitempty"`
	// Needs lists what to ask the user, in order.
	Needs []CatalogNeed `json:"needs,omitempty"`
	// Allow and Confirm are the tools the entry turns on, and the ones of
	// those that ask first.
	Allow   []string `json:"allow"`
	Confirm []string `json:"confirm,omitempty"`
	// Added is true when config.toml already has a server of this name.
	Added bool `json:"added,omitempty"`
}

// CatalogNeed is one thing a catalog server needs from the user. Kind is
// "api_key" or "note"; an api_key names the Secret it goes into, and Saved
// says whether secrets.toml holds it already. The value never comes back.
type CatalogNeed struct {
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
	Help   string `json:"help,omitempty"`
	Secret string `json:"secret,omitempty"`
	Saved  bool   `json:"saved,omitempty"`
}

// FolderInfo is one folder on a "folders" event.
type FolderInfo struct {
	// Path is the folder as config.toml writes it, "~/Notes", or as it
	// would.
	Path string `json:"path"`
	// Exists is false for a folder in config that isn't on disk now.
	Exists bool `json:"exists"`
	// Files counts the files: for an [index] folder, the ones the index
	// holds; for a suggested one, the ones the indexer would read, up to a
	// cap, with More true when the count stopped at it.
	Files int  `json:"files"`
	More  bool `json:"more,omitempty"`
}

// The kinds of file OpSaveFile saves, in Request.Kind.
const (
	// SaveChat saves the whole session as Markdown: each question and its
	// answer.
	SaveChat = "chat"
	// SaveNote saves Request.Text, one answer, as a Markdown note.
	SaveNote = "note"
)

// ModelsInfo is what OpModels reports.
type ModelsInfo struct {
	// Profile is config's profile, "lite" or "full", and Fast, Main and
	// Embed the models for each tier, as merud started with them.
	Profile string `json:"profile"`
	Fast    string `json:"fast"`
	Main    string `json:"main"`
	Embed   string `json:"embed"`
	// Runtime and RuntimeVersion name the model runtime, "ollama" and its
	// version, and Loaded lists the models it holds in memory now. Err
	// says why merud couldn't ask it.
	Runtime        string   `json:"runtime,omitempty"`
	RuntimeVersion string   `json:"runtime_version,omitempty"`
	Loaded         []string `json:"loaded,omitempty"`
	Err            string   `json:"err,omitempty"`
	// ConfigPath is config.toml, where the models are set, and OutputDir
	// the folder write_file saves in.
	ConfigPath string `json:"config_path"`
	OutputDir  string `json:"output_dir,omitempty"`
	// Choices are the models we tried as the answer model, each with
	// whether Ollama has it and which tier uses it now.
	Choices []ModelChoice `json:"choices,omitempty"`
	// Sets are the [[models.sets]] from config.toml, in config's order,
	// and Active names the one in use now: the one the last OpModelUse
	// picked, or at startup the first whose models match the ones merud
	// started with. Active is "" when none is in use.
	Sets   []ModelSet `json:"sets,omitempty"`
	Active string     `json:"active,omitempty"`
	// Warning, in the reply to OpModelUse or OpModelSet, says what the
	// switch leaves undone or what the new answer model can't do, such as
	// call tools. It is empty otherwise.
	Warning string `json:"warning,omitempty"`
}

// ModelSet is one row of `meru model` and the chat's /model box: a named
// set of models the user can switch to, and what merud knows about it
// right now.
type ModelSet struct {
	Name string `json:"name"`
	// Main, Fast and Embed are the set's models; "" when the set leaves
	// that tier as it is.
	Main  string `json:"main,omitempty"`
	Fast  string `json:"fast,omitempty"`
	Embed string `json:"embed,omitempty"`
	// ThinkOff is true when the set turns the main model's thinking off.
	ThinkOff bool `json:"think_off,omitempty"`
	// Bytes is the main model's size on disk, from Ollama's list of the
	// models it has; 0 when Ollama doesn't list it.
	Bytes int64 `json:"bytes,omitempty"`
	// Pulled is true when Ollama has the main model on disk, and Loaded
	// when it holds it in memory now.
	Pulled bool `json:"pulled,omitempty"`
	Loaded bool `json:"loaded,omitempty"`
	// Active is true for the set in use now.
	Active bool `json:"active,omitempty"`
	// Capabilities are what Ollama says the main model can do, such as
	// "tools" and "vision"; empty when Ollama can't say.
	Capabilities []string `json:"capabilities,omitempty"`
}

// ModelChoice is one model we tried as the answer model, as OpModels
// reports it. merud fills it from its list of known models and from
// Ollama.
type ModelChoice struct {
	// Name is the model as Ollama names it, and Label a short name for
	// people.
	Name  string `json:"name"`
	Label string `json:"label"`
	// Size is the download's size on disk, such as "8.1 GB".
	Size string `json:"size"`
	// Good and Bad say in one line each what the model did well and
	// badly in our tests.
	Good string `json:"good"`
	Bad  string `json:"bad"`
	// Capabilities are what the model can do, such as "vision" and
	// "tools": Ollama's own list when it has the model, and what it
	// listed in our tests otherwise.
	Capabilities []string `json:"capabilities"`
	// Installed is true when Ollama has the model on disk.
	Installed bool `json:"installed"`
	// Tiers names the tiers the model fills now, "main" and "fast", or
	// none.
	Tiers []string `json:"tiers"`
	// Pull fetches the model and Run tries it in a terminal, such as
	// "ollama pull gemma3:12b" and "ollama run gemma3:12b".
	Pull string `json:"pull"`
	Run  string `json:"run"`
}
