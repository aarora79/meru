// This file holds Call, which runs one tool on its server, and the Result it
// returns. It also records the call's span.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/obs"
)

// ErrNotAllowed means the model asked for a tool that no allow list names,
// or a server that isn't configured. The Pool refuses it without contacting
// any server. dispatch records the call with the outcome "denied".
var ErrNotAllowed = errors.New("tool not allowed")

// ErrUnavailable means the tool is allowed but its server isn't running and
// couldn't be started. The error text says why.
var ErrUnavailable = errors.New("mcp server unavailable")

// Result is what one tool call returned.
type Result struct {
	// Text joins the server's text content, one block per line. Content of
	// other kinds (images, audio, resources) shows as a short placeholder
	// such as "[image image/png]", so the model knows something was there.
	Text string
	// Structured is the server's structured content as raw JSON, or nil when
	// it sent none.
	Structured json.RawMessage
	// IsError is true when the server ran the tool and reports that it
	// failed. That is a normal result: the model reads Text to learn why.
	IsError bool
	// Duration is how long the call took, from sending to the answer.
	Duration time.Duration
}

// Call runs the tool with the namespaced name ("<server>.<tool>") and the
// model's JSON arguments. It fails with ErrNotAllowed, before contacting any
// server, when the tool isn't in its server's allow list. It fails with
// ErrUnavailable when the server is down and won't restart.
//
// The call stops when ctx ends or when the server's timeout passes; the
// error then wraps context.Canceled or context.DeadlineExceeded, and the SDK
// tells the server to stop working on it. A server that reports a tool
// failure isn't an error here: Call returns a Result with IsError set.
//
// Call records a span named "tools/call <tool>", following the OpenTelemetry
// MCP semantic conventions. It carries the arguments and result text only
// when capture_content is on.
func (p *Pool) Call(ctx context.Context, name string, args json.RawMessage) (res Result, err error) {
	s, tool, known := p.lookup(name)
	allowed := known && s.allow[tool]

	ctx, span := startCallSpan(ctx, name, tool, s, allowed)
	// This deferred function runs when Call returns. Because the results
	// are named (res, err), it sees the values Call is returning.
	defer func() {
		endCallSpan(ctx, span, res, err)
		span.End()
	}()

	if !allowed {
		return Result{}, fmt.Errorf("%w: %q", ErrNotAllowed, name)
	}
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("gen_ai.tool.call.arguments", string(args)))
	}

	arguments, err := callArguments(args)
	if err != nil {
		return Result{}, fmt.Errorf("call %s: %w", name, err)
	}

	cs, err := p.sessionFor(ctx, s)
	if err != nil {
		return Result{}, err
	}
	span.SetAttributes(sessionAttributes(s.cfg, cs)...)

	// callCtx is a separate variable so the deferred span code above still
	// sees the caller's ctx, not this one, which cancel ends on return.
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.callTimeout())
	defer cancel()

	start := time.Now()
	out, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	elapsed := time.Since(start)
	if err != nil {
		// The SDK may return its own error when ctx ends. Wrapping ctx.Err()
		// lets the caller test for context.Canceled or DeadlineExceeded
		// with errors.Is.
		if ctxErr := callCtx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = fmt.Errorf("%w (%w)", ctxErr, err)
		}
		s.markFailed(cs, err)
		return Result{Duration: elapsed}, fmt.Errorf("call %s: %w", name, err)
	}

	s.markOK()
	res = toResult(out)
	res.Duration = elapsed
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("gen_ai.tool.call.result", res.Text))
	}
	return res, nil
}

// callArguments checks that args is a JSON object (or empty, meaning no
// arguments) and returns it ready for the SDK. MCP requires an object, and a
// model sometimes writes a bare string or broken JSON.
func callArguments(args json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(args) || !strings.HasPrefix(trimmed, "{") {
		return nil, errors.New("arguments must be a JSON object")
	}
	return args, nil
}

// toResult turns the SDK's result into a Result. A type switch (`switch
// c := x.(type)`) runs the branch that matches the value's concrete type,
// with c already converted to that type.
func toResult(out *mcp.CallToolResult) Result {
	var parts []string
	for _, content := range out.Content {
		switch c := content.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.ImageContent:
			parts = append(parts, "[image "+c.MIMEType+"]")
		case *mcp.AudioContent:
			parts = append(parts, "[audio "+c.MIMEType+"]")
		case *mcp.ResourceLink:
			parts = append(parts, "[resource "+c.URI+"]")
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, "[resource "+c.Resource.URI+"]")
			}
		default:
			parts = append(parts, "[unsupported content]")
		}
	}
	res := Result{Text: strings.Join(parts, "\n"), IsError: out.IsError}
	if out.StructuredContent != nil {
		if b, err := json.Marshal(out.StructuredContent); err == nil {
			res.Structured = b
		}
	}
	return res
}

// startCallSpan starts the span for one call. The names come from the
// OpenTelemetry MCP semantic conventions: the span is named
// "{mcp.method.name} {gen_ai.tool.name}". meru.tool.server and
// meru.tool.allowed are Meru's own (ARCHITECTURE.md, "Traces"). For a name
// the Pool doesn't know, the span uses the whole name as the tool name.
func startCallSpan(ctx context.Context, name, tool string, s *server, allowed bool) (context.Context, trace.Span) {
	serverName := ""
	if s != nil {
		serverName = s.cfg.Name
	} else {
		tool = name
	}
	attrs := []attribute.KeyValue{
		attribute.String("mcp.method.name", "tools/call"),
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", tool),
		attribute.String("meru.tool.server", serverName),
		attribute.Bool("meru.tool.allowed", allowed),
	}
	return obs.Tracer().Start(ctx, "tools/call "+tool,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// sessionAttributes returns the span attributes that describe the
// connection: the transport, the server's address for HTTP, and the MCP
// session and protocol version when the server set them.
func sessionAttributes(cfg ServerConfig, cs *mcp.ClientSession) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if cfg.URL == "" {
		// The conventions call a stdio connection a "pipe".
		attrs = append(attrs, attribute.String("network.transport", "pipe"))
	} else {
		attrs = append(attrs,
			attribute.String("network.transport", "tcp"),
			attribute.String("network.protocol.name", "http"))
		if u, err := url.Parse(cfg.URL); err == nil {
			host, port, splitErr := net.SplitHostPort(u.Host)
			if splitErr != nil {
				host = u.Hostname()
			}
			attrs = append(attrs, attribute.String("server.address", host))
			if n, err := strconv.Atoi(port); err == nil {
				attrs = append(attrs, attribute.Int("server.port", n))
			}
		}
	}
	if id := cs.ID(); id != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", id))
	}
	if init := cs.InitializeResult(); init != nil {
		attrs = append(attrs, attribute.String("mcp.protocol.version", init.ProtocolVersion))
	}
	return attrs
}

// endCallSpan sets the span's outcome. The conventions name error.type
// "tool_error" when the server reports a tool failure; Meru uses "denied" for
// a tool outside the allow list, "unavailable" for a server that is down,
// and the JSON-RPC error code (rpc.response.status_code) when the server
// answered with a protocol error. A cancelled call gets a "cancelled" event
// instead of an error status, as every other Meru span does.
func endCallSpan(ctx context.Context, span trace.Span, res Result, err error) {
	if err == nil {
		if res.IsError {
			span.SetAttributes(attribute.String("error.type", "tool_error"))
			span.SetStatus(codes.Error, "tool reported an error")
		}
		return
	}
	var rpcErr *jsonrpc.Error
	switch {
	case errors.Is(err, ErrNotAllowed):
		span.SetAttributes(attribute.String("error.type", "denied"))
	case errors.Is(err, ErrUnavailable):
		span.SetAttributes(attribute.String("error.type", "unavailable"))
	case errors.Is(err, context.DeadlineExceeded):
		span.SetAttributes(attribute.String("error.type", "timeout"))
	case errors.As(err, &rpcErr):
		span.SetAttributes(
			attribute.String("error.type", fmt.Sprint(rpcErr.Code)),
			attribute.String("rpc.response.status_code", fmt.Sprint(rpcErr.Code)))
	}
	obs.EndSpanErr(ctx, span, err)
}
