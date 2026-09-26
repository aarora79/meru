// This file names every metric Meru records, the attribute keys they carry,
// and the fixed value sets those attributes may take. The table in
// ARCHITECTURE.md, "Observability" → "Metrics", is the source for all of it;
// change the table first, then this file.

package obs

import (
	"errors"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metric names. The gen_ai.* names come from the OpenTelemetry GenAI semantic
// conventions, so standard dashboards understand them; meru.* names cover
// what those conventions don't.
//
// A `const ( ... )` block declares several constants at once.
const (
	metricTokenUsage        = "gen_ai.client.token.usage" // #nosec G101 -- a metric name, not a credential
	metricOperationDuration = "gen_ai.client.operation.duration"
	metricTimeToFirstToken  = "gen_ai.server.time_to_first_token" // #nosec G101 -- a metric name, not a credential
	metricTimePerOutputTok  = "gen_ai.server.time_per_output_token"
	metricLoadDuration      = "meru.engine.load.duration"
	metricRouteDecisions    = "meru.route.decisions"
	metricTurnDuration      = "meru.turn.duration"
	metricTurnIterations    = "meru.turn.iterations"
	metricContextTokens     = "meru.context.tokens"
	metricActiveStreams     = "meru.rpc.active_streams"
	metricRetrievalDuration = "meru.retrieval.duration"
	metricToolCalls         = "meru.tool.calls"
	metricToolDuration      = "meru.tool.duration"
	metricSessions          = "meru.sessions"
	metricTurnTokens        = "meru.turn.tokens" // #nosec G101 -- a metric name, not a credential
	metricTurnDocs          = "meru.turn.docs"
)

// Attribute keys. The gen_ai.* keys are the GenAI convention's own; the
// meru.* keys are ours.
const (
	keyModel     = "gen_ai.request.model"
	keyOperation = "gen_ai.operation.name"
	keyTokenType = "gen_ai.token.type" // #nosec G101 -- an attribute key, not a credential
	keyTier      = "meru.tier"
	keyRoute     = "meru.route"
	keySource    = "meru.source"
	keyOutcome   = "meru.outcome"
	keySection   = "meru.section"
	keyStage     = "meru.stage"
	// The tool keys match the attributes on the meru.dispatch span, so a
	// dashboard can join a metric to its traces.
	keyToolKind   = "meru.tool.kind"
	keyToolServer = "meru.tool.server"
	keyToolName   = "gen_ai.tool.name"
)

// other replaces any attribute value outside its allowed set. It keeps a
// typo or a new code path from minting a fresh time series per value.
const other = "other"

// The allowed values for each bounded attribute. Metric storage keeps one
// series per distinct combination of attribute values, so an open-ended value
// (an ID, a path, a question) would grow it without limit. See ARCHITECTURE.md,
// "Metrics": "Keep attribute values to small, fixed sets."
var (
	tiers         = []string{"fast", "main", "embed"}
	routes        = []string{"direct", "search", "tools", "search+tools"}
	sources       = []string{"cli", "tui", "job", "desktop"}
	turnOutcomes  = []string{"ok", "error", "cancelled", "timeout", "cut_off", "gave_up", "bad_output"}
	routeOutcomes = []string{"ok", "low_confidence", "degraded"}
	sections      = []string{"system", "skills", "memories", "sessions", "chunks", "history", "tools"}
	stages        = []string{"vector", "fts", "fusion", "memories", "sessions"}
	toolKinds     = []string{"mcp", "a2a", "builtin", "command"}
	toolOutcomes  = []string{"ok", "error", "denied", "declined", "cancelled", "timeout"}
)

// operationNames maps Meru's own operation words to the values the GenAI
// convention defines for gen_ai.operation.name. `map[string]string` is Go's
// built-in hash table from string keys to string values.
var operationNames = map[string]string{
	"chat":     "chat",
	"generate": "text_completion",
	"embed":    "embeddings",
}

// Histogram bucket edges. Without them the SDK uses edges sized for
// milliseconds (0, 5, 10, 25 …), which would drop every call measured in
// seconds into the first two buckets. The duration and token edges are the
// ones the GenAI convention recommends; time to first token adds edges around
// the v0.1 target of one second.
var (
	durationBuckets   = []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
	firstTokenBuckets = []float64{0.001, 0.005, 0.01, 0.02, 0.04, 0.06, 0.08, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	perTokenBuckets   = []float64{0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1, 2.5}
	tokenBuckets      = []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576}
	iterationBuckets  = []float64{1, 2, 3, 4, 5, 6, 7, 8, 10, 12, 16}
	// Retrieval stages run inside merud and take micro- to milliseconds,
	// so their edges start far below the model-call ones.
	retrievalBuckets = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}
	// A search puts at most a few excerpts in the prompt, so a turn cites
	// few files. The first edge, 0, separates turns that used no file.
	docBuckets = []float64{0, 1, 2, 3, 4, 5, 6, 8, 10, 15, 20}
)

// bounded returns v when it is one of allowed, and "other" when it isn't.
// The `allowed ...string` parameter is variadic: callers pass a slice with
// `allowed...` or list the values one by one.
func bounded(v string, allowed ...string) string {
	if slices.Contains(allowed, v) {
		return v
	}
	return other
}

// operationName returns the GenAI convention's name for a Meru operation,
// or "other" for a word it doesn't know.
func operationName(op string) string {
	// A map lookup returns the value and a bool that says whether the key
	// was there; `if name, ok := …; ok` declares both and tests the bool.
	if name, ok := operationNames[op]; ok {
		return name
	}
	return other
}

// instruments holds one handle per metric. The Record functions in obs.go
// write through these handles; they never create an instrument themselves,
// so a record call costs a map lookup in the SDK and nothing more.
type instruments struct {
	tokenUsage        metric.Int64Histogram
	operationDuration metric.Float64Histogram
	timeToFirstToken  metric.Float64Histogram
	timePerOutputTok  metric.Float64Histogram
	loadDuration      metric.Float64Histogram
	routeDecisions    metric.Int64Counter
	turnDuration      metric.Float64Histogram
	turnIterations    metric.Int64Histogram
	contextTokens     metric.Int64Histogram
	activeStreams     metric.Int64UpDownCounter
	retrievalDuration metric.Float64Histogram
	toolCalls         metric.Int64Counter
	toolDuration      metric.Float64Histogram
	sessions          metric.Int64Counter
	turnTokens        metric.Int64Counter
	turnDocs          metric.Int64Histogram
}

// newInstruments creates every instrument on meter. Units follow the
// OpenTelemetry rules: "s" for seconds, and curly braces for a count of
// things, such as "{token}". It fails only if the SDK rejects a name or an
// option, which would be a bug in this file.
func newInstruments(meter metric.Meter) (*instruments, error) {
	// errs collects every creation error, so one bad instrument doesn't hide
	// another. errors.Join(errs...) returns nil when errs is empty.
	var errs []error
	// keep appends err to errs when it isn't nil. It is a closure: a
	// function value that can read and change errs from the enclosing scope.
	keep := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// in is a pointer to a new, zero-valued instruments struct. `&T{}`
	// allocates the struct and returns its address.
	in := &instruments{}
	var err error

	in.tokenUsage, err = meter.Int64Histogram(metricTokenUsage,
		metric.WithUnit("{token}"),
		metric.WithDescription("Tokens per model call, split by gen_ai.token.type (input or output)."),
		metric.WithExplicitBucketBoundaries(tokenBuckets...))
	keep(err)
	in.operationDuration, err = meter.Float64Histogram(metricOperationDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of one model call, as merud measured it."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	keep(err)
	in.timeToFirstToken, err = meter.Float64Histogram(metricTimeToFirstToken,
		metric.WithUnit("s"),
		metric.WithDescription("Time from sending a streamed request to the first text token."),
		metric.WithExplicitBucketBoundaries(firstTokenBuckets...))
	keep(err)
	in.timePerOutputTok, err = meter.Float64Histogram(metricTimePerOutputTok,
		metric.WithUnit("s"),
		metric.WithDescription("Decode time per output token, from the runtime's own counters."),
		metric.WithExplicitBucketBoundaries(perTokenBuckets...))
	keep(err)
	in.loadDuration, err = meter.Float64Histogram(metricLoadDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Time the runtime spent loading the model before a call; near zero when warm."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	keep(err)
	in.routeDecisions, err = meter.Int64Counter(metricRouteDecisions,
		metric.WithUnit("{decision}"),
		metric.WithDescription("Router decisions, by route and outcome."))
	keep(err)
	in.turnDuration, err = meter.Float64Histogram(metricTurnDuration,
		metric.WithUnit("s"),
		metric.WithDescription("End-to-end duration of one turn."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	keep(err)
	in.turnIterations, err = meter.Int64Histogram(metricTurnIterations,
		metric.WithUnit("{iteration}"),
		metric.WithDescription("Model calls in one turn's agent loop."),
		metric.WithExplicitBucketBoundaries(iterationBuckets...))
	keep(err)
	in.contextTokens, err = meter.Int64Histogram(metricContextTokens,
		metric.WithUnit("{token}"),
		metric.WithDescription("Tokens each prompt section used."),
		metric.WithExplicitBucketBoundaries(tokenBuckets...))
	keep(err)
	in.activeStreams, err = meter.Int64UpDownCounter(metricActiveStreams,
		metric.WithUnit("{stream}"),
		metric.WithDescription("Client sessions streaming from merud right now."))
	keep(err)
	in.retrievalDuration, err = meter.Float64Histogram(metricRetrievalDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of one retrieval stage: vector search, keyword search, fusion or memory recall."),
		metric.WithExplicitBucketBoundaries(retrievalBuckets...))
	keep(err)
	in.toolCalls, err = meter.Int64Counter(metricToolCalls,
		metric.WithUnit("{call}"),
		metric.WithDescription("Tool calls through dispatch, by server, tool and outcome."))
	keep(err)
	// Tool calls range from a local file write to a slow web fetch, the
	// same spread as model calls, so they share the duration edges.
	in.toolDuration, err = meter.Float64Histogram(metricToolDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of one tool call that ran, without the wait for the user's approval."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	keep(err)
	in.sessions, err = meter.Int64Counter(metricSessions,
		metric.WithUnit("{session}"),
		metric.WithDescription("Sessions started, by source."))
	keep(err)
	in.turnTokens, err = meter.Int64Counter(metricTurnTokens,
		metric.WithUnit("{token}"),
		metric.WithDescription("The main model's tokens per answered turn, summed over its model calls, by gen_ai.token.type, route and source."))
	keep(err)
	in.turnDocs, err = meter.Int64Histogram(metricTurnDocs,
		metric.WithUnit("{file}"),
		metric.WithDescription("Distinct files whose excerpts went into one answered turn's prompt."),
		metric.WithExplicitBucketBoundaries(docBuckets...))
	keep(err)

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return in, nil
}

// attr is a short name for attribute.String, which builds one key/value pair.
func attr(key, value string) attribute.KeyValue {
	return attribute.String(key, value)
}
