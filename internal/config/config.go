// This file defines the shape of config.toml as Go structs. Loading, defaults
// and validation live in load.go.

package config

// Config is everything merud reads from ~/.meru/config.toml, after defaults
// are filled in and the values have been checked.
//
// The `toml:"..."` struct tags tell the TOML parser which key maps to which
// field. A field without a tag, like Dir, is never read from the file.
type Config struct {
	// Profile picks a set of default models: "lite" (the default) or "full".
	Profile string `toml:"profile"`
	// Models names the model for each tier. Anything left empty here comes
	// from the profile.
	Models Models `toml:"models"`
	// Ollama says where the model runtime listens. It must be loopback.
	Ollama Ollama `toml:"ollama"`
	// Agent tunes the agent loop.
	Agent Agent `toml:"agent"`
	// Router tunes the one-token route classifier (docs/fast-router.md).
	Router Router `toml:"router"`
	// Observability controls OpenTelemetry export. Off unless an endpoint is set.
	Observability Observability `toml:"observability"`
	// Log sets how much merud writes to merud.log.
	Log Log `toml:"log"`
	// Index says which folders merud indexes for search (v0.2).
	Index Index `toml:"index"`
	// MCP lists the MCP servers whose tools the model may use (v0.3).
	MCP MCP `toml:"mcp"`
	// A2A lists the other agents Meru may hand tasks to (v0.3).
	A2A A2A `toml:"a2a"`
	// Builtin sets which built-in tools the model may use, and which of
	// them ask before they run (v0.3).
	Builtin Builtin `toml:"builtin"`
	// Skills configures the skill registry and the files skills write (v0.4).
	Skills Skills `toml:"skills"`
	// Web configures the built-in web_search and web_fetch tools (v0.3).
	Web Web `toml:"web"`
	// Chat tunes the `meru chat` screen. meru reads it, not merud.
	Chat Chat `toml:"chat"`
	// Commands lists the local programs the model may run, one tool each
	// (v0.3). The commands package checks them when merud starts.
	Commands []Command `toml:"commands"`
	// Connectors holds one [connectors.<id>] table per connector: whether
	// it is on, and the values the user gave for its fields. Nothing
	// reads it yet; the connector supervisor will (ARCHITECTURE.md,
	// "Connectors and the supervisor").
	Connectors map[string]Connector `toml:"connectors"`

	// Dir is the Meru home directory, usually ~/.meru. It isn't in the file;
	// Load fills it in.
	Dir string `toml:"-"`
}

// Models names the Ollama model for each tier (see ARCHITECTURE.md, "Model
// tiers"). The code asks for a tier; config says which model fills it.
//
// Sets lists the named sets `/model` and `meru model use` switch between.
// Fast, Main and Embed stay the default: they are what merud starts with,
// and what `/model save` writes.
type Models struct {
	Fast  string `toml:"fast"`
	Main  string `toml:"main"`
	Embed string `toml:"embed"`
	// Think set to true lets the main model reason before it answers when
	// no set is in use. Left out or false, thinking stays off: the
	// benchmark ran every model that way, and a thinking model takes far
	// longer to answer. A set's own think setting wins while it is in use.
	Think bool       `toml:"think"`
	Sets  []ModelSet `toml:"sets"`
}

// ThinkOff reports whether the main model's thinking is off when no set is
// in use: true unless think = true.
func (m Models) ThinkOff() bool {
	return !m.Think
}

// ModelSet is one [[models.sets]] entry: a name for a choice of models, so
// a switch names one thing rather than three. A tier left empty stays as it
// is. Only Main changes while merud runs; Fast and Embed take effect when
// the set is saved as the default and merud restarts. See ARCHITECTURE.md,
// "Model tiers".
type ModelSet struct {
	// Name is what `/model <name>` takes: letters, digits, ".", "-" and
	// "_".
	Name  string `toml:"name"`
	Main  string `toml:"main"`
	Fast  string `toml:"fast"`
	Embed string `toml:"embed"`
	// Think set to false turns the main model's hidden reasoning off for
	// this set. Left out, the model does what it does by default. It is a
	// pointer so that "left out" (nil) differs from false.
	Think *bool `toml:"think"`
}

// ThinkOff reports whether the set turns the main model's thinking off.
func (s ModelSet) ThinkOff() bool {
	return s.Think != nil && !*s.Think
}

// Ollama is where the local model runtime listens.
type Ollama struct {
	// BaseURL must point at a loopback address, such as http://127.0.0.1:11434.
	BaseURL string `toml:"base_url"`
	// KeepAlive is passed to Ollama on every call. "-1" keeps models loaded
	// for as long as Ollama runs, which is what makes answers start fast.
	KeepAlive string `toml:"keep_alive"`
	// ContextLength is how many tokens of prompt and answer Ollama makes
	// room for, sent as num_ctx on every chat call. Default 32768: Meru's
	// prompts with tool schemas run to about 20,000 tokens, the benchmark
	// ran at 32768, and Ollama's own default can be far smaller, which cuts
	// the prompt short. 0 leaves Ollama's own setting.
	ContextLength int `toml:"context_length"`
}

// Agent tunes the agent loop.
type Agent struct {
	// MaxRounds caps model calls per turn. Default 8.
	MaxRounds int `toml:"max_rounds"`
	// MaxOutputTokens caps the tokens the main model may write in one call,
	// its hidden thinking included; Ollama calls it num_predict. Default
	// 8192. Without a cap, a thinking model can reason for many minutes and
	// never answer.
	MaxOutputTokens int `toml:"max_output_tokens"`
	// TurnTimeout is how long one turn may run, from question to answer, as
	// a Go duration. When it runs out, merud stops the model and the tools
	// and tells the user it couldn't answer. Default "5m".
	TurnTimeout string `toml:"turn_timeout"`
	// HistoryTurns caps how many earlier turns of the session go into the
	// prompt. v0.1 counts turns; a token budget replaces it later.
	HistoryTurns int `toml:"history_turns"`
	// SystemPrompt is the persona and rules sent at the start of every turn.
	// Empty means the built-in default.
	SystemPrompt string `toml:"system_prompt"`
	// SummaryIdle is how long a session must go without a question before
	// merud writes its summary, as a Go duration. Default "30m".
	SummaryIdle string `toml:"summary_idle"`
}

// Router tunes the route classifier. See docs/fast-router.md.
type Router struct {
	TopLogProbs   int     `toml:"top_logprobs"`   // 1 to 20; Ollama's cap is 20
	Temperature   float64 `toml:"temperature"`    // above 0; 1.0 means raw probabilities
	MinConfidence float64 `toml:"min_confidence"` // 0 to 1; below it, take the fallback
	Fallback      string  `toml:"fallback"`       // one of the four routes
	// Decision picks how the letters become a route: "top" takes the most
	// likely letter, "marginal" asks whether the turn needs a search and
	// whether it needs tools, each against its own threshold.
	Decision        string  `toml:"decision"`
	SearchThreshold float64 `toml:"search_threshold"` // 0 to 1; "marginal" only
	ToolsThreshold  float64 `toml:"tools_threshold"`  // 0 to 1; "marginal" only
}

// Observability controls where metrics and traces go.
type Observability struct {
	// OTLPEndpoint is an OTLP/HTTP address such as http://127.0.0.1:4318.
	// Empty turns export off. A non-loopback address is refused.
	OTLPEndpoint string `toml:"otlp_endpoint"`
	// MetricsInterval is how often metrics are exported, as a Go duration
	// string such as "10s".
	MetricsInterval string `toml:"metrics_interval"`
	// Traces turns trace export on or off when an endpoint is set.
	Traces bool `toml:"traces"`
	// CaptureContent puts prompt and response text into spans. Off by default.
	CaptureContent bool `toml:"capture_content"`
}

// Log controls merud.log.
type Log struct {
	// Level is the lowest level merud writes: "debug", "info" (the default),
	// "warn" or "error". At "info" merud writes one line per turn plus its
	// startup and shutdown lines; "debug" adds a line for each stage of a
	// turn. merud's -v flag forces "debug".
	Level string `toml:"level"`
}

// Index controls which files merud reads into its search index. See
// ARCHITECTURE.md, "Storage" and "Retrieval".
type Index struct {
	// Folders lists the folders to index, for example ["~/notes"]. A leading
	// "~" means the home directory. Empty means nothing is indexed.
	Folders []string `toml:"folders"`
	// Ignore adds .gitignore-style patterns to the built-in skip list (hidden
	// folders, node_modules, build output, secret files and so on).
	Ignore []string `toml:"ignore"`
	// MaxFileMB skips files larger than this many megabytes. Default 5.
	MaxFileMB int `toml:"max_file_mb"`
	// ChunkTokens is the target size of one chunk, in estimated tokens.
	// Default 500.
	ChunkTokens int `toml:"chunk_tokens"`
	// OverlapTokens is how much each chunk repeats of the one before it, so a
	// sentence cut at a boundary still appears whole in one chunk. Default 50.
	OverlapTokens int `toml:"overlap_tokens"`
	// Watch re-indexes a file as soon as it changes while merud runs. Default
	// true. merud also rescans every folder at startup.
	Watch bool `toml:"watch"`
	// Retrieval says how a turn finds text in these folders. "auto", the
	// default, searches before the model answers on the search routes and
	// also offers the search_files tool. "agentic" skips that search and
	// earlier conversations, and leaves the model to explore with
	// search_files, grep, list_folder and read_file. See ARCHITECTURE.md,
	// "Retrieval".
	Retrieval string `toml:"retrieval"`
}

// The values [index] retrieval accepts.
const (
	RetrievalAuto    = "auto"
	RetrievalAgentic = "agentic"
)

// MCP holds the [[mcp.servers]] entries. See ARCHITECTURE.md, "MCP".
type MCP struct {
	Servers []MCPServer `toml:"servers"`
}

// MCPServer is one [[mcp.servers]] entry: how to reach the server and which
// of its tools the model may use. Exactly one of Command and URL is set.
// merud turns it into an mcp.ServerConfig and checks it there, because the
// rules for names, URLs and tool lists live with the MCP client.
type MCPServer struct {
	// Name prefixes the server's tools: "<name>.<tool>".
	Name string `toml:"name"`
	// Command and Args start a stdio server. merud runs Command directly,
	// with no shell.
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	// Env adds environment variables for a stdio server. A value written
	// "secret:<name>" is replaced by that entry of ~/.meru/secrets.toml. A
	// url entry with env fails to load: merud starts no process for it, so
	// the variables would go nowhere.
	Env map[string]string `toml:"env"`
	// URL reaches a Streamable HTTP server that is already running.
	URL string `toml:"url"`
	// Headers go on every HTTP request to URL. A value written
	// "secret:<name>" is replaced from secrets.toml, as in Env.
	Headers map[string]string `toml:"headers"`
	// Remote lets merud connect to a URL that isn't loopback. It covers
	// only where merud connects; it says nothing about what the server
	// itself reaches. It was called network before.
	Remote bool `toml:"remote"`
	// Allow lists the tools the model may call; empty means none.
	Allow []string `toml:"allow"`
	// Confirm lists allowed tools that ask before each call.
	Confirm []string `toml:"confirm"`
	// AlwaysConfirm lists allowed tools that ask before every call and
	// offer no approval for the session, such as a tool that runs shell
	// commands. It needs no entry in Confirm.
	AlwaysConfirm []string `toml:"always_confirm"`
	// Timeout caps one call, as a Go duration such as "60s". Empty means 60s.
	Timeout string `toml:"timeout"`
}

// A2A holds the [[a2a.agents]] entries. See ARCHITECTURE.md, "Other agents
// (A2A)".
type A2A struct {
	Agents []A2AAgent `toml:"agents"`
}

// A2AAgent is one [[a2a.agents]] entry. Each allowed skill becomes a tool
// named "a2a.<name>.<skill>".
type A2AAgent struct {
	Name string `toml:"name"`
	// URL is where merud reads the agent card. It must be loopback unless
	// Remote is true, which covers only where merud connects. It was called
	// network before.
	URL    string `toml:"url"`
	Remote bool   `toml:"remote"`
	// Headers go on every request to the agent; "secret:<name>" values come
	// from secrets.toml.
	Headers map[string]string `toml:"headers"`
	// Allow lists the skills, by the IDs on the agent card, that become
	// tools; empty means none. Confirm lists allowed skills that ask first.
	Allow   []string `toml:"allow"`
	Confirm []string `toml:"confirm"`
	// Timeout caps one call, as a Go duration. Empty means 60s.
	Timeout string `toml:"timeout"`
}

// Skills configures skills. See ARCHITECTURE.md, "Skills".
type Skills struct {
	// OutputDir is the one folder the write_file tool may write in. A
	// leading "~" means the home directory. Default "~/meru-output".
	OutputDir string `toml:"output_dir"`
	// Disabled names skills merud neither loads nor, for a built-in,
	// installs. A name that matches no skill is fine: the user may add
	// that skill later. Default [].
	Disabled []string `toml:"disabled"`
}

// Builtin configures the tools built into merud. configure always asks,
// whatever Confirm says.
type Builtin struct {
	// Tools lists the built-in tools the model may use. A tool left out
	// isn't registered at all. Default: all eleven, as BuiltinTools returns
	// them. A listed tool whose setting is missing, such as the file tools
	// with no [index] folders, still stays off.
	Tools []string `toml:"tools"`
	// Confirm lists built-in tools that ask before each call. Each must
	// also be in Tools. Default ["write_file"].
	Confirm []string `toml:"confirm"`
}

// Web configures web search. merud searches through a SearXNG instance the
// user runs on this machine. See ARCHITECTURE.md, "Web search". web_fetch
// has no key here: [builtin] tools turns it on or off.
type Web struct {
	// SearXNGURL is where SearXNG answers, such as http://127.0.0.1:8888.
	// It must be loopback, because merud connects to it. Empty turns
	// web_search off.
	SearXNGURL string `toml:"searxng_url"`
	// MaxResults is how many results web_search returns when the model
	// doesn't say. Default 8, at most 20.
	MaxResults int `toml:"max_results"`
}

// Chat tunes the `meru chat` screen. Only the client reads it; merud
// ignores it.
type Chat struct {
	// MouseCopy makes a click on a code block's "⧉ copy N" label copy the
	// block. It is on by default, since a click is what most people try
	// first. To see clicks the chat captures the mouse, so the terminal's
	// own click-and-drag selection needs a modifier key (Option in iTerm2,
	// Shift in most others); false gives plain selection back.
	MouseCopy bool `toml:"mouse_copy"`
}

// Command is one [[commands]] entry: a program the user declared, which the
// model sees as the tool "cmd.<name>". The model fills in the parameters;
// it never writes the command. See ARCHITECTURE.md, "Local commands".
//
// Load only parses these entries. commands.New checks them when merud
// starts, because the rules for placeholders, paths and interpreters live
// with the code that runs the program.
type Command struct {
	// Name makes the tool name "cmd.<name>": letters, digits, - and _.
	Name string `toml:"name"`
	// Description tells the model what the command is for.
	Description string `toml:"description"`
	// Argv is the program and its arguments. An element may hold "{param}"
	// placeholders; "{{" and "}}" stand for a literal brace. A leading "~"
	// means the home directory.
	Argv []string `toml:"argv"`
	// Cwd is the folder the program starts in. Empty means the home
	// directory.
	Cwd string `toml:"cwd"`
	// Timeout caps one run, as a Go duration. Empty means 30s; at most 300s.
	Timeout string `toml:"timeout"`
	// Confirm makes each call ask the user first.
	Confirm bool `toml:"confirm"`
	// EnvAllowlist names environment variables the program gets from
	// merud's environment, on top of PATH, HOME and LANG.
	EnvAllowlist []string `toml:"env_allowlist"`
	// Params declares each placeholder, keyed by its name.
	Params map[string]CommandParam `toml:"params"`
}

// CommandParam declares one parameter of a [[commands]] entry.
type CommandParam struct {
	// Type is "string", "int", "enum" or "path".
	Type string `toml:"type"`
	// Description tells the model what to pass.
	Description string `toml:"description"`
	// Under is the folder a path must resolve inside. Path only, and
	// required for it.
	Under string `toml:"under"`
	// Min and Max bound an int. They are pointers so that a missing bound
	// (nil) differs from a bound of 0.
	Min *int64 `toml:"min"`
	Max *int64 `toml:"max"`
	// Values lists what an enum accepts.
	Values []string `toml:"values"`
	// MaxLen caps a string, in bytes. 0 means 4096.
	MaxLen int `toml:"max_len"`
	// Pattern is a regular expression, in Go's RE2 syntax, that a whole
	// string must match, such as "[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+" for a
	// GitHub owner/name. String only; empty means any text.
	Pattern string `toml:"pattern"`
}

// Connector is one [connectors.<id>] table: "enabled", a true or false,
// and one key per field of the connector's manifest, such as vault_path.
// Each value is a string or a bool. A secret field never appears here;
// its value lives in secrets.toml as secret:connector_<id>_<field>.
//
// It is a map rather than a struct because each connector has its own
// fields, named in its manifest. `any` is Go's name for "a value of any
// type"; checkConnectors in load.go makes sure each one is a string or a
// bool.
type Connector map[string]any

// Enabled reports whether the table says enabled = true. ok is false when
// the table leaves enabled out, so the caller can fall back on the
// connector's own default.
func (c Connector) Enabled() (enabled, ok bool) {
	// c["enabled"].(bool) is a type assertion: it asks whether the value
	// is a bool, and ok says whether it was.
	enabled, ok = c["enabled"].(bool)
	return enabled, ok
}

// Value returns the string value of key, and false when the table has no
// such key or it isn't a string.
func (c Connector) Value(key string) (string, bool) {
	v, ok := c[key].(string)
	return v, ok
}
