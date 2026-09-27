// This file holds Ollama's own JSON shapes and the functions that convert
// between them and Meru's types. Nothing outside this package sees these
// types, so a change in Ollama's API stays inside this file and ollama.go.
//
// The field names follow Ollama's API reference for /api/chat, /api/embed,
// /api/show, /api/version and /api/ps.

package engine

import (
	"encoding/json"
	"strings"
	"time"
)

// chatRequest is the body of POST /api/chat.
//
// The text in backticks after each field is a struct tag: it names the JSON
// key, and `omitempty` leaves the key out when the field holds its zero value.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
	// KeepAlive is raw JSON because Ollama wants a number for "-1" (seconds)
	// and a string for "5m". See keepAliveJSON.
	KeepAlive json.RawMessage `json:"keep_alive,omitempty"`
	Options   *chatOptions    `json:"options,omitempty"`
	// Think is a pointer so nil (key left out, the model's default) differs
	// from false (thinking off).
	Think       *bool `json:"think,omitempty"`
	LogProbs    bool  `json:"logprobs,omitempty"`
	TopLogProbs int   `json:"top_logprobs,omitempty"`
}

// chatOptions is the "options" object: sampling settings for one call.
type chatOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
}

// chatMessage is one message in Ollama's format.
type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Thinking  string         `json:"thinking,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	// Images are pictures on a user message. Ollama wants each as a
	// base64 string, and encoding/json writes a []byte that way.
	Images [][]byte `json:"images,omitempty"`
}

// chatToolCall is one tool call as Ollama writes it: the name and arguments
// sit inside a "function" object.
type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Function chatToolFunction `json:"function"`
}

// chatToolFunction is the inside of a chatToolCall.
type chatToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// chatTool describes one tool on the request, in the OpenAI-style shape
// Ollama accepts: {"type": "function", "function": {...}}.
type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolSpec `json:"function"`
}

// chatToolSpec is the inside of a chatTool.
type chatToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// chatResponse is the reply from /api/chat: the whole reply when stream is
// false, or one line of the stream when it is true. The counters are only
// set when Done is true.
type chatResponse struct {
	Message    chatMessage `json:"message"`
	Done       bool        `json:"done"`
	DoneReason string      `json:"done_reason"`
	// Error is set when Ollama fails partway through a stream. It sends the
	// error as one more JSON line, because the 200 status has already gone.
	Error string `json:"error"`

	LogProbs []wireLogProb `json:"logprobs"`

	PromptEvalCount    int   `json:"prompt_eval_count"`
	EvalCount          int   `json:"eval_count"`
	LoadDuration       int64 `json:"load_duration"` // nanoseconds, like the other durations
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalDuration       int64 `json:"eval_duration"`
	TotalDuration      int64 `json:"total_duration"`
}

// wireLogProb is one generated token with its log probability and, when
// top_logprobs was set, the alternatives Ollama considered at that position.
type wireLogProb struct {
	Token       string           `json:"token"`
	LogProb     float64          `json:"logprob"`
	TopLogProbs []wireTopLogProb `json:"top_logprobs"`
}

// wireTopLogProb is one alternative token at a position.
type wireTopLogProb struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
}

// embedRequest is the body of POST /api/embed.
type embedRequest struct {
	Model     string          `json:"model"`
	Input     []string        `json:"input"`
	KeepAlive json.RawMessage `json:"keep_alive,omitempty"`
}

// embedResponse is the reply from /api/embed: one vector per input.
type embedResponse struct {
	Embeddings []Vector `json:"embeddings"`
}

// showRequest is the body of POST /api/show.
type showRequest struct {
	Model string `json:"model"`
}

// showResponse is the part of the reply from /api/show that Meru reads:
// what the model can do, such as ["completion", "vision", "tools"], its
// size and quantization, and model_info, whose keys depend on the model's
// architecture. The reply holds more, such as the license and the list of
// tensors, which the decoder skips.
type showResponse struct {
	Capabilities []string `json:"capabilities"`
	Details      struct {
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
	// json.RawMessage keeps each value undecoded, since details reads
	// only one of them.
	ModelInfo map[string]json.RawMessage `json:"model_info"`
}

// details turns the reply into ModelDetails. The context length sits
// under "<architecture>.context_length", such as "qwen3.context_length",
// so details finds the key by its ending. A missing or odd value leaves
// ContextLength at 0.
func (r showResponse) details() ModelDetails {
	d := ModelDetails{
		Capabilities:  r.Capabilities,
		ParameterSize: r.Details.ParameterSize,
		Quantization:  r.Details.QuantizationLevel,
	}
	for k, v := range r.ModelInfo {
		var n int
		if strings.HasSuffix(k, ".context_length") && json.Unmarshal(v, &n) == nil {
			d.ContextLength = n
		}
	}
	return d
}

// versionResponse is the reply from GET /api/version.
type versionResponse struct {
	Version string `json:"version"`
}

// psResponse is the reply from GET /api/ps: the models in memory now.
type psResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// tagsResponse is the reply from GET /api/tags: the models on disk, each
// with its size in bytes. The reply says more about each, such as its
// digest, which Meru doesn't read.
type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"models"`
}

// unloadRequest is the body of the POST /api/generate that unloads a
// model: no prompt, keep_alive 0, and stream off, so Ollama answers with
// one JSON object.
type unloadRequest struct {
	Model     string          `json:"model"`
	KeepAlive json.RawMessage `json:"keep_alive"`
	Stream    bool            `json:"stream"`
}

// errorResponse is the body Ollama sends with a non-2xx status.
type errorResponse struct {
	Error string `json:"error"`
}

// toChatMessages converts Meru messages to Ollama's format.
//
// make([]T, 0, n) creates an empty slice with room for n items, so append
// doesn't have to grow it.
func toChatMessages(msgs []Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		cm := chatMessage{Role: string(m.Role), Content: m.Content, ToolName: m.ToolName, Images: m.Images}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}") // Ollama wants an object, even an empty one
			}
			cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
				ID:       tc.ID,
				Function: chatToolFunction{Name: tc.Name, Arguments: args},
			})
		}
		out = append(out, cm)
	}
	return out
}

// toChatTools converts Meru tool specs to Ollama's format. It returns nil
// for no tools, so the "tools" key stays out of the request.
func toChatTools(tools []ToolSpec) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, chatTool{
			Type: "function",
			// Go can convert one struct type to another when their fields
			// match in name, type and order; struct tags don't count. So
			// chatToolSpec(t) copies all three fields in one step.
			Function: chatToolSpec(t),
		})
	}
	return out
}

// fromChatToolCalls converts Ollama's tool calls to Meru's.
func fromChatToolCalls(calls []chatToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

// fromLogProbs converts Ollama's per-token log probabilities to Meru's. It
// copies values and converts nothing; the router owns the arithmetic.
func fromLogProbs(lps []wireLogProb) []PositionLogProbs {
	if len(lps) == 0 {
		return nil
	}
	out := make([]PositionLogProbs, 0, len(lps))
	for _, lp := range lps {
		pos := PositionLogProbs{Chosen: TokenLogProb{Token: lp.Token, LogProb: lp.LogProb}}
		for _, alt := range lp.TopLogProbs {
			pos.Top = append(pos.Top, TokenLogProb(alt)) // same fields, so a plain conversion
		}
		out = append(out, pos)
	}
	return out
}

// usage copies Ollama's counters into a Usage. Ollama reports durations as
// whole nanoseconds, which is also what time.Duration counts, so the
// conversion is a plain type change.
func (r chatResponse) usage() Usage {
	return Usage{
		PromptTokens:       r.PromptEvalCount,
		OutputTokens:       r.EvalCount,
		LoadDuration:       time.Duration(r.LoadDuration),
		PromptEvalDuration: time.Duration(r.PromptEvalDuration),
		EvalDuration:       time.Duration(r.EvalDuration),
		TotalDuration:      time.Duration(r.TotalDuration),
	}
}
