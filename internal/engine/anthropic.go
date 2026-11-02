// This file holds AnthropicEngine, the Engine for a server that speaks the
// Anthropic Messages API: POST <url>/messages. It answers, and Embed
// fails: the API has no embeddings, and the embedding model stays on the
// local Ollama anyway. See docs/design/model-router.md.

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// messagesPath is the Messages path under the endpoint's URL.
const messagesPath = "/messages"

// anthropicVersion is the API version Meru speaks, sent on every request
// as the API requires.
const anthropicVersion = "2023-06-01"

// AnthropicEngine is the Engine for one Anthropic-style endpoint and one
// model. Build one with NewAnthropic. It holds no state that changes, so
// several goroutines may call it at once.
type AnthropicEngine struct {
	api   *endpointAPI
	model string
}

// This line checks at compile time that *AnthropicEngine has every Engine
// method.
var _ Engine = (*AnthropicEngine)(nil)

// NewAnthropic returns the engine for cfg. It sends the key, when there
// is one, as "x-api-key". It fails when cfg names no model or its URL
// isn't on this machine. client and log may be nil, as for NewOllama.
func NewAnthropic(cfg EndpointConfig, client *http.Client, log *slog.Logger) (*AnthropicEngine, error) {
	h := http.Header{}
	h.Set("anthropic-version", anthropicVersion)
	if cfg.Key != "" {
		h.Set("x-api-key", cfg.Key)
	}
	api, err := newEndpointAPI("anthropic", cfg, h, client, log)
	if err != nil {
		return nil, err
	}
	return &AnthropicEngine{api: api, model: cfg.Model}, nil
}

// Generate sends msgs and waits for the whole answer. It fails when the
// request fails, the server answers with a non-2xx status, or the reply
// isn't JSON. opts.Model is ignored: the engine answers with its own
// model.
func (e *AnthropicEngine) Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error) {
	if opts.LogProbs {
		return Completion{}, ErrNoLogProbs
	}
	body, err := e.body(msgs, tools, opts, false)
	if err != nil {
		return Completion{}, err
	}
	start := time.Now()
	resp, span, err := e.api.post(ctx, messagesPath, body)
	if err != nil {
		return Completion{}, err
	}
	defer span.End()
	defer resp.Body.Close()
	var r anResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Completion{}, e.api.fail(ctx, span, start, messagesPath, fmt.Errorf("endpoint %s: decode reply: %w", e.api.name, ctxErr(ctx, err)))
	}
	var out Completion
	var text strings.Builder
	for _, b := range r.Content {
		// A thinking block holds the model's reasoning; like Ollama's
		// thinking, it stays out of the answer.
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: arguments(b.Input)})
		}
	}
	out.Text = text.String()
	out.DoneReason = anDoneReason(r.StopReason)
	out.Usage = Usage{PromptTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens, TotalDuration: time.Since(start)}
	e.logDone(ctx, start, out.Usage, out.DoneReason)
	return out, nil
}

// Stream sends msgs with streaming on and returns the answer piece by
// piece. The server sends typed events (see anEvent): text arrives as it
// comes, a tool call's arguments arrive as fragments of JSON joined here
// by block index, and the calls go out whole on the last Delta, as Ollama
// hands them out. The last Delta has Done set and carries the usage.
//
// Errors before the stream starts come back from Stream; after that, as
// the sequence's last item. An error event, such as an overloaded server,
// wraps ErrModelOutput, since asking again can work. The caller must range
// over the sequence once.
func (e *AnthropicEngine) Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error) {
	if opts.LogProbs {
		return nil, ErrNoLogProbs
	}
	body, err := e.body(msgs, tools, opts, true)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, span, err := e.api.post(ctx, messagesPath, body)
	if err != nil {
		return nil, err
	}
	used := false
	seq := func(yield func(Delta, error) bool) {
		if used {
			yield(Delta{}, errors.New("anthropic stream: already read; call Stream again"))
			return
		}
		used = true
		defer span.End()
		defer resp.Body.Close()
		fail := func(err error) {
			yield(Delta{}, e.api.fail(ctx, span, start, messagesPath, err))
		}

		calls := map[int]*ToolCall{} // tool_use blocks by their index
		args := map[int]string{}     // their arguments so far
		var usage Usage
		var reason string
		first, stopped := true, false
		for ev, err := range readSSE(resp.Body) {
			if err != nil {
				fail(fmt.Errorf("endpoint %s stream: read: %w", e.api.name, ctxErr(ctx, err)))
				return
			}
			var a anEvent
			if err := json.Unmarshal(ev.Data, &a); err != nil {
				fail(fmt.Errorf("endpoint %s stream: decode event: %w", e.api.name, err))
				return
			}
			switch a.Type {
			case "message_start":
				usage.PromptTokens = a.Message.Usage.InputTokens
			case "content_block_start":
				if a.ContentBlock.Type == "tool_use" {
					calls[a.Index] = &ToolCall{ID: a.ContentBlock.ID, Name: a.ContentBlock.Name}
				}
			case "content_block_delta":
				switch a.Delta.Type {
				case "text_delta":
					if a.Delta.Text == "" {
						continue
					}
					if first {
						first = false
						e.api.log.DebugContext(ctx, "endpoint first token", "endpoint", e.api.name,
							"ms", time.Since(start).Milliseconds())
					}
					if !yield(Delta{Text: a.Delta.Text}, nil) {
						return
					}
				case "input_json_delta":
					args[a.Index] += a.Delta.PartialJSON
				}
				// thinking_delta and signature_delta carry the model's
				// reasoning, which stays out of the answer.
			case "message_delta":
				if a.Delta.StopReason != "" {
					reason = a.Delta.StopReason
				}
				usage.OutputTokens = a.Usage.OutputTokens
			case "message_stop":
				stopped = true
			case "error":
				msg := "the server reported an error"
				if a.Error != nil && a.Error.Message != "" {
					msg = a.Error.Message
				}
				fail(fmt.Errorf("endpoint %s stream: %w: %s", e.api.name, ErrModelOutput, msg))
				return
			}
			if stopped {
				break
			}
		}
		if !stopped {
			fail(fmt.Errorf("endpoint %s stream: ended before done: %w", e.api.name, ctxErr(ctx, io.ErrUnexpectedEOF)))
			return
		}
		usage.TotalDuration = time.Since(start)
		d := Delta{Done: true, DoneReason: anDoneReason(reason), Usage: usage}
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			c := *calls[i]
			c.Arguments = arguments(json.RawMessage(args[i]))
			d.ToolCalls = append(d.ToolCalls, c)
		}
		e.logDone(ctx, start, d.Usage, d.DoneReason)
		yield(d, nil)
	}
	return seq, nil
}

// Embed always fails with ErrNoEmbed.
func (e *AnthropicEngine) Embed(context.Context, []Input) ([]Vector, error) {
	return nil, ErrNoEmbed
}

// Info names the API and the model; it asks the server nothing.
func (e *AnthropicEngine) Info(context.Context) (ModelInfo, error) {
	return ModelInfo{Runtime: "anthropic", LoadedModels: []string{e.model}}, nil
}

// body builds the JSON for one request. The API requires max_tokens, so a
// call with no cap gets defaultMaxTokens. opts.NoThink needs nothing:
// the API thinks only when a request asks it to, and Meru never asks.
func (e *AnthropicEngine) body(msgs []Message, tools []ToolSpec, opts Options, stream bool) ([]byte, error) {
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	system, messages := toAnMessages(msgs)
	req := anRequest{
		Model: e.model, System: system, Messages: messages, Tools: toAnTools(tools),
		MaxTokens: maxTokens, Stream: stream, Temperature: opts.Temperature,
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: encode request: %w", e.api.name, err)
	}
	return b, nil
}

// logDone writes the debug line that closes a call.
func (e *AnthropicEngine) logDone(ctx context.Context, start time.Time, u Usage, reason string) {
	e.api.log.DebugContext(ctx, "endpoint done", "endpoint", e.api.name, "model", e.model,
		"ms", time.Since(start).Milliseconds(), "prompt_tokens", u.PromptTokens,
		"output_tokens", u.OutputTokens, "done_reason", reason)
}
