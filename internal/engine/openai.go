// This file holds OpenAIEngine, the Engine for a server that speaks the
// OpenAI chat completions API: POST <url>/chat/completions. Many model
// servers speak it, Ollama's own /v1 among them, so one engine covers
// them all. It answers, and Embed fails: the embedding model stays on the
// local Ollama. See docs/design/model-router.md.

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
	"time"
)

// chatPath is the chat completions path under the endpoint's URL.
const chatPath = "/chat/completions"

// OpenAIEngine is the Engine for one OpenAI-style endpoint and one model.
// Build one with NewOpenAI. It holds no state that changes, so several
// goroutines may call it at once.
type OpenAIEngine struct {
	api   *endpointAPI
	model string
}

// This line checks at compile time that *OpenAIEngine has every Engine
// method.
var _ Engine = (*OpenAIEngine)(nil)

// NewOpenAI returns the engine for cfg. It sends the key, when there is
// one, as "Authorization: Bearer <key>". It fails when cfg names no model
// or its URL isn't on this machine. client and log may be nil, as for
// NewOllama.
func NewOpenAI(cfg EndpointConfig, client *http.Client, log *slog.Logger) (*OpenAIEngine, error) {
	h := http.Header{}
	if cfg.Key != "" {
		h.Set("Authorization", "Bearer "+cfg.Key)
	}
	api, err := newEndpointAPI("openai", cfg, h, client, log)
	if err != nil {
		return nil, err
	}
	return &OpenAIEngine{api: api, model: cfg.Model}, nil
}

// Generate sends msgs and waits for the whole answer. It fails when the
// request fails, the server answers with a non-2xx status, or the reply
// isn't JSON with one choice. opts.Model is ignored: the engine answers
// with its own model, the one its [[endpoints]] entry names.
func (e *OpenAIEngine) Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error) {
	if opts.LogProbs {
		return Completion{}, ErrNoLogProbs
	}
	body, err := e.body(msgs, tools, opts, false)
	if err != nil {
		return Completion{}, err
	}
	start := time.Now()
	resp, span, err := e.api.post(ctx, chatPath, body)
	if err != nil {
		return Completion{}, err
	}
	defer span.End()
	defer resp.Body.Close()
	var r oaResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Completion{}, e.api.fail(ctx, span, start, chatPath, fmt.Errorf("endpoint %s: decode reply: %w", e.api.name, ctxErr(ctx, err)))
	}
	if len(r.Choices) == 0 {
		return Completion{}, e.api.fail(ctx, span, start, chatPath, fmt.Errorf("endpoint %s: the reply holds no answer", e.api.name))
	}
	c := r.Choices[0]
	out := Completion{
		Text:       c.Message.Content,
		ToolCalls:  fromOAToolCalls(c.Message.ToolCalls),
		DoneReason: oaDoneReason(c.FinishReason),
		Usage:      e.usage(r.Usage, start),
	}
	e.logDone(ctx, start, out.Usage, out.DoneReason)
	return out, nil
}

// Stream sends msgs with streaming on and returns the answer piece by
// piece. The server sends one "data: {json}" event per piece and
// "data: [DONE]" at the end. Text comes as it arrives; tool calls come in
// pieces, joined here by their index and handed out whole on the last
// Delta, as Ollama hands them out. The last Delta has Done set and carries
// the usage.
//
// Errors before the stream starts come back from Stream; after that, as
// the sequence's last item. The caller must range over the sequence once.
func (e *OpenAIEngine) Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error) {
	if opts.LogProbs {
		return nil, ErrNoLogProbs
	}
	body, err := e.body(msgs, tools, opts, true)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, span, err := e.api.post(ctx, chatPath, body)
	if err != nil {
		return nil, err
	}
	used := false
	seq := func(yield func(Delta, error) bool) {
		if used {
			yield(Delta{}, errors.New("openai stream: already read; call Stream again"))
			return
		}
		used = true
		defer span.End()
		defer resp.Body.Close()
		fail := func(err error) {
			yield(Delta{}, e.api.fail(ctx, span, start, chatPath, err))
		}

		// calls holds the tool calls being built, by their index in the
		// stream. A map, since a server may number them from any start.
		calls := map[int]*ToolCall{}
		args := map[int]string{}
		var reason string
		var usage *oaUsage
		first := true
		done := false // the server sent "data: [DONE]"
		for ev, err := range readSSE(resp.Body) {
			if err != nil {
				fail(fmt.Errorf("endpoint %s stream: read: %w", e.api.name, ctxErr(ctx, err)))
				return
			}
			if string(ev.Data) == "[DONE]" {
				done = true
				break
			}
			var r oaResponse
			if err := json.Unmarshal(ev.Data, &r); err != nil {
				fail(fmt.Errorf("endpoint %s stream: decode event: %w", e.api.name, err))
				return
			}
			// A server that fails mid-stream sends {"error": {...}}, which
			// decodes to no choices; the raw event says why.
			if len(r.Choices) == 0 && r.Usage == nil {
				if msg := streamError(ev.Data); msg != "" {
					fail(fmt.Errorf("endpoint %s stream: %w: %s", e.api.name, ErrModelOutput, msg))
					return
				}
			}
			if r.Usage != nil {
				usage = r.Usage
			}
			if len(r.Choices) == 0 {
				continue
			}
			c := r.Choices[0]
			if c.FinishReason != "" {
				reason = c.FinishReason
			}
			for _, tc := range c.Delta.ToolCalls {
				// Join the pieces of each call by index: the ID and name
				// come in the first piece, and the arguments in fragments
				// that may split anywhere, even inside a string.
				call, ok := calls[tc.Index]
				if !ok {
					call = &ToolCall{}
					calls[tc.Index] = call
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if call.Name == "" {
					call.Name = tc.Function.Name
				}
				args[tc.Index] += tc.Function.Arguments
			}
			if c.Delta.Content == "" {
				continue
			}
			if first {
				first = false
				e.api.log.DebugContext(ctx, "endpoint first token", "endpoint", e.api.name,
					"ms", time.Since(start).Milliseconds())
			}
			if !yield(Delta{Text: c.Delta.Content}, nil) {
				return
			}
		}
		if !done && reason == "" {
			// The body ended before the server said the answer was over:
			// it died or the connection dropped.
			fail(fmt.Errorf("endpoint %s stream: ended before done: %w", e.api.name, ctxErr(ctx, io.ErrUnexpectedEOF)))
			return
		}
		d := Delta{Done: true, DoneReason: oaDoneReason(reason), Usage: e.usage(usage, start)}
		// Hand the calls out in index order.
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
func (e *OpenAIEngine) Embed(context.Context, []Input) ([]Vector, error) {
	return nil, ErrNoEmbed
}

// Info names the API and the model; it asks the server nothing.
func (e *OpenAIEngine) Info(context.Context) (ModelInfo, error) {
	return ModelInfo{Runtime: "openai", LoadedModels: []string{e.model}}, nil
}

// body builds the JSON for one request. opts.NoThink has no standard
// field in this API, so the model thinks as it does by default.
func (e *OpenAIEngine) body(msgs []Message, tools []ToolSpec, opts Options, stream bool) ([]byte, error) {
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	req := oaRequest{
		Model: e.model, Messages: toOAMessages(msgs), Tools: toOATools(tools),
		Stream: stream, MaxTokens: maxTokens, Temperature: opts.Temperature,
	}
	if stream {
		req.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: encode request: %w", e.api.name, err)
	}
	return b, nil
}

// usage turns the API's token counts into Meru's. The API reports no
// timings, so TotalDuration is the time the call took here.
func (e *OpenAIEngine) usage(u *oaUsage, start time.Time) Usage {
	out := Usage{TotalDuration: time.Since(start)}
	if u != nil {
		out.PromptTokens = u.PromptTokens
		out.OutputTokens = u.CompletionTokens
	}
	return out
}

// logDone writes the debug line that closes a call.
func (e *OpenAIEngine) logDone(ctx context.Context, start time.Time, u Usage, reason string) {
	e.api.log.DebugContext(ctx, "endpoint done", "endpoint", e.api.name, "model", e.model,
		"ms", time.Since(start).Milliseconds(), "prompt_tokens", u.PromptTokens,
		"output_tokens", u.OutputTokens, "done_reason", reason)
}

// streamError returns the message of an {"error": {"message": ...}} event,
// or "" when data isn't one.
func streamError(data []byte) string {
	var er struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &er) != nil || er.Error == nil {
		return ""
	}
	if er.Error.Message == "" {
		return "the server reported an error"
	}
	return er.Error.Message
}
