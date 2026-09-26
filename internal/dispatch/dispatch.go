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
	KindCommand = "command" // a [[commands]] entry, named "cmd.<name>"
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

// Backend is one source of tools: the MCP client pool, the A2A client, the
// built-in tools, or the local commands. There are four, which is why this
// is an interface.
type Backend interface {
	// Kind returns KindMCP, KindA2A, KindBuiltin or KindCommand.
	Kind() string
	// Tools returns the tools the model may use from this source, by full
	// name: "<server>.<tool>", "a2a.<agent>.<skill>", "cmd.<name>", or the
	// built-in's name. Tools not on an allowlist aren't in it.
	Tools() []engine.ToolSpec
	// Confirm says whether the named tool asks first. name is one that
	// Tools returned.
	Confirm(name string) Confirm
	// Locate splits a full name into the server (or agent) and the tool
	// name for rows and metrics. For a built-in or a command, server is
	// "meru".
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

// Auditor is an extra method a Backend may have. When the model's
// arguments don't show what a call will do, AuditArgs returns what will
// happen instead, as a JSON object, and Dispatch records that in their
// place: in the tool_call line, the approval prompt, the tool_calls row and
// the span. It returns nil to keep the model's arguments, for example when
// they are invalid and the call will fail anyway.
//
// The commands backend is the one Auditor. The model sends {"repo":"meru"},
// but the audit log must show the program that ran, such as
// ["git","-C","/home/you/repos/meru","log"]. Dispatch writes the tool_call
// line before the call runs, so the backend works that out up front rather
// than in its Result.
type Auditor interface {
	AuditArgs(name string, args json.RawMessage) json.RawMessage
}

// CallConfirmer is an extra method a Backend may have. Confirm decides per
// tool; ConfirmCall decides per call, from the call's arguments, session
// and question. Dispatch asks ConfirmCall first, and uses what it returns
// when ok is true; with ok false, Confirm decides as usual.
//
// The built-in tools are the one CallConfirmer. web_fetch runs without
// asking for a URL that a web_search result or the user's own question in
// the same session showed, and asks for any other URL, because a URL the
// model made up can carry the user's data to a stranger's server in its
// path or query (ARCHITECTURE.md, "Web search").
type CallConfirmer interface {
	ConfirmCall(c Call) (confirm Confirm, ok bool)
}

// Refresher is an extra method a Backend may have. Refresh brings the
// backend's tool list up to date: it asks each connected server for its
// tools again, tries once to reach each server that isn't connected, and
// returns when every server has answered or failed. The agent loop calls
// it, through Dispatcher.Refresh, at the start of a turn that offers tools
// and before it lists them. The MCP backend is the one Refresher: merud
// never lists or retries an MCP server in the background (ARCHITECTURE.md,
// "MCP").
type Refresher interface {
	Refresh(ctx context.Context)
}

// Result is what a tool call hands back to the model.
type Result struct {
	// Text is the result as the model reads it.
	Text string
	// IsError is true when the tool ran and reported a failure.
	IsError bool
	// Sources lists the excerpts from the user's files that Text holds,
	// numbered as Text numbers them, so the agent can add them to the
	// turn's sources and the clients can list the ones the answer cites.
	// search_files fills it; other tools leave it nil.
	Sources []rpc.Citation
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
	// Question is what the user typed: this turn's question, then the
	// user's earlier questions that the model sees in its history, one per
	// line. The agent fills it. A CallConfirmer reads it to tell a URL the
	// user gave from one the model made up. It never reaches the transcript
	// or the tool_calls row, which already hold the question.
	Question string
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

// citeKey is the key under which the agent puts its citation counter on
// the context, as sessionKey does for the session.
type citeKey struct{}

// WithCiteNumbers returns a copy of ctx that carries next, the turn's
// citation counter. next(n) reserves n citation numbers and returns the
// first. The agent calls it before Dispatch, so a tool that returns
// numbered excerpts can number them after the ones already in the
// prompt, and the model's [n] marks name one excerpt across the turn.
//
// Backend.Call takes no turn, so, like the session, the counter rides on
// the context rather than on a new parameter every backend would have to
// take.
func WithCiteNumbers(ctx context.Context, next func(n int) int) context.Context {
	return context.WithValue(ctx, citeKey{}, next)
}

// CiteNumbers reserves n citation numbers for the call a backend is
// running and returns the first. Calls that run at the same time get
// ranges that don't overlap. Outside a turn, or when n is 0 or less, it
// reserves nothing and returns 1.
func CiteNumbers(ctx context.Context, n int) int {
	next, ok := ctx.Value(citeKey{}).(func(int) int)
	if !ok || n <= 0 {
		return 1
	}
	return next(n)
}
