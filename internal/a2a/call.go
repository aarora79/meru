// This file holds Call, which sends the model's message to an agent and
// turns the agent's reply into a dispatch.Result. It also records the
// call's span.

package a2a

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

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	sdklog "github.com/a2aproject/a2a-go/v2/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/obs"
)

// cancelTimeout caps the request that asks an agent to stop a task after
// the call's context ended. The turn is already over, so Meru doesn't wait
// long for the agent to agree.
const cancelTimeout = 2 * time.Second

// Call sends the "message" argument to the agent behind name
// ("a2a.<agent>.<skill>") and waits for the agent's answer. It fails with
// ErrNotAllowed, before contacting any agent, when the skill isn't in its
// agent's allow list, and with ErrUnavailable when the agent's card can't
// be read.
//
// A2A has no field that picks a skill: the agent reads the message and
// decides what to do. The skill in the tool name chooses the description
// the model sees and the allow entry the call needs.
//
// Call always asks for a stream of updates. When the card says the agent
// can't stream, the SDK sends a plain request instead and waits for the
// whole reply; Call reads both the same way.
//
// The answer is the text of the task's artifacts, or the agent's message
// when it replied without a task. A task that ends failed, rejected or
// canceled, or that stops to ask for more input, gives a Result with
// IsError set, and the text says why.
//
// The call stops when ctx ends or the agent's timeout passes; the error then
// wraps context.Canceled or context.DeadlineExceeded. If the agent had
// started a task by then, Call asks it to cancel that task.
//
// Call records a span named "invoke_agent <agent>". It carries the
// message and the answer only when capture_content is on.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (res dispatch.Result, err error) {
	a, skill, known := c.lookup(name)
	allowed := known && a.allow[skill]

	ctx, span := startCallSpan(ctx, name, a, skill, allowed)
	// This deferred function runs when Call returns. Because the results
	// are named (res, err), it sees the values Call is returning.
	defer func() {
		endCallSpan(ctx, span, res, err)
		span.End()
	}()

	if !allowed {
		return dispatch.Result{}, fmt.Errorf("%w: %q", ErrNotAllowed, name)
	}
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("gen_ai.tool.call.arguments", string(args)))
	}

	text, err := messageArg(args)
	if err != nil {
		return dispatch.Result{}, fmt.Errorf("call %s: %w", name, err)
	}
	client, err := c.clientFor(ctx, a)
	if err != nil {
		return dispatch.Result{}, err
	}

	// callCtx is a separate variable so the deferred span code above still
	// sees the caller's ctx, not this one, which cancel ends on return.
	callCtx, cancel := context.WithTimeout(ctx, a.cfg.callTimeout())
	defer cancel()
	// The SDK logs through the logger it finds in the context; this sends
	// its lines to merud's log file.
	callCtx = sdklog.AttachLogger(callCtx, c.log.With("a2a_agent", a.cfg.Name))

	got, err := send(callCtx, client, text)
	if got.taskID != "" {
		span.SetAttributes(attribute.String("meru.a2a.task.id", string(got.taskID)))
	}
	if got.state != sdk.TaskStateUnspecified {
		span.SetAttributes(attribute.String("meru.a2a.task.state", stateWord(got.state)))
	}
	if err != nil {
		ctxErr := callCtx.Err()
		if ctxErr != nil {
			// The SDK may return its own error when ctx ends. Wrapping
			// ctx.Err() lets the caller test for context.Canceled or
			// DeadlineExceeded with errors.Is.
			if !errors.Is(err, ctxErr) {
				err = fmt.Errorf("%w (%w)", ctxErr, err)
			}
			if got.taskID != "" && !got.finished() {
				c.cancelTask(ctx, client, a, got.taskID)
			}
		}
		a.markFailed(client, err, ctxErr == nil && unreachable(err))
		return dispatch.Result{}, fmt.Errorf("call %s: %w", name, err)
	}

	a.markOK()
	res = got.result()
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("gen_ai.tool.call.result", res.Text))
	}
	return res, nil
}

// send sends text as a new message and reads the updates until the task
// finishes or the stream ends. It returns what it collected, even on an
// error, so Call can cancel a task the agent started.
func send(ctx context.Context, client *a2aclient.Client, text string) (*answer, error) {
	got := &answer{}
	req := &sdk.SendMessageRequest{Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewTextPart(text))}
	// SendStreamingMessage returns an iterator: the loop asks it for one
	// update at a time, and the SDK reads each from the open response.
	// Leaving the loop early closes the response.
	for ev, err := range client.SendStreamingMessage(ctx, req) {
		if err != nil {
			return got, err
		}
		got.add(ev)
		if got.finished() {
			return got, nil
		}
	}
	if !got.finished() {
		return got, fmt.Errorf("the agent stopped sending updates before the task finished (state %q)", stateWord(got.state))
	}
	return got, nil
}

// cancelTask asks the agent to stop task id, after the call's context
// ended. It uses a fresh deadline, because ctx has already ended, and keeps
// ctx's values so the request stays in the turn's trace. A failure is only
// logged: the call has failed already.
func (c *Client) cancelTask(ctx context.Context, client *a2aclient.Client, a *agent, id sdk.TaskID) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	if _, err := client.CancelTask(cctx, &sdk.CancelTaskRequest{ID: id}); err != nil {
		c.log.Debug("a2a task cancel failed", "a2a_agent", a.cfg.Name, "task", id, "err", err)
	}
}

// messageArg pulls the message out of the model's arguments, which must be
// a JSON object with a non-empty "message" string.
func messageArg(args json.RawMessage) (string, error) {
	var in struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", errors.New(`arguments must be a JSON object such as {"message": "..."}`)
	}
	if strings.TrimSpace(in.Message) == "" {
		return "", errors.New(`arguments need a non-empty "message"`)
	}
	return in.Message, nil
}

// unreachable reports whether err means the agent couldn't be reached at
// all: no connection, or a connection that broke. The HTTP client wraps
// those in a *url.Error. An agent that answered with an error doesn't count.
func unreachable(err error) bool {
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// answer collects an agent's updates into the one reply Call returns.
type answer struct {
	taskID sdk.TaskID
	state  sdk.TaskState
	// status is the text of the latest status message that had text. A
	// failed task often says why here.
	status string
	// message is the text of a reply the agent sent as a message; replied
	// says whether one came.
	message string
	replied bool
	// order keeps artifacts in the order they first arrived; artifacts
	// holds each one's text parts.
	order     []sdk.ArtifactID
	artifacts map[sdk.ArtifactID][]string
}

// add folds one update into the answer. A type switch (`switch e :=
// ev.(type)`) runs the branch that matches the update's concrete type, with
// e already converted to that type.
func (g *answer) add(ev sdk.Event) {
	switch e := ev.(type) {
	case *sdk.Message:
		g.message = partsText(e.Parts)
		g.replied = true
	case *sdk.Task:
		// A task carries its whole state, so its artifacts replace any
		// collected so far.
		g.taskID = e.ID
		g.setStatus(e.Status)
		for _, art := range e.Artifacts {
			g.setArtifact(art, false)
		}
	case *sdk.TaskStatusUpdateEvent:
		g.taskID = e.TaskID
		g.setStatus(e.Status)
	case *sdk.TaskArtifactUpdateEvent:
		g.taskID = e.TaskID
		g.setArtifact(e.Artifact, e.Append)
	}
}

// setStatus records a task's state and the text of its status message.
func (g *answer) setStatus(s sdk.TaskStatus) {
	g.state = s.State
	if s.Message != nil {
		if text := partsText(s.Message.Parts); text != "" {
			g.status = text
		}
	}
}

// setArtifact stores an artifact's text. With appendParts set, the parts
// add to the artifact with the same ID; otherwise they replace it.
func (g *answer) setArtifact(art *sdk.Artifact, appendParts bool) {
	if art == nil {
		return
	}
	if g.artifacts == nil {
		g.artifacts = make(map[sdk.ArtifactID][]string)
	}
	// The second value from a map lookup says whether the key was there.
	if _, seen := g.artifacts[art.ID]; !seen {
		g.order = append(g.order, art.ID)
	}
	text := partsText(art.Parts)
	if appendParts {
		g.artifacts[art.ID] = append(g.artifacts[art.ID], text)
	} else {
		g.artifacts[art.ID] = []string{text}
	}
}

// finished reports whether the agent has said all it will say for this
// call: it replied with a message, or the task reached a final state or
// stopped to ask for input.
func (g *answer) finished() bool {
	switch g.state {
	case sdk.TaskStateInputRequired, sdk.TaskStateAuthRequired:
		return true
	}
	return g.state.Terminal() || (g.replied && g.taskID == "")
}

// result turns the collected updates into the Result the model reads.
func (g *answer) result() dispatch.Result {
	switch g.state {
	case sdk.TaskStateFailed, sdk.TaskStateRejected, sdk.TaskStateCanceled:
		reason := g.status
		if reason == "" {
			reason = "the agent gave no reason"
		}
		return dispatch.Result{Text: fmt.Sprintf("The agent's task ended %s: %s", stateWord(g.state), reason), IsError: true}
	case sdk.TaskStateInputRequired:
		return dispatch.Result{Text: "The agent asked for more input, and Meru can't answer an agent's question yet. It asked: " + g.status, IsError: true}
	case sdk.TaskStateAuthRequired:
		return dispatch.Result{Text: "The agent asked for credentials Meru doesn't have: " + g.status, IsError: true}
	}

	var parts []string
	for _, id := range g.order {
		if text := strings.Join(g.artifacts[id], ""); text != "" {
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "\n\n")
	if text == "" {
		text = g.status
	}
	if text == "" {
		text = g.message
	}
	if text == "" {
		text = "(the agent finished without returning any text)"
	}
	return dispatch.Result{Text: text}
}

// partsText joins the parts of a message or artifact into text. Structured
// data becomes JSON; a file shows as a short placeholder, so the model knows
// something was there.
func partsText(parts sdk.ContentParts) string {
	var out []string
	for _, p := range parts {
		if p == nil {
			continue
		}
		switch v := p.Content.(type) {
		case sdk.Text:
			out = append(out, string(v))
		case sdk.Data:
			if b, err := json.Marshal(v.Value); err == nil {
				out = append(out, string(b))
			}
		case sdk.URL:
			out = append(out, "[file "+string(v)+"]")
		case sdk.Raw:
			kind := p.MediaType
			if kind == "" {
				kind = "binary"
			}
			out = append(out, fmt.Sprintf("[file %s, %d bytes]", kind, len(v)))
		}
	}
	return strings.Join(out, "\n")
}

// stateWord turns "TASK_STATE_FAILED" into "failed", for text and spans.
func stateWord(s sdk.TaskState) string {
	if s == sdk.TaskStateUnspecified {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(string(s), "TASK_STATE_"))
}

// startCallSpan starts the span for one call. The OpenTelemetry GenAI
// semantic conventions name a call to a remote agent "invoke_agent
// {gen_ai.agent.name}", with gen_ai.operation.name = invoke_agent. Meru adds
// meru.a2a.skill, and meru.tool.server and meru.tool.allowed as on MCP tool
// spans (ARCHITECTURE.md, "Traces"). For a name the Client doesn't know,
// the span uses the whole name as the agent name.
func startCallSpan(ctx context.Context, name string, a *agent, skill string, allowed bool) (context.Context, trace.Span) {
	agentName := name
	if a != nil {
		agentName = a.cfg.Name
	}
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.agent.name", agentName),
		attribute.String("meru.a2a.skill", skill),
		attribute.String("meru.tool.server", agentName),
		attribute.Bool("meru.tool.allowed", allowed),
	}
	if a != nil {
		attrs = append(attrs, addressAttributes(a.cfg.URL)...)
	}
	return obs.Tracer().Start(ctx, "invoke_agent "+agentName,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// addressAttributes returns server.address and server.port for the agent's
// card URL, as the OpenTelemetry conventions name them.
func addressAttributes(raw string) []attribute.KeyValue {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	attrs := []attribute.KeyValue{attribute.String("server.address", u.Hostname())}
	if _, port, err := net.SplitHostPort(u.Host); err == nil {
		if n, err := strconv.Atoi(port); err == nil {
			attrs = append(attrs, attribute.Int("server.port", n))
		}
	}
	return attrs
}

// endCallSpan sets the span's outcome, with the same error.type values as
// MCP tool spans: "tool_error" when the agent's task failed, and Meru's
// own "denied", "unavailable" and "timeout". A cancelled call gets a
// "cancelled" event instead of an error status, as every other Meru span
// does.
func endCallSpan(ctx context.Context, span trace.Span, res dispatch.Result, err error) {
	if err == nil {
		if res.IsError {
			span.SetAttributes(attribute.String("error.type", "tool_error"))
			span.SetStatus(codes.Error, "agent reported an error")
		}
		return
	}
	switch {
	case errors.Is(err, ErrNotAllowed):
		span.SetAttributes(attribute.String("error.type", "denied"))
	case errors.Is(err, ErrUnavailable):
		span.SetAttributes(attribute.String("error.type", "unavailable"))
	case errors.Is(err, context.DeadlineExceeded):
		span.SetAttributes(attribute.String("error.type", "timeout"))
	}
	obs.EndSpanErr(ctx, span, err)
}
