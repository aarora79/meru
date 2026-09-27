// This file defines the Engine interface and the types that pass through it.
// Everything that talks to a model goes through these four methods, so the rest
// of Meru never sees a model runtime's own request or response format.

package engine

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"time"
)

// Engine is the one boundary between Meru and a model runtime (see
// ARCHITECTURE.md, "Engine layer"). It has four methods on purpose: resist
// growing it. Anything new should be a field on Message, Options or
// Completion, as pictures are: see Message.Images.
//
// An interface in Go is a set of method signatures. Any type that has these
// methods satisfies Engine automatically; there is no "implements" keyword.
type Engine interface {
	// Generate sends msgs to the model named in opts and waits for the whole
	// answer. tools may be nil when the route allows no tool calls.
	Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error)

	// Stream is Generate, but it hands back the answer piece by piece as the
	// model writes it. iter.Seq2 is Go's standard "range over function" type:
	// callers loop with `for delta, err := range stream { ... }`. The last
	// Delta has Done set and carries the usage counters.
	Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error)

	// Embed turns each text into a vector with the engine's embedding model.
	// The result has one Vector per input text, in the same order.
	Embed(ctx context.Context, texts []string) ([]Vector, error)

	// Info reports what the runtime is and which models it has loaded.
	Info(ctx context.Context) (ModelInfo, error)
}

// Role says who wrote a message. It is a named string type, so the compiler
// stops us from passing an arbitrary string where a Role belongs.
type Role string

// The four roles a message can have. `const ( ... )` declares several
// constants at once.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in a conversation sent to the model.
//
// The text in backticks after each field is a struct tag. encoding/json reads
// it to decide the JSON key, and `omitempty` leaves the key out when the field
// holds its zero value (empty string, nil slice).
type Message struct {
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolName names the tool whose result this is, when Role is RoleTool.
	ToolName string `json:"tool_name,omitempty"`
	// Images holds pictures for the model to look at, each the raw bytes
	// of a PNG, JPEG, GIF or WebP file, on a user message. Only a model
	// with the "vision" capability can read them; see
	// OllamaEngine.Capabilities. encoding/json writes a []byte as a
	// base64 string, which is the form Ollama wants.
	Images [][]byte `json:"images,omitempty"`
}

// Vision is the capability a model needs to look at Message.Images, and
// ToolUse the one it needs to take a list of tools, as Ollama's /api/show
// lists them. Ollama refuses a request that offers tools to a model
// without ToolUse.
const (
	Vision  = "vision"
	ToolUse = "tools"
)

// ToolSpec describes one tool the model may call: its name, what it does, and
// a JSON Schema for its arguments. v0.1 sends no tools; the type exists so the
// interface doesn't change when MCP arrives in v0.3.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // raw JSON Schema, passed through untouched
}

// ToolCall is the model asking to run one tool.
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Options controls one Generate or Stream call. The zero value (every field
// empty) is valid: it means "the model's defaults".
type Options struct {
	// Model is the runtime's name for the model, straight from config.toml.
	Model string
	// Temperature, when non-nil, overrides the model's sampling temperature.
	// A pointer lets "not set" differ from "set to 0".
	Temperature *float64
	// MaxTokens caps how many tokens the model may write. Zero means no cap.
	MaxTokens int
	// LogProbs asks the runtime to report how likely each generated token was.
	// The router needs this (see docs/fast-router.md).
	LogProbs bool
	// TopLogProbs is how many alternatives to report at each position. Ollama
	// caps it at 20. Zero means "only the chosen token".
	TopLogProbs int
	// NoThink turns off a thinking model's hidden reasoning, for short calls
	// where the model must answer at once: picking a turn's skills, or a
	// session's one-sentence summary. LogProbs turns thinking off as well.
	// False leaves the model's default.
	NoThink bool
}

// Completion is a whole answer from Generate.
type Completion struct {
	Text      string
	ToolCalls []ToolCall
	// DoneReason is why the model stopped: "stop", "length", and so on.
	DoneReason string
	Usage      Usage
	// LogProbs has one entry per generated token, and is empty unless
	// Options.LogProbs was set.
	LogProbs []PositionLogProbs
}

// Delta is one piece of a streamed answer.
type Delta struct {
	Text      string
	ToolCalls []ToolCall
	// Done is true on the last Delta of a stream.
	Done bool
	// DoneReason and Usage are only filled on the last Delta.
	DoneReason string
	Usage      Usage
}

// Usage holds the runtime's own counters for one call. Meru reports token
// counts from here instead of estimating them.
type Usage struct {
	PromptTokens       int
	OutputTokens       int
	LoadDuration       time.Duration // time spent loading the model (0 when it was warm)
	PromptEvalDuration time.Duration // time spent reading the prompt
	EvalDuration       time.Duration // time spent writing the answer
	TotalDuration      time.Duration
}

// TokenLogProb is one token and how likely the model thought it was, as a
// natural logarithm. Log probabilities are zero or negative; closer to zero
// is more likely.
type TokenLogProb struct {
	Token   string
	LogProb float64
}

// PositionLogProbs is what the model considered at one position in its
// answer: the token it picked, and the alternatives it weighed.
type PositionLogProbs struct {
	Chosen TokenLogProb
	Top    []TokenLogProb
}

// Vector is one embedding: a list of numbers that places a text in "meaning
// space". Texts with similar meaning get vectors that point the same way.
type Vector []float32

// ModelInfo describes the runtime behind an Engine.
type ModelInfo struct {
	Runtime        string   // for example "ollama"
	RuntimeVersion string   // for example "0.34.0"
	LoadedModels   []string // models currently in memory
}

// ModelDetails is what the runtime says about one model: what it can do
// and how big it is. OllamaEngine.Details fills it from /api/show; a field
// the runtime leaves out stays empty or 0.
type ModelDetails struct {
	Capabilities  []string // for example ["completion", "vision", "tools", "thinking"]
	ParameterSize string   // for example "36.0B"
	Quantization  string   // for example "Q4_K_M"
	ContextLength int      // the most tokens the model can read at once
}

// clone returns a copy of d whose Capabilities a caller can change without
// touching the engine's cache.
func (d ModelDetails) clone() ModelDetails {
	d.Capabilities = slices.Clone(d.Capabilities)
	return d
}
