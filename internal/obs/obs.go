// This file is the observability API the rest of Meru calls. Other packages
// record through these functions and never import OpenTelemetry's SDK
// directly, so metric names and attribute rules live in one place.
//
// Until Setup runs with an endpoint, every function here is a no-op that costs
// next to nothing: it loads one pointer, finds it nil and returns.

package obs

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/config"
)

// state is what Setup decides and the record functions read.
type state struct {
	// inst is nil when export is off; every record call then returns at once.
	inst *instruments
	// captureContent mirrors config's capture_content.
	captureContent bool
}

// current holds the live state. AGENTS.md rules out package-level mutable
// state, and this is the one exception: the record functions are called from
// every package, and passing a handle through all of them would touch every
// signature for no gain. Setup writes it once at startup.
//
// atomic.Pointer lets many goroutines read the pointer while Setup swaps it,
// without a lock and without a data race. Its zero value holds nil, which
// means "not set up": record calls are no-ops and CaptureContent is false.
var current atomic.Pointer[state]

// load returns the instruments to record into, or nil when export is off.
func load() *instruments {
	s := current.Load()
	if s == nil {
		return nil
	}
	return s.inst
}

// Setup starts metric and trace export to cfg.OTLPEndpoint. With no endpoint
// it leaves OpenTelemetry's no-op providers in place. It refuses a
// non-loopback endpoint. Call the returned shutdown function before exiting,
// so the last batch of data is sent.
//
// The body lives in setup.go.
func Setup(ctx context.Context, cfg config.Observability) (shutdown func(context.Context) error, err error) {
	return setup(ctx, cfg)
}

// Tracer returns the tracer every Meru span comes from. Before Setup it is
// OpenTelemetry's no-op tracer. After Setup with traces off, its spans record
// nothing but still carry a fresh trace ID, which merud writes to its log and
// to the session transcript (see setup.go).
func Tracer() trace.Tracer {
	return otel.Tracer("github.com/aarora79/meru")
}

// ModelCall describes one finished model call, for RecordModelCall.
type ModelCall struct {
	Tier      string        // "fast", "main" or "embed"
	Model     string        // the model name from config
	Operation string        // "chat", "generate" or "embed"
	Duration  time.Duration // whole call, as merud measured it
	// TimeToFirstToken is zero for calls that didn't stream.
	TimeToFirstToken time.Duration
	Usage            Usage
}

// Usage holds the runtime counters obs reads from one model call. It mirrors
// the fields of engine.Usage that metrics need, so this package doesn't import
// the engine: meru imports rpc, rpc imports obs, and the thin client must not
// depend on the engine (AGENTS.md, "Shape").
type Usage struct {
	PromptTokens       int
	OutputTokens       int
	LoadDuration       time.Duration // time the runtime spent loading the model
	PromptEvalDuration time.Duration // time the runtime spent reading the prompt; spans only
	EvalDuration       time.Duration // time the runtime spent writing the answer
}

// RecordModelCall records gen_ai.client.token.usage,
// gen_ai.client.operation.duration, gen_ai.server.time_to_first_token,
// gen_ai.server.time_per_output_token and meru.engine.load.duration.
//
// The model name comes from config.toml, so it is a small set in practice.
// Tier and operation outside their known sets become "other". Operation is
// reported with the GenAI convention's words: "generate" becomes
// "text_completion" and "embed" becomes "embeddings".
func RecordModelCall(ctx context.Context, c ModelCall) {
	in := load()
	if in == nil {
		return
	}
	model := attr(keyModel, c.Model)
	tier := attr(keyTier, bounded(c.Tier, tiers...))
	op := operationName(c.Operation)

	// metric.WithAttributes attaches key/value pairs to one measurement.
	in.operationDuration.Record(ctx, c.Duration.Seconds(),
		metric.WithAttributes(model, tier, attr(keyOperation, op)))

	in.tokenUsage.Record(ctx, int64(c.Usage.PromptTokens),
		metric.WithAttributes(model, tier, attr(keyOperation, op), attr(keyTokenType, "input")))
	// An embedding call writes no tokens, and a stream of zeros would drag
	// the output histogram toward nothing.
	if op != "embeddings" {
		in.tokenUsage.Record(ctx, int64(c.Usage.OutputTokens),
			metric.WithAttributes(model, tier, attr(keyOperation, op), attr(keyTokenType, "output")))
	}

	if c.TimeToFirstToken > 0 {
		in.timeToFirstToken.Record(ctx, c.TimeToFirstToken.Seconds(),
			metric.WithAttributes(model, tier))
	}

	// Decode speed comes from the runtime's own clock (EvalDuration), which
	// leaves out queueing and prompt reading. Both counters must be positive,
	// or the division means nothing.
	if c.Usage.OutputTokens > 0 && c.Usage.EvalDuration > 0 {
		perToken := c.Usage.EvalDuration.Seconds() / float64(c.Usage.OutputTokens)
		in.timePerOutputTok.Record(ctx, perToken, metric.WithAttributes(model, tier))
	}

	// Record the load time on every call, zero included, so the histogram's
	// count is the number of calls and a dashboard can show what share of
	// them paid for a cold load.
	in.loadDuration.Record(ctx, c.Usage.LoadDuration.Seconds(), metric.WithAttributes(model))
}

// Turn describes one finished turn, for RecordTurn.
type Turn struct {
	Route      string // one of the four routes
	Source     string // "cli", "tui" or "job"
	Outcome    string // "ok", "error", "cancelled", "timeout", "cut_off", "gave_up" or "bad_output"
	Duration   time.Duration
	Iterations int
}

// RecordTurn records meru.turn.duration and meru.turn.iterations.
// Values outside the known sets become "other".
func RecordTurn(ctx context.Context, t Turn) {
	in := load()
	if in == nil {
		return
	}
	route := attr(keyRoute, bounded(t.Route, routes...))
	in.turnDuration.Record(ctx, t.Duration.Seconds(), metric.WithAttributes(
		route,
		attr(keySource, bounded(t.Source, sources...)),
		attr(keyOutcome, bounded(t.Outcome, turnOutcomes...)),
	))
	in.turnIterations.Record(ctx, int64(t.Iterations), metric.WithAttributes(route))
}

// RecordRoute records one router decision in meru.route.decisions.
// outcome is "ok", "low_confidence" or "degraded". Values outside the known
// sets become "other".
func RecordRoute(ctx context.Context, route, outcome string) {
	in := load()
	if in == nil {
		return
	}
	in.routeDecisions.Add(ctx, 1, metric.WithAttributes(
		attr(keyRoute, bounded(route, routes...)),
		attr(keyOutcome, bounded(outcome, routeOutcomes...)),
	))
}

// RecordContextTokens records meru.context.tokens for one prompt section:
// "system", "skills", "memories", "sessions", "chunks", "history" or
// "tools". Any other section becomes "other".
func RecordContextTokens(ctx context.Context, section string, tokens int) {
	in := load()
	if in == nil {
		return
	}
	in.contextTokens.Record(ctx, int64(tokens),
		metric.WithAttributes(attr(keySection, bounded(section, sections...))))
}

// RecordRetrieval records meru.retrieval.duration for one retrieval stage:
// "vector", "fts", "fusion", "memories" or "sessions". Any other stage
// becomes "other".
func RecordRetrieval(ctx context.Context, stage string, d time.Duration) {
	in := load()
	if in == nil {
		return
	}
	in.retrievalDuration.Record(ctx, d.Seconds(),
		metric.WithAttributes(attr(keyStage, bounded(stage, stages...))))
}

// ToolCallMetric describes one finished tool call, for RecordToolCall.
type ToolCallMetric struct {
	Kind    string // "mcp", "a2a", "builtin" or "command"
	Server  string // the MCP server or A2A agent; "meru" for a built-in or a command
	Tool    string // the tool's name without the server
	Outcome string // "ok", "error", "denied", "declined", "cancelled" or "timeout"
	// Duration is how long the tool ran. It is zero for a call that never
	// ran: denied, declined, or cancelled before it started.
	Duration time.Duration
}

// RecordToolCall records meru.tool.calls and, for a call that ran,
// meru.tool.duration.
//
// Server and tool names come from config, so they form a small set, with
// one exception: a denied call names a tool the model made up, and a model
// can make up any number. RecordToolCall reports those as "other". Kind
// and outcome outside their known sets become "other" too.
func RecordToolCall(ctx context.Context, c ToolCallMetric) {
	in := load()
	if in == nil {
		return
	}
	server, tool := c.Server, c.Tool
	if c.Outcome == "denied" {
		server, tool = other, other
	}
	kind := attr(keyToolKind, bounded(c.Kind, toolKinds...))
	serverAttr := attr(keyToolServer, server)
	toolAttr := attr(keyToolName, tool)
	in.toolCalls.Add(ctx, 1, metric.WithAttributes(
		kind, serverAttr, toolAttr,
		attr(keyOutcome, bounded(c.Outcome, toolOutcomes...)),
	))
	if c.Duration > 0 {
		in.toolDuration.Record(ctx, c.Duration.Seconds(), metric.WithAttributes(kind, serverAttr, toolAttr))
	}
}

// RecordSession adds one to meru.sessions when a turn starts a new
// session. source is "cli", "tui" or "job"; any other value becomes
// "other".
func RecordSession(ctx context.Context, source string) {
	in := load()
	if in == nil {
		return
	}
	in.sessions.Add(ctx, 1, metric.WithAttributes(attr(keySource, bounded(source, sources...))))
}

// TurnUsage describes what one answered turn used, for RecordTurnUsage.
type TurnUsage struct {
	Route  string // one of the four routes
	Source string // "cli", "tui" or "job"
	// TokensIn and TokensOut sum the main model's tokens over the turn's
	// model calls.
	TokensIn  int
	TokensOut int
	// Docs counts the distinct files whose excerpts went into the prompt.
	Docs int
}

// RecordTurnUsage records meru.turn.tokens and meru.turn.docs for one
// answered turn. A failed or cancelled turn records neither, so the
// numbers match the turns table that `meru usage` reads.
//
// gen_ai.client.token.usage already counts tokens, per model call and by
// model and tier. meru.turn.tokens counts them per turn and adds the route
// and source, which a model call doesn't know, so a dashboard can show
// which kind of question spends the tokens. Values outside the known sets
// become "other".
func RecordTurnUsage(ctx context.Context, u TurnUsage) {
	in := load()
	if in == nil {
		return
	}
	route := attr(keyRoute, bounded(u.Route, routes...))
	source := attr(keySource, bounded(u.Source, sources...))
	in.turnTokens.Add(ctx, int64(u.TokensIn), metric.WithAttributes(attr(keyTokenType, "input"), route, source))
	in.turnTokens.Add(ctx, int64(u.TokensOut), metric.WithAttributes(attr(keyTokenType, "output"), route, source))
	in.turnDocs.Record(ctx, int64(u.Docs), metric.WithAttributes(route))
}

// ActiveStreams adds delta (+1 or -1) to meru.rpc.active_streams.
func ActiveStreams(ctx context.Context, delta int64) {
	in := load()
	if in == nil {
		return
	}
	in.activeStreams.Add(ctx, delta)
}

// CaptureContent reports whether spans may carry prompt and response text.
// It is false unless config set capture_content = true.
func CaptureContent() bool {
	s := current.Load()
	return s != nil && s.captureContent
}
