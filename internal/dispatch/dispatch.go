// This file holds the types the rest of merud uses to reach dispatch: the
// Backend interface each tool source implements, and the Call and Result
// that go in and out of Dispatch.

package dispatch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// Kinds of tool source, as they appear in tool_calls rows, transcript lines
// and metrics.
const (
	KindMCP     = "mcp"
	KindA2A     = "a2a"
	KindBuiltin = "builtin"
)

// Outcomes of a call, as they appear in tool_calls rows, transcript lines
// and the meru.tool.calls metric. The set is fixed.
const (
	OutcomeOK        = "ok"        // the call ran and the tool reported success
	OutcomeError     = "error"     // the call ran and failed, or the tool reported an error
	OutcomeDenied    = "denied"    // the tool isn't on any allowlist, so the call didn't run
	OutcomeDeclined  = "declined"  // the user said no, or nobody could be asked
	OutcomeCancelled = "cancelled" // the turn ended first
	OutcomeTimeout   = "timeout"   // the call ran past its timeout
)

// Confirm says whether a tool asks the user before it runs.
type Confirm int

const (
	// ConfirmNever runs the tool without asking.
	ConfirmNever Confirm = iota // iota numbers the constants 0, 1, 2
	// ConfirmAsk asks, unless the user already approved the tool for this
	// session.
	ConfirmAsk
	// ConfirmAlways asks every time and offers no session approval. Only
	// the configure tool uses it: config grants lasting trust, so the model
	// can't grant any to itself.
	ConfirmAlways
)

// Backend is one source of tools: the MCP client pool, the A2A client, or
// the built-in tools. There are three, which is why this is an interface.
type Backend interface {
	// Kind returns KindMCP, KindA2A or KindBuiltin.
	Kind() string
	// Tools returns the tools the model may use from this source, by full
	// name: "<server>.<tool>", "a2a.<agent>.<skill>", or the built-in's
	// name. Tools not on an allowlist aren't in it.
	Tools() []engine.ToolSpec
	// Confirm says whether the named tool asks first. name is one that
	// Tools returned.
	Confirm(name string) Confirm
	// Locate splits a full name into the server (or agent) and the tool
	// name for rows and metrics. For a built-in, server is "meru".
	Locate(name string) (server, tool string)
	// Call runs the named tool with args, a JSON object. A tool that ran
	// and reported a failure returns a Result with IsError set and a nil
	// error; err is for calls that couldn't run or finish.
	Call(ctx context.Context, name string, args json.RawMessage) (Result, error)
	// Status describes each server or agent behind this backend for
	// `meru tools list`: whether it is connected and which tools it gives
	// the model.
	Status() []rpc.ServerInfo
}

// Result is what a tool call hands back to the model.
type Result struct {
	// Text is the result as the model reads it.
	Text string
	// IsError is true when the tool ran and reported a failure.
	IsError bool
}

// Call is one tool call the model asked for.
type Call struct {
	// ID is the call's ID within the turn; it ties the transcript lines
	// and the client's tool events together.
	ID string
	// Name is the tool's full name, as the model wrote it.
	Name string
	// Args are the arguments, a JSON object.
	Args json.RawMessage
	// Session is the session's ID. Session approvals are kept per session.
	Session string
	// Source is where the question came from. A "job" has nobody to ask,
	// so every call that needs a yes is declined.
	Source rpc.Source
	// Append writes one line to the session's transcript.
	Append func(transcript.Line) error
	// Approve asks the user. nil means nobody can be asked.
	Approve rpc.ApproveFunc
	// TraceID is the turn's trace ID, for the transcript and the row.
	TraceID string
}

// Outcome is how Dispatch ended a call: one of the Outcome constants, plus
// how long it took.
type Outcome struct {
	Outcome  string
	Duration time.Duration
}

// sessionKey is the key under which Dispatch puts the call's session on
// the context it hands a backend. An unexported struct type as the key
// means no other package can read or overwrite the value by accident.
type sessionKey struct{}

// withSession returns a copy of ctx that carries the session ID.
func withSession(ctx context.Context, session string) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

// SessionFrom returns the session of the call a backend is running, or ""
// outside a call or for a call with no session.
//
// Backend.Call takes no session, and only the remember tool needs one, to
// record which session a memory came from. Adding a parameter would change
// every backend for that one tool, so Dispatch puts the session on the
// call's context instead. ctx.Value returns an any, Go's empty interface;
// the ", ok" form of the type assertion gives "" rather than a panic when
// the value is missing.
func SessionFrom(ctx context.Context) string {
	s, _ := ctx.Value(sessionKey{}).(string)
	return s
}
