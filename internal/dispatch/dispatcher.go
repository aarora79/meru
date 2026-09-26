// This file holds the Dispatcher: the backends it routes calls to, the
// session approvals it remembers, and Dispatch, the one function every tool
// call goes through. ARCHITECTURE.md, "Agent loop" step 4 and "Approving a
// tool call", describe what it must do.

package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// maxModelResult caps, in characters, the result text the model reads. A
// tool can return a whole web page or a long file; 16,000 characters is
// about 4,000 tokens, which leaves room in the prompt for the rest of the
// turn.
const maxModelResult = 16000

// maxLoggedResult caps the result text in the transcript and the
// tool_calls row. The full text goes to the model; the log keeps enough to
// see what came back.
const maxLoggedResult = store.MaxToolResult

// maxDeniedName caps the tool name a denied call records. The model wrote
// that name, and it can be any length.
const maxDeniedName = 128

// Recorder writes one row to the tool_calls audit log. *store.Store
// implements it; tests use a fake.
type Recorder interface {
	InsertToolCall(ctx context.Context, row store.ToolCall) error
}

// Options holds what New needs besides the backends and the recorder.
type Options struct {
	// Redact removes secret values from text. The secrets package supplies
	// it. nil leaves text as it is.
	Redact func(string) string
	// Log gets a warning when a write fails and a debug line per call.
	// nil discards them.
	Log *slog.Logger
}

// Dispatcher runs tool calls on the backends that own them and records each
// call. Create it with New. Its methods are safe to call from several
// goroutines, so the agent loop can run independent calls side by side.
type Dispatcher struct {
	rec    Recorder
	redact func(string) string
	log    *slog.Logger

	// mu guards the two fields below it.
	mu       sync.Mutex
	backends []Backend
	// approved holds the tools the user approved "for this session". It
	// lives only in memory: an approval ends when merud stops, and never
	// reaches config (ARCHITECTURE.md, "Approving a tool call").
	approved map[approvalKey]bool
}

// approvalKey names one session approval: a tool, by full name, in one
// session. A struct of two strings works as a map key because Go compares
// structs field by field.
type approvalKey struct {
	session string
	tool    string
}

// New returns a Dispatcher over backends, which it asks in order: when two
// offer a tool with the same name, the first wins. rec gets one row per
// call; nil records no rows.
func New(backends []Backend, rec Recorder, opts Options) *Dispatcher {
	d := &Dispatcher{
		rec:      rec,
		redact:   opts.Redact,
		log:      opts.Log,
		backends: slices.Clone(backends),
		approved: map[approvalKey]bool{},
	}
	if d.redact == nil {
		d.redact = func(s string) string { return s }
	}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}
	return d
}

// snapshot returns a copy of the backend list, so callers can walk it
// without holding mu while a backend does its own locking.
func (d *Dispatcher) snapshot() []Backend {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.backends)
}

// Tools returns every tool the model may use, from every backend, in
// backend order. When two backends offer the same full name, the first one
// keeps it and Tools logs a warning.
func (d *Dispatcher) Tools() []engine.ToolSpec {
	var out []engine.ToolSpec
	owner := map[string]string{} // tool name → kind of the backend that has it
	for _, b := range d.snapshot() {
		for _, t := range b.Tools() {
			if kept, dup := owner[t.Name]; dup {
				d.log.Warn("two tool sources offer the same tool; the first one keeps it",
					"tool", t.Name, "kept", kept, "dropped", b.Kind())
				continue
			}
			owner[t.Name] = b.Kind()
			out = append(out, t)
		}
	}
	return out
}

// Servers describes every server, agent and built-in set behind the
// backends, in backend order, for `meru tools list`.
func (d *Dispatcher) Servers() []rpc.ServerInfo {
	var out []rpc.ServerInfo
	for _, b := range d.snapshot() {
		out = append(out, b.Status()...)
	}
	return out
}

// Replace swaps in b for the backend of the given kind, or adds b at the
// end when there is none. merud calls it after the configure tool changes
// config and the MCP pool is rebuilt. Calls already running finish on the
// old backend; the caller closes it once they have.
func (d *Dispatcher) Replace(kind string, b Backend) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, old := range d.backends {
		if old.Kind() == kind {
			d.backends[i] = b
			return
		}
	}
	d.backends = append(d.backends, b)
}

// Refresh has every backend that is a Refresher bring its tool list up to
// date, one backend after another. A backend that isn't a Refresher is
// skipped. The ", ok" form of the type assertion
// b.(Refresher) gives ok = false, not a panic, for those.
func (d *Dispatcher) Refresh(ctx context.Context) {
	for _, b := range d.snapshot() {
		if c, ok := b.(Refresher); ok {
			c.Refresh(ctx)
		}
	}
}

// Asks reports whether a call to the named tool would ask the user before
// it runs, unless a session approval covers it. A tool no backend offers
// counts as asking, the safe answer. The agent loop uses it to offer only
// commands that never ask on the "search" route.
func (d *Dispatcher) Asks(name string) bool {
	b := d.find(name)
	return b == nil || b.Confirm(name) != ConfirmNever
}

// find returns the backend that owns the tool name, the first one that
// lists it, as Tools does. It returns nil when no backend offers the tool.
func (d *Dispatcher) find(name string) Backend {
	for _, b := range d.snapshot() {
		for _, t := range b.Tools() {
			if t.Name == name {
				return b
			}
		}
	}
	return nil
}

// Dispatch runs one tool call and records it. It is the only path from the
// model to a tool (AGENTS.md, non-negotiable 4). It never returns a Go
// error: every failure becomes a Result the model can read, and the
// Outcome says how the call ended.
//
// In order, it:
//
//  1. finds the backend that offers the tool; with none, the call is
//     "denied" and doesn't run;
//  2. writes the tool_call line to the transcript;
//  3. asks the user when the tool, or this one call, needs a yes (see
//     confirmFor and approve);
//  4. runs the call on the backend, which enforces its own timeout, with
//     the call's session on ctx (see SessionFrom);
//  5. cuts the result for the model and removes secrets from it;
//  6. writes the tool_result line, for every call, whatever its outcome;
//  7. writes the tool_calls row;
//  8. records the metrics and the meru.dispatch span.
//
// Outcome.Duration is the time the tool ran, without the wait for the
// user's answer; it is zero for a call that never ran.
func (d *Dispatcher) Dispatch(ctx context.Context, c Call) (Result, Outcome) {
	start := time.Now()
	// The span covers the whole call, approval included. The MCP pool's
	// "tools/call <tool>" span nests under it, because ctx now carries it.
	ctx, span := obs.Tracer().Start(ctx, "meru.dispatch",
		trace.WithAttributes(attribute.String("gen_ai.tool.name", c.Name)))
	defer span.End()

	args := d.redactArgs(c.Args)
	b := d.find(c.Name)
	var kind, server, tool string
	if b != nil {
		kind = b.Kind()
		server, tool = b.Locate(c.Name)
		// b.(Auditor) is a type assertion: ok is true when b also has
		// the AuditArgs method.
		if a, ok := b.(Auditor); ok {
			if audit := a.AuditArgs(c.Name, c.Args); audit != nil {
				args = d.redactArgs(audit)
			}
		}
	} else {
		kind, server, tool = guessLocation(c.Name)
	}

	// Step 2. A denied call gets a tool_call line too, so replay pairs its
	// lines the same way as any other call's.
	appendErr := d.append(c, transcript.Line{
		TS: start, Type: transcript.TypeToolCall, CallID: c.ID,
		Kind: kind, Server: server, Tool: tool, Args: args, TraceID: c.TraceID,
	})

	var res Result
	var outcome, approval string
	var ran time.Duration
	switch {
	case b == nil:
		outcome = OutcomeDenied
		res = Result{IsError: true, Text: fmt.Sprintf(
			"The tool %q isn't available, so it didn't run. Use one of the tools you were given, or answer without one.", c.Name)}
	case appendErr != nil:
		// Every call must reach the transcript before it runs. A call
		// that can't be logged doesn't run.
		outcome = OutcomeError
		res = Result{IsError: true, Text: "Meru couldn't record this call in the session transcript, so it didn't run."}
	default:
		res, outcome, approval, ran = d.run(ctx, c, b, kind, server, tool, args)
	}

	// Step 5. Redact first, so a secret that straddles the cut still goes.
	res.Text = d.redact(res.Text)
	logged := capRunes(res.Text, maxLoggedResult)
	res.Text = capForModel(res.Text)

	// Step 6.
	_ = d.append(c, transcript.Line{
		Type: transcript.TypeToolResult, CallID: c.ID, Outcome: outcome, OK: outcome == OutcomeOK,
		Ms: ran.Milliseconds(), Result: logged, TraceID: c.TraceID,
	})

	// Step 7. The row goes in even when the turn was cancelled, so
	// WithoutCancel keeps ctx's values but drops its end.
	if d.rec != nil {
		row := store.ToolCall{
			CallID: c.ID, Session: c.Session, Time: start, Kind: kind, Server: server, Tool: tool,
			Args: args, Result: logged, Outcome: outcome, Approval: approval,
			DurationMillis: ran.Milliseconds(), TraceID: c.TraceID,
		}
		if err := d.rec.InsertToolCall(context.WithoutCancel(ctx), row); err != nil {
			// The transcript holds the call, and replay can rebuild the row
			// from it, so a failed row doesn't fail the call.
			d.log.Warn("write tool_calls row", "tool", c.Name, "err", err)
		}
	}

	// Step 8.
	obs.RecordToolCall(ctx, obs.ToolCallMetric{Kind: kind, Server: server, Tool: tool, Outcome: outcome, Duration: ran})
	span.SetAttributes(
		attribute.String("meru.tool.kind", kind),
		attribute.String("meru.tool.server", server),
		attribute.String("meru.tool.outcome", outcome),
		attribute.String("meru.tool.approval", approval),
	)
	if obs.CaptureContent() {
		span.SetAttributes(
			attribute.String("gen_ai.tool.call.arguments", string(args)),
			attribute.String("gen_ai.tool.call.result", logged),
		)
	}
	switch outcome {
	case OutcomeCancelled:
		span.AddEvent("cancelled")
	case OutcomeError, OutcomeTimeout, OutcomeDenied:
		span.SetStatus(codes.Error, outcome)
	}
	d.log.Debug("tool call", "tool", c.Name, "kind", kind, "outcome", outcome,
		"ms", ran.Milliseconds(), "approval", approval, "trace_id", c.TraceID)

	return res, Outcome{Outcome: outcome, Duration: ran}
}

// run does steps 3 and 4 for a tool that a backend offers: it asks the
// user when the tool needs it, then runs the call. It returns the result,
// the outcome, the user's choice ("" when nobody was asked) and how long
// the tool ran.
func (d *Dispatcher) run(ctx context.Context, c Call, b Backend, kind, server, tool string, args json.RawMessage) (res Result, outcome, approval string, ran time.Duration) {
	approval, outcome = d.approve(ctx, c, confirmFor(b, c), kind, server, tool, args)
	switch outcome {
	case "":
		// Approved, or no approval needed: run it.
	case OutcomeCancelled:
		return Result{IsError: true, Text: "The turn ended before the user answered, so the call didn't run."}, outcome, approval, 0
	default: // OutcomeDeclined
		if approval == "" {
			return Result{IsError: true, Text: "This tool needs the user's approval, and nobody can give it here, so the call didn't run. Answer without it."},
				outcome, approval, 0
		}
		return Result{IsError: true, Text: "The user said no to this call, so it didn't run. Answer without it, or ask the user what to do."},
			outcome, approval, 0
	}

	callStart := time.Now()
	// The backend gets the session on ctx; see SessionFrom.
	res, err := b.Call(withSession(ctx, c.Session), c.Name, c.Args)
	ran = time.Since(callStart)
	switch {
	case err == nil && res.IsError:
		return res, OutcomeError, approval, ran
	case err == nil:
		return res, OutcomeOK, approval, ran
	// ctx ended: the turn was cancelled, or its own deadline passed. Either
	// way the tool didn't fail; the turn stopped waiting for it.
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return Result{IsError: true, Text: "The call was cancelled before it finished."}, OutcomeCancelled, approval, ran
	// The backend's own timeout passed while the turn still waited.
	case errors.Is(err, context.DeadlineExceeded):
		return Result{IsError: true, Text: "The tool didn't answer in time, so the call stopped."}, OutcomeTimeout, approval, ran
	default:
		return Result{IsError: true, Text: "The tool call failed: " + err.Error()}, OutcomeError, approval, ran
	}
}

// confirmFor says whether call c to backend b asks first: what b's
// ConfirmCall says, when b is a CallConfirmer with an answer for c, and
// b.Confirm(c.Name) otherwise.
func confirmFor(b Backend, c Call) Confirm {
	if cc, ok := b.(CallConfirmer); ok {
		if confirm, ok := cc.ConfirmCall(c); ok {
			return confirm
		}
	}
	return b.Confirm(c.Name)
}

// approve decides whether the call may run, asking the user when the tool
// needs it:
//
//   - ConfirmNever runs without asking.
//   - ConfirmAsk runs without asking when the user approved the tool for
//     this session; otherwise it asks, offering once, session and deny.
//   - ConfirmAlways asks every time and offers only once and deny, whatever
//     the user chose before.
//
// A job, or a client that can't ask (c.Approve is nil), can't say yes, so
// the call is declined without asking. approve writes an approval line for
// each answer. It returns the user's choice ("" when nobody answered) and
// an outcome: "" to run the call, "declined" or "cancelled" to skip it.
func (d *Dispatcher) approve(ctx context.Context, c Call, confirm Confirm, kind, server, tool string, args json.RawMessage) (choice, outcome string) {
	var choices []rpc.Choice
	switch confirm {
	case ConfirmNever:
		return "", ""
	case ConfirmAsk:
		if d.sessionApproved(c.Session, c.Name) {
			return "", ""
		}
		choices = []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny}
	default: // ConfirmAlways, and any value a backend might add later
		choices = []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}
	}

	if c.Approve == nil || c.Source == rpc.SourceJob {
		return "", OutcomeDeclined
	}
	answer, err := c.Approve(ctx, rpc.Approval{ID: c.ID, Name: c.Name, Kind: kind, Args: args, Choices: choices})
	if err != nil {
		if ctx.Err() != nil {
			return "", OutcomeCancelled
		}
		// The client went away without answering. Nobody said yes.
		d.log.Debug("approval failed", "tool", c.Name, "err", err)
		return "", OutcomeDeclined
	}
	// The client may offer only the choices sent; anything else counts as
	// no. The rpc server checks this too, but the rule belongs here.
	if !slices.Contains(choices, answer) {
		answer = rpc.ChoiceDeny
	}
	// An approval line that can't be written doesn't stop the call: the
	// user said yes or no, and the tool_call line already holds the call.
	_ = d.append(c, transcript.Line{
		Type: transcript.TypeApproval, CallID: c.ID, Server: server, Tool: tool,
		Choice: string(answer), TraceID: c.TraceID,
	})

	switch answer {
	case rpc.ChoiceOnce:
		return string(answer), ""
	case rpc.ChoiceSession:
		d.approveForSession(c.Session, c.Name)
		return string(answer), ""
	default:
		return string(rpc.ChoiceDeny), OutcomeDeclined
	}
}

// sessionApproved reports whether the user approved tool for the rest of
// session. A call with no session never counts as approved.
func (d *Dispatcher) sessionApproved(session, tool string) bool {
	if session == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.approved[approvalKey{session, tool}]
}

// approveForSession remembers that the user approved tool for the rest of
// session. With no session there is nothing to remember it by.
func (d *Dispatcher) approveForSession(session, tool string) {
	if session == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.approved[approvalKey{session, tool}] = true
}

// append writes l through c.Append, logging a failure at warn. A nil
// Append writes nothing. It returns the error so Dispatch can refuse to
// run a call it couldn't log.
func (d *Dispatcher) append(c Call, l transcript.Line) error {
	if c.Append == nil {
		return nil
	}
	if err := c.Append(l); err != nil {
		d.log.Warn("write transcript line", "type", l.Type, "tool", c.Name, "err", err)
		return err
	}
	return nil
}

// redactArgs returns the call's arguments ready for the transcript, the
// row and the approval prompt: secrets removed and the JSON compacted, as
// the transcript stores it, so a live row and a replayed one match. Empty
// arguments give nil. Arguments that aren't valid JSON, which a model
// sometimes writes, become one JSON string, so the transcript line still
// encodes.
func (d *Dispatcher) redactArgs(args json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(args))) == 0 {
		return nil
	}
	text := d.redact(string(args))
	// json.Compact writes the same JSON without the spaces between tokens.
	// It fails on text that isn't valid JSON.
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(text)); err == nil {
		return json.RawMessage(buf.Bytes())
	}
	// Marshal of a string can't fail.
	quoted, _ := json.Marshal(text)
	return json.RawMessage(quoted)
}

// guessLocation splits the name of a tool no backend offers, for a denied
// call's row: "a2a.<agent>.<skill>" is an A2A skill, "cmd.<name>" a local
// command, "<server>.<tool>" an MCP tool, and a name with no dot a
// built-in. The model wrote the name, so guessLocation cuts the tool part
// to maxDeniedName characters.
func guessLocation(name string) (kind, server, tool string) {
	if strings.HasPrefix(name, "cmd.") {
		return KindCommand, "meru", capRunes(name, maxDeniedName)
	}
	// strings.CutPrefix returns the rest of the string and whether the
	// prefix was there.
	if rest, ok := strings.CutPrefix(name, "a2a."); ok {
		server, tool, _ = strings.Cut(rest, ".")
		return KindA2A, capRunes(server, maxDeniedName), capRunes(tool, maxDeniedName)
	}
	if server, tool, ok := strings.Cut(name, "."); ok {
		return KindMCP, capRunes(server, maxDeniedName), capRunes(tool, maxDeniedName)
	}
	return KindBuiltin, "meru", capRunes(name, maxDeniedName)
}

// capForModel cuts text to maxModelResult characters and, when it cut
// anything, adds a line saying so, so the model knows the result goes on.
func capForModel(text string) string {
	n := utf8.RuneCountInString(text)
	if n <= maxModelResult {
		return text
	}
	return capRunes(text, maxModelResult) +
		fmt.Sprintf("\n[Meru cut this result: it showed the first %d of %d characters.]", maxModelResult, n)
}

// capRunes cuts s to at most n characters. It counts runes, Go's name for
// a Unicode character, so it never splits a character in two.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
