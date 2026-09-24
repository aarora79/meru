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
	// Builtin sets which built-in tools ask before they run (v0.3).
	Builtin Builtin `toml:"builtin"`
	// Skills configures the skill registry and the files skills write (v0.4).
	Skills Skills `toml:"skills"`

	// Dir is the Meru home directory, usually ~/.meru. It isn't in the file;
	// Load fills it in.
	Dir string `toml:"-"`
}

// Models names the Ollama model for each tier (see ARCHITECTURE.md, "Model
// tiers"). The code asks for a tier; config says which model fills it.
type Models struct {
	Fast  string `toml:"fast"`
	Main  string `toml:"main"`
	Embed string `toml:"embed"`
}

// Ollama is where the local model runtime listens.
type Ollama struct {
	// BaseURL must point at a loopback address, such as http://127.0.0.1:11434.
	BaseURL string `toml:"base_url"`
	// KeepAlive is passed to Ollama on every call. "-1" keeps models loaded
	// for as long as Ollama runs, which is what makes answers start fast.
	KeepAlive string `toml:"keep_alive"`
}

// Agent tunes the agent loop.
type Agent struct {
	// MaxRounds caps model calls per turn. Default 8.
	MaxRounds int `toml:"max_rounds"`
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
}

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
	// "secret:<name>" is replaced by that entry of ~/.meru/secrets.toml.
	Env map[string]string `toml:"env"`
	// URL reaches a Streamable HTTP server that is already running.
	URL string `toml:"url"`
	// Headers go on every HTTP request to URL. A value written
	// "secret:<name>" is replaced from secrets.toml, as in Env.
	Headers map[string]string `toml:"headers"`
	// Network allows a URL that isn't loopback.
	Network bool `toml:"network"`
	// Allow lists the tools the model may call; empty means none.
	Allow []string `toml:"allow"`
	// Confirm lists allowed tools that ask before each call.
	Confirm []string `toml:"confirm"`
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
	// Network is true.
	URL     string `toml:"url"`
	Network bool   `toml:"network"`
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
}

// Builtin configures the tools built into merud. configure always asks,
// whatever Confirm says.
type Builtin struct {
	// Confirm lists built-in tools that ask before each call.
	Confirm []string `toml:"confirm"`
}
