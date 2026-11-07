// This file joins merud's log to its traces: a slog handler that stamps each
// line with the trace ID of the span in its context and hides any secret in
// it, and Preview, which cuts captured text down to a length that fits on one
// log line.

package obs

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"go.opentelemetry.io/otel/trace"
)

// maxPreview is the most characters of question or answer text a debug log
// line carries when capture_content is on. A preview helps match a log line
// to what the user asked; the whole text belongs in the transcript.
const maxPreview = 200

// LogHandler wraps next so that every record logged with a context that
// holds a span gains a trace_id attribute. The same ID names the turn's trace
// in any OTLP backend you run and sits on its transcript lines, so one grep
// finds all three.
//
// Only the *Context logging calls pass a context through, so code that wants
// the trace ID on a line calls log.DebugContext(ctx, ...) rather than
// log.Debug(...). A record with no span in its context passes through as is.
//
// Every record also passes through r, so a key a server echoes back in an
// error never reaches the log file: the message and each attribute come out
// with [secret:<name>] in place of the key. r may be nil, which hides nothing.
func LogHandler(next slog.Handler, r *Redactor) slog.Handler {
	return traceHandler{next: next, r: r}
}

// traceHandler is the slog.Handler LogHandler returns. It holds the handler
// it wraps and the Redactor, adds one attribute on the way through, and
// redacts the rest.
type traceHandler struct {
	next slog.Handler
	r    *Redactor
}

// Enabled reports whether next would write a record at level, so a debug
// line costs nothing at info level.
func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle builds a copy of r with its message and attributes redacted, adds
// trace_id when ctx carries a span with a trace ID, and passes the copy on.
// A record can't have its attributes replaced in place, so the copy starts
// from slog.NewRecord and takes each attribute through the Redactor.
func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.r.Redact(r.Message), r.PC)
	// Attrs calls the function once per attribute; returning true goes on.
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.r.attr(a))
		return true
	})
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		out.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	return h.next.Handle(ctx, out)
}

// WithAttrs returns a handler whose lines carry attrs as well, redacted with
// the secrets known now. It wraps the result again, so a logger built with
// log.With(...) keeps its trace IDs and its redaction.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hidden := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		hidden[i] = h.r.attr(a)
	}
	return traceHandler{next: h.next.WithAttrs(hidden), r: h.r}
}

// WithGroup returns a handler that nests later attributes under name,
// wrapped again for the same reason as WithAttrs.
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{next: h.next.WithGroup(name), r: h.r}
}

// Discard returns a logger that writes nothing. Packages that take an
// optional logger use it when the caller passes nil.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// Preview returns s cut to its first 200 characters, with "…" added when it
// was longer. Callers use it only when CaptureContent is true; without that,
// no question or answer text reaches the log at any level.
func Preview(s string) string {
	if utf8.RuneCountInString(s) <= maxPreview {
		return s
	}
	// Ranging over a string steps through characters (runes), and i is the
	// byte offset where each one starts. Stop at the 201st character.
	n := 0
	for i := range s {
		if n == maxPreview {
			return s[:i] + "…"
		}
		n++
	}
	return s
}
