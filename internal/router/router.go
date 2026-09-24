// This file holds Decide, the router's one entry point, with the types it
// takes and returns.

package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
)

// Route is one of the four ways to answer a turn. It is a named string type,
// so the compiler stops a plain string from standing in for a route.
type Route string

// The four routes (ARCHITECTURE.md, "Agent loop").
const (
	RouteDirect      Route = "direct"       // answer from the model alone
	RouteSearch      Route = "search"       // retrieve from the user's files first
	RouteTools       Route = "tools"        // call MCP tools
	RouteSearchTools Route = "search+tools" // both
)

// ParseRoute returns the Route named s, and false when s names none of the
// four. Config validation uses it to check router.fallback.
func ParseRoute(s string) (Route, bool) {
	for _, o := range options {
		if string(o.route) == s {
			return o.route, true
		}
	}
	return "", false
}

// Outcome says how sure the router was.
type Outcome string

// The three outcomes. They are also the "outcome" attribute on the
// meru.route.decisions metric, so the set must stay small and fixed.
const (
	// OutcomeOK means the model gave a clear answer above the confidence floor.
	OutcomeOK Outcome = "ok"
	// OutcomeLowConfidence means the winning route scored below MinConfidence.
	OutcomeLowConfidence Outcome = "low_confidence"
	// OutcomeDegraded means fewer than two route letters appeared among the
	// alternatives, so the distribution means nothing.
	OutcomeDegraded Outcome = "degraded"
)

// Config tunes Decide. Build it from config.toml with ConfigFrom.
type Config struct {
	Model         string  // the fast-tier model
	TopLogProbs   int     // alternatives to ask for; Ollama's cap is 20
	Temperature   float64 // above 0; 1.0 leaves the probabilities as the model gave them
	MinConfidence float64 // 0 to 1; a winner below this takes the fallback
	Fallback      Route   // the route to take when the model is unsure
	// Log receives one debug line per decision. Nil means no log lines.
	// ConfigFrom leaves it nil; merud sets it to its own logger.
	Log *slog.Logger
}

// ConfigFrom builds a Config from the [router] table and the fast model's
// name. It fails when a value is out of range, so a bad config.toml stops
// merud at startup instead of on the first turn.
func ConfigFrom(r config.Router, fastModel string) (Config, error) {
	fb, ok := ParseRoute(r.Fallback)
	if !ok {
		return Config{}, fmt.Errorf("router.fallback %q: want one of direct, search, tools, search+tools", r.Fallback)
	}
	cfg := Config{
		Model:         fastModel,
		TopLogProbs:   r.TopLogProbs,
		Temperature:   r.Temperature,
		MinConfidence: r.MinConfidence,
		Fallback:      fb,
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate checks every field of cfg and returns the first problem it finds.
//
// (cfg Config) is a value receiver: the method gets its own copy of cfg,
// which is fine for a small struct it only reads.
func (cfg Config) validate() error {
	// A switch with no value runs the first case whose condition is true.
	switch {
	case cfg.Model == "":
		return errors.New("router: no fast model configured")
	case cfg.TopLogProbs < 1 || cfg.TopLogProbs > 20:
		return fmt.Errorf("router.top_logprobs %d: want 1 to 20", cfg.TopLogProbs)
	case !(cfg.Temperature > 0): // written this way so NaN fails too
		return fmt.Errorf("router.temperature %v: want a number above 0", cfg.Temperature)
	case !(cfg.MinConfidence >= 0 && cfg.MinConfidence <= 1):
		return fmt.Errorf("router.min_confidence %v: want 0 to 1", cfg.MinConfidence)
	}
	if _, ok := ParseRoute(string(cfg.Fallback)); !ok {
		return fmt.Errorf("router.fallback %q: want one of direct, search, tools, search+tools", cfg.Fallback)
	}
	return nil
}

// Turn is what the router sees of one turn.
type Turn struct {
	// SystemPrompt goes first, as a system message. Empty leaves it out.
	SystemPrompt string
	// History is this session's earlier messages, oldest first, already cut
	// to the history budget. The prompt shows them newest first.
	History []engine.Message
	// Question is the user's new message.
	Question string
	// Folders lists the folders Meru indexes, as config.toml writes them,
	// such as "~/repos/meru". The prompt names them, so a question about a
	// project kept there goes to search. Empty leaves the line out.
	Folders []string
}

// Decision is what the router concluded.
type Decision struct {
	// Route is the chosen route, or cfg.Fallback when Outcome isn't ok.
	Route Route
	// Confidence is the winning letter's probability after normalising, 0
	// to 1. With a low_confidence outcome it is the score that fell short,
	// not the fallback's. It is 0 when the outcome is degraded.
	Confidence float64
	Outcome    Outcome
	// Probs is the whole distribution. It may be nil or hold one route when
	// the outcome is degraded.
	Probs map[Route]float64
}

// Decide picks a route for one turn. It sends a single prompt to the fast
// model, reads the probabilities the model gave each route letter, and
// returns the most likely route with its confidence.
//
// It returns an error only when cfg is invalid or the model call itself
// fails. A model that answers unclearly is not an error: Decide returns the
// fallback route and says why in Outcome, so the caller can carry on and the
// metrics can record it.
//
// Decide records a "meru.route" span with a gen_ai.chat span under it for
// the model call, the meru.route.decisions metric, and a debug log line
// with the whole distribution.
func Decide(ctx context.Context, eng engine.Engine, cfg Config, turn Turn) (Decision, error) {
	start := time.Now()
	// Start returns a new ctx that carries the span, so spans started
	// further down (the model call) nest under it. defer ends the span when
	// Decide returns, on every path.
	ctx, span := obs.Tracer().Start(ctx, "meru.route")
	defer span.End()

	if err := cfg.validate(); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Decision{}, err
	}

	comp, err := ask(ctx, eng, cfg, turn)
	if err != nil {
		obs.EndSpanErr(ctx, span, err)
		return Decision{}, fmt.Errorf("route: %w", err)
	}

	d := decide(comp, cfg)

	obs.RecordRoute(ctx, string(d.Route), string(d.Outcome))
	span.SetAttributes(
		attribute.String("meru.route.decision", string(d.Route)),
		attribute.Float64("meru.route.confidence", d.Confidence),
		attribute.String("meru.route.outcome", string(d.Outcome)),
	)
	// One number per route, such as meru.route.p.direct = 0.21. The four
	// probabilities are numbers about the router, not text from the user,
	// so they go on every span. A route missing from the distribution is 0.
	for _, o := range options {
		span.SetAttributes(attribute.Float64("meru.route.p."+string(o.route), d.Probs[o.route]))
	}
	logger(cfg).DebugContext(ctx, "route", "route", d.Route,
		"confidence", round3(d.Confidence), "outcome", d.Outcome,
		"probs", formatProbs(d.Probs), "model", cfg.Model,
		"ms", time.Since(start).Milliseconds())
	return d, nil
}

// ask sends the routing prompt to the fast model and returns its one-token
// completion, inside a gen_ai.chat span. One token with log probabilities
// means the model pays for reading the prompt and decoding one letter,
// nothing more.
func ask(ctx context.Context, eng engine.Engine, cfg Config, turn Turn) (engine.Completion, error) {
	ctx, span := obs.StartChat(ctx, obs.Chat{Tier: "fast", Model: cfg.Model, MaxTokens: 1})
	defer span.End()
	comp, err := eng.Generate(ctx, buildMessages(turn), nil, engine.Options{
		Model:       cfg.Model,
		MaxTokens:   1,
		LogProbs:    true,
		TopLogProbs: cfg.TopLogProbs,
	})
	if err != nil {
		obs.EndSpanErr(ctx, span, err)
		return engine.Completion{}, err
	}
	u := comp.Usage
	obs.ChatResult(span, obs.Usage{
		PromptTokens: u.PromptTokens, OutputTokens: u.OutputTokens,
		LoadDuration: u.LoadDuration, PromptEvalDuration: u.PromptEvalDuration,
		EvalDuration: u.EvalDuration,
	}, comp.DoneReason)
	return comp, nil
}

// logger returns cfg.Log, or a logger that writes nothing when it is nil,
// so a Config built in a test needs no logger.
func logger(cfg Config) *slog.Logger {
	if cfg.Log == nil {
		return obs.Discard()
	}
	return cfg.Log
}

// round3 rounds p to three decimal places for the log line, where more
// digits would be noise.
func round3(p float64) float64 {
	return math.Round(p*1000) / 1000
}

// decide reads a Decision out of a finished completion. It is Decide
// without the I/O, split out so tests can feed it fixtures.
func decide(comp engine.Completion, cfg Config) Decision {
	var p map[Route]float64
	if len(comp.LogProbs) > 0 {
		p = probs(comp.LogProbs[0], cfg.Temperature)
	}
	// Fewer than two letters means the model didn't weigh the options at
	// all, so its "confidence" means nothing.
	if len(p) < 2 {
		return Decision{Route: cfg.Fallback, Outcome: OutcomeDegraded, Probs: p}
	}
	win, conf := best(p)
	if conf < cfg.MinConfidence {
		return Decision{Route: cfg.Fallback, Confidence: conf, Outcome: OutcomeLowConfidence, Probs: p}
	}
	return Decision{Route: win, Confidence: conf, Outcome: OutcomeOK, Probs: p}
}

// formatProbs writes a distribution as "direct=0.120 search=0.050 ...", in
// letter order so the text is stable from run to run.
func formatProbs(p map[Route]float64) string {
	parts := make([]string, 0, len(options))
	for _, o := range options {
		parts = append(parts, fmt.Sprintf("%s=%.3f", o.route, p[o.route]))
	}
	return strings.Join(parts, " ")
}
