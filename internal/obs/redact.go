// This file keeps secrets out of merud's log and its spans: Redactor, which
// holds the function that hides each stored secret, and the span exporter
// wrapper that runs it over every span before the span leaves merud. The
// log handler in log.go runs the same Redactor over every record. See
// ARCHITECTURE.md, "Secrets stay out of config".

package obs

import (
	"context"
	"log/slog"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
)

// Redactor hides secrets in text that merud writes to its log or a span.
// merud builds one at startup, hands it to LogHandler and Setup, and calls
// Set with the secret set each time it loads secrets.toml, so a key added
// through Settings is hidden from the next line on.
//
// It holds the function in an atomic.Pointer, not behind a mutex: a log line
// can come from code that holds any lock, and Redact must never wait on one.
// A nil *Redactor, and a Redactor whose Set was never called, return text as
// it is, which is what tests and the code before secrets load see.
type Redactor struct {
	f atomic.Pointer[func(string) string]
}

// NewRedactor returns a Redactor that hides nothing until Set is called.
func NewRedactor() *Redactor {
	return &Redactor{}
}

// Set makes f the function Redact runs, such as a secrets set's Redact.
func (r *Redactor) Set(f func(string) string) {
	r.f.Store(&f)
}

// Redact returns text with each secret replaced by [secret:<name>].
func (r *Redactor) Redact(text string) string {
	if r == nil || text == "" {
		return text
	}
	f := r.f.Load()
	if f == nil {
		return text
	}
	return (*f)(text)
}

// attr returns a with any secret hidden in its value. It resolves a
// LogValuer first, walks groups, and checks an error's text and the
// printed form of any other value. A value that holds no secret keeps its
// type, so the text handler prints it as before.
func (r *Redactor) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(r.Redact(v.String()))
	case slog.KindGroup:
		group := v.Group()
		out := make([]slog.Attr, len(group))
		for i, g := range group {
			out[i] = r.attr(g)
		}
		a.Value = slog.GroupValue(out...)
	case slog.KindAny:
		// An error prints as err.Error(); anything else as fmt prints it.
		// value.String gives the text the handler would write.
		s := v.String()
		if err, ok := v.Any().(error); ok {
			s = err.Error()
		}
		if hidden := r.Redact(s); hidden != s {
			a.Value = slog.StringValue(hidden)
		}
	default:
		a.Value = v
	}
	return a
}

// redactExporter wraps the exporter that sends spans to the OTLP endpoint.
// It hides secrets in each span's attributes, event attributes (where an
// error's message sits) and status text, at the one place every span passes,
// so no span-making code has to remember to.
type redactExporter struct {
	next trace.SpanExporter
	r    *Redactor
}

// ExportSpans wraps each span so its text comes out redacted, then hands
// them on.
func (e redactExporter) ExportSpans(ctx context.Context, spans []trace.ReadOnlySpan) error {
	out := make([]trace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = redactedSpan{ReadOnlySpan: s, r: e.r}
	}
	return e.next.ExportSpans(ctx, out)
}

// Shutdown stops the wrapped exporter.
func (e redactExporter) Shutdown(ctx context.Context) error {
	return e.next.Shutdown(ctx)
}

// redactedSpan is a span as the exporter sees it, with its text redacted.
// Embedding trace.ReadOnlySpan gives it every method of the span it wraps,
// so it only redefines the three that carry text.
type redactedSpan struct {
	trace.ReadOnlySpan
	r *Redactor
}

// Attributes returns the span's attributes with secrets hidden.
func (s redactedSpan) Attributes() []attribute.KeyValue {
	return s.r.kvs(s.ReadOnlySpan.Attributes())
}

// Events returns the span's events, such as a recorded error, with
// secrets hidden in their attributes.
func (s redactedSpan) Events() []trace.Event {
	events := s.ReadOnlySpan.Events()
	out := make([]trace.Event, len(events))
	for i, ev := range events {
		ev.Attributes = s.r.kvs(ev.Attributes)
		out[i] = ev
	}
	return out
}

// Status returns the span's status with secrets hidden in its text.
func (s redactedSpan) Status() trace.Status {
	st := s.ReadOnlySpan.Status()
	st.Description = s.r.Redact(st.Description)
	return st
}

// kvs returns kvs with secrets hidden in every string and string-list value.
func (r *Redactor) kvs(kvs []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, len(kvs))
	for i, kv := range kvs {
		switch kv.Value.Type() {
		case attribute.STRING:
			kv.Value = attribute.StringValue(r.Redact(kv.Value.AsString()))
		case attribute.STRINGSLICE:
			list := kv.Value.AsStringSlice()
			for j := range list {
				list[j] = r.Redact(list[j])
			}
			kv.Value = attribute.StringSliceValue(list)
		}
		out[i] = kv
	}
	return out
}
