// This file holds the JSON shapes of the Anthropic Messages API, which
// AnthropicEngine sends and reads, and the functions that turn Meru's
// messages into them. Only the fields Meru uses are here.

package engine

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// anRequest is the body of POST /messages.
type anRequest struct {
	Model       string      `json:"model"`
	System      string      `json:"system,omitempty"`
	Messages    []anMessage `json:"messages"`
	Tools       []anTool    `json:"tools,omitempty"`
	MaxTokens   int         `json:"max_tokens"` // the API requires it
	Stream      bool        `json:"stream,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
}

// anMessage is one message: a role, "user" or "assistant", and a list of
// blocks. The API has no system or tool role: the system prompt is its
// own field, and a tool's result is a block in a user message.
type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

// anBlock is one block of a message. Type says which fields it uses:
// "text" (Text), "image" (Source), "tool_use" (ID, Name, Input) or
// "tool_result" (ToolUseID, Content).
type anBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *anImageSource  `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   []anBlock       `json:"content,omitempty"`
}

// anImageSource holds a picture's bytes, base64-encoded, and its media
// type, such as "image/png".
type anImageSource struct {
	Type      string `json:"type"` // always "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// anTool offers one tool to the model.
type anTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anResponse is a whole reply.
type anResponse struct {
	Content    []anBlock `json:"content"`
	StopReason string    `json:"stop_reason"`
	Usage      anUsage   `json:"usage"`
}

// anUsage holds the token counts. A stream sends input_tokens at its start
// and output_tokens at its end.
type anUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anEvent is one event of a stream. Its type, which the "event:" line
// also gives, says which fields it fills:
//
//   - message_start: Message.Usage.InputTokens
//   - content_block_start: Index and ContentBlock, a text, tool_use or
//     thinking block
//   - content_block_delta: Index and Delta, with text (text_delta) or a
//     fragment of a tool call's arguments (input_json_delta)
//   - message_delta: Delta.StopReason and Usage.OutputTokens
//   - error: Error.Message
//
// Other types, such as ping, content_block_stop and message_stop, carry
// nothing Meru needs.
type anEvent struct {
	Type         string     `json:"type"`
	Index        int        `json:"index"`
	Message      anResponse `json:"message"`
	ContentBlock anBlock    `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage anUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// toAnMessages turns Meru's messages into the system prompt and the API's
// messages. System messages join into one prompt. A tool's result becomes
// a tool_result block, with any pictures it returned, in a user message.
// Messages of the same role in a row merge into one, as the API wants
// user and assistant to take turns. The API refuses an empty text block,
// so empty text is left out.
func toAnMessages(msgs []Message) (string, []anMessage) {
	msgs, results := toolCallIDs(msgs)
	var system []string
	var out []anMessage
	add := func(role string, blocks []anBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, anMessage{Role: role, Content: blocks})
	}
	for i, m := range msgs {
		switch m.Role {
		case RoleSystem:
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case RoleAssistant:
			var blocks []anBlock
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, c := range m.ToolCalls {
				blocks = append(blocks, anBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: arguments(c.Arguments)})
			}
			add("assistant", blocks)
		case RoleTool:
			inner := anImages(m.Images)
			if m.Content != "" {
				inner = append([]anBlock{{Type: "text", Text: m.Content}}, inner...)
			}
			add("user", []anBlock{{Type: "tool_result", ToolUseID: results[i], Content: inner}})
		default:
			// Pictures go before the text, as the API's guide suggests.
			blocks := anImages(m.Images)
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			add("user", blocks)
		}
	}
	return strings.Join(system, "\n\n"), out
}

// anImages returns one image block per picture.
func anImages(images [][]byte) []anBlock {
	var out []anBlock
	for _, img := range images {
		out = append(out, anBlock{Type: "image", Source: &anImageSource{
			Type: "base64", MediaType: imageType(img), Data: base64.StdEncoding.EncodeToString(img),
		}})
	}
	return out
}

// toAnTools turns Meru's tool specs into the API's, or nil for none. A
// tool with no schema gets the empty object schema the API requires.
func toAnTools(tools []ToolSpec) []anTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anTool, len(tools))
	for i, t := range tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out[i] = anTool{Name: t.Name, Description: t.Description, InputSchema: schema}
	}
	return out
}

// anDoneReason maps the API's stop_reason to Meru's: "length" when the
// answer hit max_tokens, "stop" for the end of an answer or a tool call.
// Anything else, such as "refusal", passes through.
func anDoneReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "", "end_turn", "tool_use", "stop_sequence":
		return "stop"
	default:
		return r
	}
}
