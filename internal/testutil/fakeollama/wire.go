// This file defines the JSON shapes the fake reads and writes. They follow
// Ollama's API documentation (docs/api.md in the ollama/ollama repository) for
// the fields Meru uses, and leave the rest out.

package fakeollama

import "encoding/json"

// chatRequest is the body of POST /api/chat.
//
// Stream is a pointer because Ollama streams when the field is missing: a nil
// pointer means "not sent", which differs from an explicit false.
type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	Stream      *bool           `json:"stream,omitempty"`
	Options     map[string]any  `json:"options,omitempty"`
	KeepAlive   json.RawMessage `json:"keep_alive,omitempty"`
	LogProbs    bool            `json:"logprobs,omitempty"`
	TopLogProbs int             `json:"top_logprobs,omitempty"`
}

// generateRequest is the body of POST /api/generate.
type generateRequest struct {
	Model       string          `json:"model"`
	Prompt      string          `json:"prompt"`
	System      string          `json:"system,omitempty"`
	Stream      *bool           `json:"stream,omitempty"`
	Options     map[string]any  `json:"options,omitempty"`
	KeepAlive   json.RawMessage `json:"keep_alive,omitempty"`
	LogProbs    bool            `json:"logprobs,omitempty"`
	TopLogProbs int             `json:"top_logprobs,omitempty"`
}

// embedRequest is the body of POST /api/embed. Input is either one string or
// a list of strings, so the fake decodes it by hand.
type embedRequest struct {
	Model      string          `json:"model"`
	Input      json.RawMessage `json:"input"`
	KeepAlive  json.RawMessage `json:"keep_alive,omitempty"`
	Dimensions int             `json:"dimensions,omitempty"`
}

// chatMessage is one message in a chat request or response.
type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
}

// wireToolCall is how Ollama sends a tool call: the name and arguments sit
// inside a "function" object, and the arguments are a JSON object, not a
// string.
type wireToolCall struct {
	ID       string       `json:"id,omitempty"`
	Function wireFunction `json:"function"`
}

// wireFunction is the inside of a wireToolCall.
type wireFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// wireLogProb is one generated token with its log probability, and the
// alternatives Ollama reports when the request sets top_logprobs.
type wireLogProb struct {
	Token       string         `json:"token"`
	LogProb     float64        `json:"logprob"`
	Bytes       []int          `json:"bytes,omitempty"`
	TopLogProbs []wireTokenLog `json:"top_logprobs,omitempty"`
}

// wireTokenLog is one alternative token inside wireLogProb.
type wireTokenLog struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
	Bytes   []int   `json:"bytes,omitempty"`
}

// counters are the usage fields on the last object of every chat or generate
// reply. Durations are in nanoseconds, as Ollama sends them.
type counters struct {
	TotalDuration      int64 `json:"total_duration,omitempty"`
	LoadDuration       int64 `json:"load_duration,omitempty"`
	PromptEvalCount    int   `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64 `json:"prompt_eval_duration,omitempty"`
	EvalCount          int   `json:"eval_count,omitempty"`
	EvalDuration       int64 `json:"eval_duration,omitempty"`
}

// chatResponse is one object of a /api/chat reply: the only object when not
// streaming, or one line of the NDJSON stream. The embedded counters (a
// struct placed inside another without a field name) put their fields at the
// top level of the JSON object, where Ollama has them.
type chatResponse struct {
	Model      string        `json:"model"`
	CreatedAt  string        `json:"created_at"`
	Message    chatMessage   `json:"message"`
	Done       bool          `json:"done"`
	DoneReason string        `json:"done_reason,omitempty"`
	LogProbs   []wireLogProb `json:"logprobs,omitempty"`
	counters
}

// generateResponse is one object of a /api/generate reply.
type generateResponse struct {
	Model      string        `json:"model"`
	CreatedAt  string        `json:"created_at"`
	Response   string        `json:"response"`
	Done       bool          `json:"done"`
	DoneReason string        `json:"done_reason,omitempty"`
	LogProbs   []wireLogProb `json:"logprobs,omitempty"`
	counters
}

// embedResponse is the reply to /api/embed.
type embedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float32 `json:"embeddings"`
	TotalDuration   int64       `json:"total_duration"`
	LoadDuration    int64       `json:"load_duration"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// psModel is one entry in the /api/ps reply.
type psModel struct {
	Name          string    `json:"name"`
	Model         string    `json:"model"`
	Size          int64     `json:"size"`
	Digest        string    `json:"digest"`
	Details       psDetails `json:"details"`
	ExpiresAt     string    `json:"expires_at"`
	SizeVRAM      int64     `json:"size_vram"`
	ContextLength int       `json:"context_length"`
}

// psDetails describes a loaded model's format.
type psDetails struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}

// errorBody is how Ollama reports a failure, both as an HTTP error body and
// as the last line of a stream that fails part way.
type errorBody struct {
	Error string `json:"error"`
}
