// This file holds the Agent and its one entry point, Handle, which runs a
// whole turn for one rpc request: session, route, prompt, streamed answer,
// transcript lines, spans and metrics.

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// DefaultSystemPrompt is the system prompt when config sets none.
const DefaultSystemPrompt = "You are Meru, a personal assistant that runs entirely on the user's own computer. " +
	"Answer clearly and briefly. If you don't know something, say so."

// Router picks a route for one turn. The agent defines the interface with
// only the method it calls, so this package doesn't depend on the router's
// own types; merud passes in a small adapter around internal/router.
type Router interface {
	// Decide returns the route for question, given the session's history. It
	// returns an error only when the model call fails; an unsure model gives
	// the fallback route with a non-"ok" outcome instead.
	Decide(ctx context.Context, question string, history []engine.Message) (Decision, error)
}

// Decision is what the router concluded. See docs/fast-router.md.
type Decision struct {
	Route      string  // "direct", "search", "tools" or "search+tools"
	Confidence float64 // 0 to 1
	Outcome    string  // "ok", "low_confidence" or "degraded"
}

// Agent runs turns. Build one with New and share it: Handle keeps no state
// between calls, so many turns can run at once.
type Agent struct {
	engine      engine.Engine
	router      Router
	models      config.Models
	historyN    int    // earlier turns to put in the prompt
	system      string // system prompt
	sessionsDir string // where transcripts live, usually ~/.meru/sessions
	log         *slog.Logger
}

// New returns an Agent that answers with eng, routes with router and keeps
// transcripts under cfg.Dir/sessions.
func New(cfg config.Config, eng engine.Engine, router Router, log *slog.Logger) *Agent {
	system := cfg.Agent.SystemPrompt
	if system == "" {
		system = DefaultSystemPrompt
	}
	// &Agent{...} builds the struct and returns a pointer to it, so every
	// caller shares one Agent instead of copying it.
	return &Agent{
		engine:      eng,
		router:      router,
		models:      cfg.Models,
		historyN:    cfg.Agent.HistoryTurns,
		system:      system,
		sessionsDir: filepath.Join(cfg.Dir, "sessions"),
		log:         log,
	}
}

// Handle runs one turn for req and sends its events through emit, in this
// order: "session", "route", then one "token" per piece of the answer. It has
// the rpc.Handler signature, so merud passes a.Handle straight to rpc.Serve,
// which sends the closing "done" or "error".
//
// Handle fails when the request is bad (empty question, unknown session or
// source), when the transcript can't be written, when a model call fails, or
// when ctx is cancelled. A cancelled turn writes no assistant line.
//
// err is a named result, so the deferred function below can read the final
// error and record the turn's outcome whichever return statement ran.
func (a *Agent) Handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) (err error) {
	start := time.Now()
	question := strings.TrimSpace(req.Text)
	if question == "" {
		return errors.New("empty question")
	}
	source, err := sourceOf(req.Source)
	if err != nil {
		return err
	}

	ctx, span := obs.Tracer().Start(ctx, "meru.turn")
	var route, sessionID string
	iterations := 0
	defer func() {
		outcome := outcomeOf(ctx, err)
		span.SetAttributes(
			attribute.String("meru.route", route),
			attribute.String("meru.source", source),
			attribute.String("meru.session.id", sessionID),
			attribute.Int("meru.turn.iterations", iterations),
			attribute.String("meru.turn.outcome", outcome),
		)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
		// The turn's ctx may be cancelled by now. WithoutCancel keeps its
		// values (the trace) but drops the cancel, so the metric still records.
		obs.RecordTurn(context.WithoutCancel(ctx), obs.Turn{
			Route: route, Source: source, Outcome: outcome,
			Duration: time.Since(start), Iterations: iterations,
		})
		// Only the error, never the question or answer, goes in the log.
		a.log.Info("turn", "session", sessionID, "route", route, "source", source,
			"outcome", outcome, "ms", time.Since(start).Milliseconds(), "err", err)
	}()
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("meru.question", question))
	}

	sess, err := a.session(req.Session)
	if err != nil {
		return err
	}
	sessionID = sess.ID()
	if err := emit(rpc.Event{Type: rpc.EventSession, Session: sessionID}); err != nil {
		return err
	}

	// Read the history before writing this turn's question, so the question
	// isn't in it twice.
	history, err := sess.History(a.historyN)
	if err != nil {
		return err
	}
	traceID := traceIDOf(span)
	if err := sess.Append(transcript.Line{Type: transcript.TypeUser, Text: question, TraceID: traceID}); err != nil {
		return err
	}

	dec, err := a.route(ctx, question, history)
	if err != nil {
		return err
	}
	route = dec.Route
	if err := emit(rpc.Event{Type: rpc.EventRoute, Route: dec.Route, Confidence: dec.Confidence}); err != nil {
		return err
	}

	// v0.1 answers every route directly: search arrives in v0.2 and tools in
	// v0.3. The route is still recorded above, so the metrics show how often
	// each one would have run.
	msgs := buildMessages(a.system, history, question)
	iterations = 1
	answer, usage, err := a.answer(ctx, msgs, emit)
	if err != nil {
		return err
	}
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("meru.answer", answer))
	}

	return sess.Append(transcript.Line{
		Type:      transcript.TypeAssistant,
		Text:      answer,
		TokensIn:  usage.PromptTokens,
		TokensOut: usage.OutputTokens,
		TraceID:   traceID,
	})
}

// session opens the session named id, or starts a new one when id is empty.
func (a *Agent) session(id string) (*transcript.Session, error) {
	if id == "" {
		return transcript.New(a.sessionsDir)
	}
	return transcript.Open(a.sessionsDir, id)
}

// route asks the router for this turn's route. The router records the
// meru.route span and the meru.route.decisions metric itself, so the agent
// records neither.
func (a *Agent) route(ctx context.Context, question string, history []engine.Message) (Decision, error) {
	dec, err := a.router.Decide(ctx, question, history)
	if err != nil {
		return Decision{}, fmt.Errorf("route: %w", err)
	}
	return dec, nil
}

// answer streams the main model's reply to msgs, sends each piece through
// emit as a "token" event, and returns the whole text with the runtime's
// usage counters. It records one gen_ai.chat span and the model-call metrics.
func (a *Agent) answer(ctx context.Context, msgs []engine.Message, emit func(rpc.Event) error) (string, engine.Usage, error) {
	model := a.models.Main
	ctx, span := obs.Tracer().Start(ctx, "gen_ai.chat", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", model),
		attribute.String("meru.tier", "main"),
	)

	start := time.Now()
	var ttft time.Duration // time to first token; zero until text arrives
	var usage engine.Usage
	var text strings.Builder

	// fail marks the span as failed and returns err with context added.
	fail := func(err error) (string, engine.Usage, error) {
		span.SetStatus(codes.Error, err.Error())
		return "", engine.Usage{}, fmt.Errorf("main model %s: %w", model, err)
	}

	stream, err := a.engine.Stream(ctx, msgs, nil, engine.Options{Model: model})
	if err != nil {
		return fail(err)
	}
	// range over an iterator function: each loop pass gets the next piece.
	for delta, err := range stream {
		if err != nil {
			return fail(err)
		}
		if delta.Text != "" {
			if ttft == 0 {
				ttft = time.Since(start)
			}
			text.WriteString(delta.Text)
			if err := emit(rpc.Event{Type: rpc.EventToken, Text: delta.Text}); err != nil {
				return fail(err)
			}
		}
		if delta.Done {
			usage = delta.Usage
		}
	}
	// A stream can end early without an error when ctx is cancelled.
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", usage.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", usage.OutputTokens),
	)
	obs.RecordModelCall(ctx, obs.ModelCall{
		Tier: "main", Model: model, Operation: "chat",
		Duration: time.Since(start), TimeToFirstToken: ttft,
		Usage: obs.Usage{
			PromptTokens: usage.PromptTokens, OutputTokens: usage.OutputTokens,
			LoadDuration: usage.LoadDuration, EvalDuration: usage.EvalDuration,
		},
	})
	return text.String(), usage, nil
}

// buildMessages puts the prompt together: the system prompt, the session's
// earlier turns, then the new question.
func buildMessages(system string, history []engine.Message, question string) []engine.Message {
	msgs := make([]engine.Message, 0, len(history)+2)
	msgs = append(msgs, engine.Message{Role: engine.RoleSystem, Content: system})
	msgs = append(msgs, history...) // ... spreads the slice into separate arguments
	msgs = append(msgs, engine.Message{Role: engine.RoleUser, Content: question})
	return msgs
}

// sourceOf checks the request's source and returns it as a metric value. An
// empty source means the one-shot CLI. Any other value is refused, because
// metric attributes must stay a small fixed set.
func sourceOf(s rpc.Source) (string, error) {
	switch s {
	case "":
		return string(rpc.SourceCLI), nil
	case rpc.SourceCLI, rpc.SourceTUI, rpc.SourceJob:
		return string(s), nil
	default:
		return "", fmt.Errorf("unknown source %q", s)
	}
}

// outcomeOf turns a turn's error into the outcome metric value.
func outcomeOf(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return "ok"
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "error"
	}
}

// traceIDOf returns the span's trace ID as hex, or "" when tracing is off.
func traceIDOf(span trace.Span) string {
	sc := span.SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
