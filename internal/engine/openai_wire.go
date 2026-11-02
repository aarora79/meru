// This file holds the JSON shapes of the OpenAI chat completions API, which
// OpenAIEngine sends and reads, and the functions that turn Meru's
// messages into them. Only the fields Meru uses are here: encoding/json
// skips the rest of a reply.

package engine

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// oaRequest is the body of POST /chat/completions.
type oaRequest struct {
	Model       string      `json:"model"`
	Messages    []oaMessage `json:"messages"`
	Tools       []oaTool    `json:"tools,omitempty"`
	Stream      bool        `json:"stream,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
	// StreamOptions asks for the token counts on the stream's last event;
	// without it, a stream reports none.
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
}

// oaStreamOptions holds stream_options.include_usage.
type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// oaMessage is one message. Content is a plain string, or a list of parts
// when the message carries pictures; any is Go's "value of any type", so
// the field holds either and encoding/json writes whichever it holds.
type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

// oaPart is one part of a message's content: text, or a picture as a
// data: URL.
type oaPart struct {
	Type     string      `json:"type"` // "text" or "image_url"
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

// oaImageURL holds a picture's URL; Meru sends a data: URL with the bytes.
type oaImageURL struct {
	URL string `json:"url"`
}

// oaTool offers one tool to the model.
type oaTool struct {
	Type     string         `json:"type"` // always "function"
	Function oaToolFunction `json:"function"`
}

// oaToolFunction describes the tool: its name, what it does and its
// arguments' JSON Schema.
type oaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// oaToolCall is the model asking for a tool. In a stream, Index says which
// call a piece belongs to: the ID and name come once, and the arguments
// arrive as fragments of one JSON string.
type oaToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function oaCallFunction `json:"function"`
}

// oaCallFunction names the tool and holds its arguments, which this API
// sends as a JSON string, not as an object.
type oaCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// oaResponse is a whole reply, and also one event of a stream: a stream
// sends Delta where a whole reply sends Message.
type oaResponse struct {
	Choices []oaChoice `json:"choices"`
	Usage   *oaUsage   `json:"usage"`
}

// oaChoice is one answer. Meru asks for one, so it reads Choices[0].
type oaChoice struct {
	Message      oaReplyMessage `json:"message"`
	Delta        oaReplyMessage `json:"delta"`
	FinishReason string         `json:"finish_reason"`
}

// oaReplyMessage is the model's message, or a piece of it. Some servers
// add the model's reasoning in fields such as "reasoning_content"; Meru
// reads none of them, so reasoning never reaches the answer.
type oaReplyMessage struct {
	Content   string       `json:"content"`
	ToolCalls []oaToolCall `json:"tool_calls"`
}

// oaUsage holds the token counts.
type oaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// toOAMessages turns Meru's messages into the API's. A tool result goes as
// a "tool" message tied to its call by ID. A "tool" message can't carry a
// picture, so pictures a tool returned, as view_image does, follow in one
// "user" message after the round's results.
func toOAMessages(msgs []Message) []oaMessage {
	msgs, results := toolCallIDs(msgs)
	out := make([]oaMessage, 0, len(msgs))
	var toolImages [][]byte // pictures from the current round's results
	flush := func() {
		if len(toolImages) > 0 {
			out = append(out, oaMessage{Role: string(RoleUser),
				Content: oaParts("The pictures the tools above returned.", toolImages)})
			toolImages = nil
		}
	}
	for i, m := range msgs {
		if m.Role != RoleTool {
			flush()
		}
		switch m.Role {
		case RoleTool:
			out = append(out, oaMessage{Role: string(RoleTool), Content: m.Content, ToolCallID: results[i]})
			toolImages = append(toolImages, m.Images...)
		case RoleAssistant:
			om := oaMessage{Role: string(RoleAssistant), Content: m.Content}
			for _, c := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, oaToolCall{ID: c.ID, Type: "function",
					Function: oaCallFunction{Name: c.Name, Arguments: string(arguments(c.Arguments))}})
			}
			out = append(out, om)
		default:
			var content any = m.Content
			if len(m.Images) > 0 {
				content = oaParts(m.Content, m.Images)
			}
			out = append(out, oaMessage{Role: string(m.Role), Content: content})
		}
	}
	flush()
	return out
}

// oaParts returns text and pictures as content parts, the text first.
func oaParts(text string, images [][]byte) []oaPart {
	parts := []oaPart{{Type: "text", Text: text}}
	for _, img := range images {
		url := "data:" + imageType(img) + ";base64," + base64.StdEncoding.EncodeToString(img)
		parts = append(parts, oaPart{Type: "image_url", ImageURL: &oaImageURL{URL: url}})
	}
	return parts
}

// toOATools turns Meru's tool specs into the API's, or nil for none.
func toOATools(tools []ToolSpec) []oaTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]oaTool, len(tools))
	for i, t := range tools {
		// The two structs have the same fields, so Go converts one to the
		// other; a conversion ignores the struct tags, which differ.
		out[i] = oaTool{Type: "function", Function: oaToolFunction(t)}
	}
	return out
}

// fromOAToolCalls turns the calls of a whole reply into Meru's.
func fromOAToolCalls(calls []oaToolCall) []ToolCall {
	var out []ToolCall
	for _, c := range calls {
		out = append(out, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: arguments(json.RawMessage(c.Function.Arguments))})
	}
	return out
}

// oaDoneReason maps the API's finish_reason to Meru's: "length" when the
// answer hit its token cap, which the agent reads as a cut-off answer, and
// "stop" for the end of an answer or a tool call. Anything else, such as
// "content_filter", passes through.
func oaDoneReason(r string) string {
	switch strings.ToLower(r) {
	case "length":
		return "length"
	case "", "stop", "tool_calls", "function_call":
		return "stop"
	default:
		return r
	}
}
