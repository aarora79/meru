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
