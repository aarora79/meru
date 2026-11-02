// This file holds what the OpenAI and Anthropic engines share: the HTTP
// client for an endpoint, one request with its span and its error, the
// reader for a server-sent event stream, and the pairing of tool calls
// with their results. See docs/design/model-router.md.

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
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/loopback"
	"github.com/aarora79/meru/internal/obs"
)

// ErrNoEmbed is what Embed returns on an endpoint engine. The embedding
// model always runs on the local Ollama, and config refuses an endpoint
// for it, so nothing should reach this.
var ErrNoEmbed = errors.New("an endpoint doesn't embed; the embedding model runs on the local Ollama")

// ErrNoLogProbs is what Generate returns on an endpoint engine asked for
// token probabilities. Only the router asks for them, and it runs on the
// fast model, which config keeps on the local Ollama.
var ErrNoLogProbs = errors.New("an endpoint doesn't report token probabilities; the router runs on the local Ollama")

// defaultMaxTokens caps an answer when the caller sets no cap. The
// Anthropic API refuses a request without one, and a cap stops a runaway
// answer on any server.
const defaultMaxTokens = 8192

// EndpointConfig is what an endpoint engine needs: where the server is,
// the key, and the model. merud fills it from a config.Endpoint, with the
// key already read from secrets.toml.
type EndpointConfig struct {
	Name  string // the [[endpoints]] name, for errors and logs
	URL   string // the API's base, such as http://127.0.0.1:11434/v1
	Key   string // the API key itself, or "" for none; never logged
	Model string // the model name the server knows
}

// endpointAPI sends requests to one endpoint. Both endpoint engines hold
// one; it does what OllamaEngine's do and fail do for Ollama.
type endpointAPI struct {
	name    string // the endpoint's name, such as "lab"
	kind    string // "openai" or "anthropic", for spans and errors
	base    string // the URL with no trailing slash
	headers http.Header
	client  *http.Client
	log     *slog.Logger
}

// newEndpointAPI checks cfg and returns the API for it. headers go on
// every request; they hold the key, so nothing here ever logs them.
//
// For now an endpoint must be on this machine, so it fails when the URL
// isn't loopback, the same check NewOllama makes (AGENTS.md,
// non-negotiable 1). client may be nil, for a plain http.Client with no
// overall timeout: a long answer can take minutes to stream, so calls end
// when their context ends.
func newEndpointAPI(kind string, cfg EndpointConfig, headers http.Header, client *http.Client, log *slog.Logger) (*endpointAPI, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("%s endpoint %s: no model given", kind, cfg.Name)
	}
	if err := loopback.CheckURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("%s endpoint %s: %w", kind, cfg.Name, err)
	}
	c := &http.Client{}
	if client != nil {
		copied := *client
		c = &copied
	}
	// A redirect could carry the prompt to another host, so none is
	// followed, as with Ollama.
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("%s endpoint %s sent a redirect; Meru doesn't follow redirects", kind, cfg.Name)
	}
	if log == nil {
		log = obs.Discard()
	}
	return &endpointAPI{
		name: cfg.Name, kind: kind, base: strings.TrimRight(cfg.URL, "/"),
		headers: headers, client: c, log: log,
	}, nil
}

// EndpointError is a non-2xx reply from an endpoint. Callers can find it
// with errors.As to tell a refused key (401) or a rate limit (429) from
// other failures.
type EndpointError struct {
	Endpoint   string // the [[endpoints]] name
	Path       string // such as "/chat/completions"
	StatusCode int
	// Message is the server's own error text, or the start of the raw body
	// when it isn't JSON in a form we know.
	Message string
}

// Error makes *EndpointError an error. A 401 and a 429 get a sentence that
// says what to do.
func (e *EndpointError) Error() string {
	msg := fmt.Sprintf("endpoint %s %s: %d %s: %s", e.Endpoint, e.Path, e.StatusCode,
		http.StatusText(e.StatusCode), e.Message)
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		msg += fmt.Sprintf("; check the api_key entry for %s in secrets.toml", e.Endpoint)
	case http.StatusTooManyRequests:
		msg += "; the server is limiting requests, so try again in a minute"
	}
	return msg
}

// post sends body to path and returns the response when the status is
// 2xx, with the span that covers the call; the caller ends the span and
// closes the body. On any other status it reads the server's message,
// ends the span and returns an *EndpointError.
func (a *endpointAPI) post(ctx context.Context, path string, body []byte) (*http.Response, trace.Span, error) {
	start := time.Now()
	ctx, span := obs.Tracer().Start(ctx, http.MethodPost+" "+path,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.request.method", http.MethodPost),
			attribute.String("url.full", a.base+path),
			attribute.Int("http.request.body.size", len(body)),
			attribute.String("meru.endpoint", a.name),
		))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, a.fail(ctx, span, start, path, fmt.Errorf("endpoint %s %s: build request: %w", a.name, path, err))
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range a.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil, a.fail(ctx, span, start, path, fmt.Errorf("endpoint %s %s: %w", a.name, path, err))
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, nil, a.fail(ctx, span, start, path, a.apiError(path, resp))
	}
	a.log.DebugContext(ctx, "endpoint http", "endpoint", a.name, "api", a.kind, "path", path,
		"status", resp.StatusCode, "headers_ms", time.Since(start).Milliseconds())
	return resp, span, nil
}

// fail logs a failed call at debug level, ends its span with the error,
// and returns err, as OllamaEngine.fail does.
func (a *endpointAPI) fail(ctx context.Context, span trace.Span, start time.Time, path string, err error) error {
	obs.EndSpanErr(ctx, span, err)
	span.End()
	a.log.DebugContext(ctx, "endpoint error", "endpoint", a.name, "path", path,
		"ms", time.Since(start).Milliseconds(), "err", err)
	return err
}

// apiError builds an *EndpointError from a failed response. Both APIs
// send {"error": {"message": "..."}}; anything else goes in as the start
// of the raw text.
func (a *endpointAPI) apiError(path string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := strings.TrimSpace(string(raw))
	var er struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &er) == nil && er.Error.Message != "" {
		msg = er.Error.Message
	}
	if msg == "" {
		msg = "(empty body)"
	}
	// A server may answer with a whole HTML page; the first line or so says
	// enough.
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return &EndpointError{Endpoint: a.name, Path: path, StatusCode: resp.StatusCode, Message: msg}
}

// sseEvent is one event from a server-sent event stream: its type from an
// "event:" line, which the Anthropic API sends and the OpenAI API doesn't,
// and its data from the "data:" lines.
type sseEvent struct {
	Type string
	Data []byte
}

// readSSE returns the events in body, a text/event-stream: lines of
// "field: value", with a blank line after each event. It yields each
// event that has data, then stops at the end of the body. An error, such
// as a dropped connection, comes out as the last item.
//
// See docs/coding-notes/go-basics/server-sent-events.md. bufio.Scanner
// reads one line at a time; Buffer lets a line grow to maxStreamLine, as a
// tool call's arguments can make one long.
func readSSE(body io.Reader) iter.Seq2[sseEvent, error] {
	return func(yield func(sseEvent, error) bool) {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64<<10), maxStreamLine)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				// A blank line ends the event.
				if len(ev.Data) > 0 && !yield(ev, nil) {
					return
				}
				ev = sseEvent{}
				continue
			}
			field, value, _ := bytes.Cut(line, []byte(":"))
			// One space after the colon belongs to the format, not the value.
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				ev.Type = string(value)
			case "data":
				if len(ev.Data) > 0 {
					ev.Data = append(ev.Data, '\n')
				}
				ev.Data = append(ev.Data, value...)
			}
			// Other fields (id, retry) and comments (a line that starts
			// with ":") don't matter here.
		}
		if err := sc.Err(); err != nil {
			yield(sseEvent{}, err)
			return
		}
		if len(ev.Data) > 0 {
			yield(ev, nil)
		}
	}
}

// toolCallIDs gives every tool call in msgs an ID, and returns the ID of
// the call each tool result answers, by message index. Both APIs tie a
// result to its call by ID, while Meru's messages carry the tool's name
// and come in call order (see agent.runRound). A call that came back with
// no ID gets "call_<message>_<n>"; the server only needs the two to match.
//
// It returns a copy of msgs, so the caller's history keeps its own IDs.
func toolCallIDs(msgs []Message) ([]Message, map[int]string) {
	out := make([]Message, len(msgs))
	copy(out, msgs)
	results := map[int]string{}
	var pending []string // IDs of the last assistant message's calls not yet answered
	for i, m := range out {
		switch m.Role {
		case RoleAssistant:
			pending = nil
			if len(m.ToolCalls) == 0 {
				continue
			}
			calls := make([]ToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				if c.ID == "" {
					c.ID = fmt.Sprintf("call_%d_%d", i, j)
				}
				calls[j] = c
				pending = append(pending, c.ID)
			}
			out[i].ToolCalls = calls
		case RoleTool:
			if len(pending) > 0 {
				results[i] = pending[0]
				pending = pending[1:]
			} else {
				// A result with no call before it, which the agent doesn't
				// send. It still needs an ID the server accepts.
				results[i] = fmt.Sprintf("call_%d", i)
			}
		}
	}
	return out, results
}

// imageType returns the media type of a picture's bytes, such as
// "image/png", for an API that wants it named. Meru only attaches PNG,
// JPEG, GIF and WebP files; http.DetectContentType knows all four.
func imageType(b []byte) string {
	return http.DetectContentType(b)
}

// arguments returns a tool call's arguments as JSON, with "{}" for none:
// both APIs want an object even when the tool takes no arguments.
func arguments(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}
