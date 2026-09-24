// This file is the observability API the rest of Meru calls. Other packages
// record through these functions and never import OpenTelemetry's SDK
// directly, so metric names and attribute rules live in one place.
//
// Until Setup runs with an endpoint, every function here is a no-op that costs
// next to nothing.

package obs

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// Setup starts metric and trace export to cfg.OTLPEndpoint. With no endpoint
// it leaves OpenTelemetry's no-op providers in place. It refuses a
// non-loopback endpoint. Call the returned shutdown function before exiting,
// so the last batch of data is sent.
func Setup(ctx context.Context, cfg config.Observability) (shutdown func(context.Context) error, err error) {
	return func(context.Context) error { return nil }, nil
}

// Tracer returns the tracer every Meru span comes from.
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
	Usage            engine.Usage
}

// RecordModelCall records gen_ai.client.token.usage,
// gen_ai.client.operation.duration, gen_ai.server.time_to_first_token,
// gen_ai.server.time_per_output_token and meru.engine.load.duration.
func RecordModelCall(ctx context.Context, c ModelCall) {}

// Turn describes one finished turn, for RecordTurn.
type Turn struct {
	Route      string // one of the four routes
	Source     string // "cli", "tui" or "job"
	Outcome    string // "ok", "error" or "cancelled"
	Duration   time.Duration
	Iterations int
}

// RecordTurn records meru.turn.duration and meru.turn.iterations.
func RecordTurn(ctx context.Context, t Turn) {}

// RecordRoute records one router decision in meru.route.decisions.
// outcome is "ok", "low_confidence" or "degraded".
func RecordRoute(ctx context.Context, route, outcome string) {}

// RecordContextTokens records meru.context.tokens for one prompt section:
// "system", "skills", "memories", "chunks", "history" or "tools".
func RecordContextTokens(ctx context.Context, section string, tokens int) {}

// ActiveStreams adds delta (+1 or -1) to meru.rpc.active_streams.
func ActiveStreams(ctx context.Context, delta int64) {}

// CaptureContent reports whether spans may carry prompt and response text.
// It is false unless config set capture_content = true.
func CaptureContent() bool { return false }
