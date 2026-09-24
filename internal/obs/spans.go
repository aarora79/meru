// This file holds the span helpers more than one package needs: the
// gen_ai.chat span every model call gets, and the one way Meru marks a span
// as failed or cancelled. The span tree they build is drawn in
// ARCHITECTURE.md, "Observability" → "Traces".

package obs

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Chat describes one model call, for StartChat.
type Chat struct {
	Tier      string // "fast" or "main"
	Model     string // the model name from config
	MaxTokens int    // the cap sent to the model; zero means none, and is left off
	Stream    bool   // true when the answer streams back piece by piece
}

// StartChat starts a gen_ai.chat span for one model call and returns a ctx
// that carries it, so the engine's HTTP span nests under it. The attribute
// names follow the OpenTelemetry GenAI semantic conventions; meru.tier and
// meru.stream are Meru's own. The caller sets the result with ChatResult or
// EndSpanErr, then ends the span.
func StartChat(ctx context.Context, c Chat) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", c.Model),
		attribute.String("meru.tier", c.Tier),
		attribute.Bool("meru.stream", c.Stream),
	}
	if c.MaxTokens > 0 {
		attrs = append(attrs, attribute.Int("gen_ai.request.max_tokens", c.MaxTokens))
	}
	// SpanKindClient marks this span as a call out to another service.
	return Tracer().Start(ctx, "gen_ai.chat",
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// ChatResult sets what a finished model call reported on its gen_ai.chat
// span: token counts, why the model stopped, and the runtime's own timings
// in milliseconds. The timings say where the call's time went: loading the
// model, reading the prompt, or writing the answer.
func ChatResult(span trace.Span, u Usage, finishReason string) {
	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", u.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", u.OutputTokens),
		attribute.Int64("meru.ollama.load_ms", u.LoadDuration.Milliseconds()),
		attribute.Int64("meru.ollama.prompt_eval_ms", u.PromptEvalDuration.Milliseconds()),
		attribute.Int64("meru.ollama.eval_ms", u.EvalDuration.Milliseconds()),
	)
	if finishReason != "" {
		// The convention makes this a list, one entry per answer choice.
		// Ollama returns one choice.
		span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{finishReason}))
	}
}

// EndSpanErr marks span with how its work ended, when err isn't nil. A
// cancelled call (ctx ended, or err wraps context.Canceled) gets a
// "cancelled" event and keeps an unset status, because the user pressing
// Esc isn't a fault. Any other error is recorded as an exception event and
// sets the span's status to Error. It doesn't end the span.
func EndSpanErr(ctx context.Context, span trace.Span, err error) {
	if err == nil {
		return
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		span.AddEvent("cancelled")
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
