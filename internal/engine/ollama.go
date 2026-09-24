// This file holds OllamaEngine, the one Engine Meru ships. It talks to
// Ollama's native HTTP API on loopback: /api/chat for answers, /api/embed for
// vectors, and /api/version plus /api/ps for Info.

package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/loopback"
)

// maxStreamLine is the longest single NDJSON line we accept from a stream,
// 8 MiB. A text chunk is tiny; a line carrying a large tool call can be long.
// The cap stops a broken server from filling memory.
const maxStreamLine = 8 << 20

// maxErrorBody is how much of a failed response's body we read to build the
// error message.
const maxErrorBody = 64 << 10

// OllamaEngine is the Engine backed by a local Ollama server. Build one with
// NewOllama; the zero value isn't usable.
//
// It holds no mutable state after NewOllama returns, so several goroutines
// may call it at once.
type OllamaEngine struct {
	baseURL    string          // for example "http://127.0.0.1:11434", no trailing slash
	keepAlive  json.RawMessage // keep_alive as Ollama wants it, or nil to leave it out
	embedModel string          // model Embed uses; empty makes Embed fail
	client     *http.Client
}

// This line checks at compile time that *OllamaEngine has every Engine
// method. It creates nothing at run time: `_` discards the value.
var _ Engine = (*OllamaEngine)(nil)

// NewOllama returns an engine that talks to the Ollama at baseURL.
//
// It fails when baseURL isn't loopback (see loopback.CheckURL), which is how
// Meru keeps model traffic on this machine, or when keepAlive is neither
// empty, a whole number of seconds ("-1" means "keep loaded forever"), nor a
// Go duration such as "5m".
//
// embedModel names the model Embed uses. client may be nil, which means a
// plain http.Client with no overall timeout: calls end when their context
// ends, because a long answer can take minutes to stream.
func NewOllama(baseURL, keepAlive, embedModel string, client *http.Client) (*OllamaEngine, error) {
	if err := loopback.CheckURL(baseURL); err != nil {
		return nil, fmt.Errorf("ollama base URL: %w", err)
	}
	ka, err := keepAliveJSON(keepAlive)
	if err != nil {
		return nil, err
	}

	// Copy the caller's client so we can change its redirect rule without
	// touching theirs. `*client` reads the struct the pointer points to.
	c := &http.Client{}
	if client != nil {
		copied := *client
		c = &copied
	}
	// Ollama never redirects. Refusing redirects means a bad proxy or server
	// can't bounce a request, with the prompt in it, to another host.
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("ollama sent a redirect; Meru doesn't follow redirects")
	}

	return &OllamaEngine{
		baseURL:    strings.TrimRight(baseURL, "/"),
		keepAlive:  ka,
		embedModel: embedModel,
		client:     c,
	}, nil
}

// keepAliveJSON turns config's keep_alive string into the JSON Ollama
// accepts. Ollama reads a JSON number as seconds and a JSON string as a Go
// duration; the string "-1" fails there with "missing unit", so whole
// numbers go out as numbers. Empty means "leave the key out".
func keepAliveJSON(s string) (json.RawMessage, error) {
	if s == "" {
		return nil, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return json.RawMessage(strconv.Itoa(n)), nil
	}
	if _, err := time.ParseDuration(s); err != nil {
		return nil, fmt.Errorf("ollama keep_alive %q: want whole seconds such as \"-1\" or a duration such as \"5m\"", s)
	}
	// json.Marshal of a string can't fail, so we drop its error with `_`.
	b, _ := json.Marshal(s)
	return b, nil
}

// APIError is a non-2xx reply from Ollama. Callers can find it with
// errors.As, for example to tell "model not found" (404) from other failures.
type APIError struct {
	Path       string // the API path, such as "/api/chat"
	StatusCode int
	// Message is Ollama's "error" text, or the raw body when it isn't JSON.
	Message string
}

// Error makes *APIError satisfy Go's built-in error interface.
//
// (e *APIError) is the receiver: the value the method is called on. A
// pointer receiver avoids copying the struct.
func (e *APIError) Error() string {
	return fmt.Sprintf("ollama %s: %d %s: %s", e.Path, e.StatusCode, http.StatusText(e.StatusCode), e.Message)
}

// Generate sends msgs to opts.Model through POST /api/chat and waits for the
// whole answer. It fails when the model is empty, the request fails, Ollama
// answers with a non-2xx status, or the reply isn't valid JSON.
//
// When opts.LogProbs is set it also turns the model's "thinking" off. The
// caller wants the probabilities of the answer's first tokens, and a
// thinking model would otherwise spend those tokens on hidden reasoning.
// The router relies on this (see docs/fast-router.md).
func (e *OllamaEngine) Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error) {
	body, err := e.chatBody(msgs, tools, opts, false)
	if err != nil {
		return Completion{}, err
	}
	resp, err := e.do(ctx, http.MethodPost, "/api/chat", body)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()

	var r chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Completion{}, fmt.Errorf("ollama /api/chat: decode reply: %w", ctxErr(ctx, err))
	}
	if r.Error != "" {
		return Completion{}, fmt.Errorf("ollama /api/chat: %s", r.Error)
	}
	return Completion{
		Text:       r.Message.Content,
		ToolCalls:  fromChatToolCalls(r.Message.ToolCalls),
		DoneReason: r.DoneReason,
		Usage:      r.usage(),
		LogProbs:   fromLogProbs(r.LogProbs),
	}, nil
}

// Stream sends msgs to opts.Model through POST /api/chat with streaming on,
// and returns the answer as a sequence of Deltas. Ollama writes NDJSON: one
// JSON object per line. The last Delta has Done set and carries Usage.
//
// Stream itself fails for the same reasons as Generate, before any text
// arrives. Errors after that (a dropped connection, a cancelled ctx, an
// error line from Ollama) come out of the sequence as its last item.
//
// The caller must range over the sequence, once. The HTTP response stays
// open until the loop ends or ctx is cancelled.
//
// Chunks with no text and no tool calls are skipped. A thinking model sends
// those while it reasons, and Delta has nowhere to put the reasoning.
func (e *OllamaEngine) Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error) {
	body, err := e.chatBody(msgs, tools, opts, true)
	if err != nil {
		return nil, err
	}
	resp, err := e.do(ctx, http.MethodPost, "/api/chat", body)
	if err != nil {
		return nil, err
	}

	used := false
	// An iter.Seq2 is a function that calls yield once per item. The
	// caller's loop body runs inside yield, and yield returns false when the
	// caller breaks out of the loop; then we must stop.
	seq := func(yield func(Delta, error) bool) {
		if used {
			yield(Delta{}, errors.New("ollama stream: already read; call Stream again"))
			return
		}
		used = true
		defer resp.Body.Close()

		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), maxStreamLine)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var r chatResponse
			if err := json.Unmarshal(line, &r); err != nil {
				yield(Delta{}, fmt.Errorf("ollama stream: decode line: %w", err))
				return
			}
			if r.Error != "" {
				yield(Delta{}, fmt.Errorf("ollama stream: %s", r.Error))
				return
			}
			d := Delta{Text: r.Message.Content, ToolCalls: fromChatToolCalls(r.Message.ToolCalls)}
			if r.Done {
				d.Done = true
				d.DoneReason = r.DoneReason
				d.Usage = r.usage()
				yield(d, nil)
				return
			}
			if d.Text == "" && len(d.ToolCalls) == 0 {
				continue
			}
			if !yield(d, nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(Delta{}, fmt.Errorf("ollama stream: read: %w", ctxErr(ctx, err)))
			return
		}
		// The body ended without a done line: Ollama died or the
		// connection dropped.
		yield(Delta{}, fmt.Errorf("ollama stream: ended before done: %w", ctxErr(ctx, io.ErrUnexpectedEOF)))
	}
	return seq, nil
}

// Embed turns each text into a vector with the engine's embedding model,
// through POST /api/embed. It returns one Vector per text, in order. No
// texts means no request and a nil result. It fails when no embedding model
// is set, the request fails, or Ollama returns the wrong number of vectors.
func (e *OllamaEngine) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if e.embedModel == "" {
		return nil, errors.New("ollama embed: no embedding model configured")
	}
	body, err := json.Marshal(embedRequest{Model: e.embedModel, Input: texts, KeepAlive: e.keepAlive})
	if err != nil {
		return nil, fmt.Errorf("ollama /api/embed: encode request: %w", err)
	}
	resp, err := e.do(ctx, http.MethodPost, "/api/embed", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("ollama /api/embed: decode reply: %w", ctxErr(ctx, err))
	}
	if len(r.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama /api/embed: sent %d texts, got %d vectors", len(texts), len(r.Embeddings))
	}
	return r.Embeddings, nil
}

// Info reports Ollama's version (GET /api/version) and the models it has in
// memory (GET /api/ps). It fails if either request fails.
func (e *OllamaEngine) Info(ctx context.Context) (ModelInfo, error) {
	v, err := e.version(ctx)
	if err != nil {
		return ModelInfo{}, err
	}
	var ps psResponse
	if err := e.getJSON(ctx, "/api/ps", &ps); err != nil {
		return ModelInfo{}, err
	}
	info := ModelInfo{Runtime: "ollama", RuntimeVersion: v}
	for _, m := range ps.Models {
		info.LoadedModels = append(info.LoadedModels, m.Name)
	}
	return info, nil
}

// version returns the version string from GET /api/version.
func (e *OllamaEngine) version(ctx context.Context) (string, error) {
	var v versionResponse
	if err := e.getJSON(ctx, "/api/version", &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", errors.New("ollama /api/version: reply has no version")
	}
	return v.Version, nil
}

// chatBody builds the JSON body for /api/chat. It fails when opts names no
// model.
func (e *OllamaEngine) chatBody(msgs []Message, tools []ToolSpec, opts Options, stream bool) ([]byte, error) {
	if opts.Model == "" {
		return nil, errors.New("ollama /api/chat: no model given")
	}
	req := chatRequest{
		Model:       opts.Model,
		Messages:    toChatMessages(msgs),
		Tools:       toChatTools(tools),
		Stream:      stream,
		KeepAlive:   e.keepAlive,
		LogProbs:    opts.LogProbs,
		TopLogProbs: opts.TopLogProbs,
	}
	if opts.Temperature != nil || opts.MaxTokens > 0 {
		// &chatOptions{...} builds the struct and takes its address, which
		// is what the pointer field wants.
		req.Options = &chatOptions{Temperature: opts.Temperature, NumPredict: opts.MaxTokens}
	}
	if opts.LogProbs {
		off := false
		req.Think = &off
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ollama /api/chat: encode request: %w", err)
	}
	return b, nil
}

// getJSON sends a GET to path and decodes the JSON reply into out.
//
// out has type any, Go's name for "a value of any type". json.Decode fills
// it through the pointer the caller passes.
func (e *OllamaEngine) getJSON(ctx context.Context, path string, out any) error {
	resp, err := e.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("ollama %s: decode reply: %w", path, ctxErr(ctx, err))
	}
	return nil
}

// do sends one request and returns the response when the status is 2xx. On
// any other status it reads the body, closes it and returns an *APIError.
// The caller must close the body of a response it gets back.
//
// http.NewRequestWithContext ties the request to ctx: cancelling ctx aborts
// the connection, including a stream in progress.
func (e *OllamaEngine) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, rd)
	if err != nil {
		return nil, fmt.Errorf("ollama %s: build request: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, apiError(path, resp)
	}
	return resp, nil
}

// apiError builds an *APIError from a failed response. Ollama sends
// {"error": "..."}; anything else goes into the message as raw text.
func apiError(path string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := strings.TrimSpace(string(raw))
	var er errorResponse
	if json.Unmarshal(raw, &er) == nil && er.Error != "" {
		msg = er.Error
	}
	if msg == "" {
		msg = "(empty body)"
	}
	return &APIError{Path: path, StatusCode: resp.StatusCode, Message: msg}
}

// ctxErr prefers ctx's own error over err when ctx has ended. A cancelled
// request surfaces as a read error such as "use of closed connection";
// returning context.Canceled lets callers test for it with errors.Is.
func ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%w (%v)", cerr, err)
	}
	return err
}
