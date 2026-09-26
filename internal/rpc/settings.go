// This file holds the types of the settings ops the desktop app sends:
// where a question may look (Scope), each tool's policy and the change
// OpToolPolicy makes, the tool sources and catalog servers OpConnections
// lists, the folders OpFolders lists, the two kinds of file OpSaveFile
// saves, and the models OpModels reports. merud makes every change these
// ops ask for; a client only sends the request.

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

// Connection is one tool source as the app's Library shows it: an MCP
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
// what it needs from the user, for the Library's "Add a connection".
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
}
