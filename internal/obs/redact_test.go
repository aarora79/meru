// This file tests that secrets never reach the log or an exported span:
// the log handler's redaction of messages and attributes, a secret set
// after the logger is built, and the span exporter wrapper.

package obs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// key stands in for a stored secret; hideKey is the redaction a secrets
// set would run.
const key = "opensesame-opensesame"

func hideKey(s string) string { return strings.ReplaceAll(s, key, "[secret:test_key]") }

// logValue is a slog.LogValuer whose value holds the key, to check that
// the handler resolves a value before it redacts it.
type logValue struct{}

func (logValue) LogValue() slog.Value { return slog.StringValue("token " + key) }

func TestLogHandlerRedacts(t *testing.T) {
	var buf bytes.Buffer
	r := NewRedactor()
	r.Set(hideKey)
	log := slog.New(LogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), r))

	log.With("header", "Bearer "+key).Warn("server said invalid key "+key,
		"err", errors.New("connect: invalid key "+key),
		"body", key,
		slog.Group("request", "auth", key),
		"valuer", logValue{},
		"count", 3)

	out := buf.String()
	if strings.Contains(out, key) {
		t.Fatalf("the log holds the key:\n%s", out)
	}
	for _, want := range []string{
		`msg="server said invalid key [secret:test_key]"`,
		`err="connect: invalid key [secret:test_key]"`,
		`body=[secret:test_key]`,
		`request.auth=[secret:test_key]`,
		`valuer="token [secret:test_key]"`,
		`header="Bearer [secret:test_key]"`,
		`count=3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %s:\n%s", want, out)
		}
	}
}

func TestRedactorSetLater(t *testing.T) {
	// A logger built before secrets load hides nothing, then hides the key
	// from the first line after Set, with no new logger.
	var buf bytes.Buffer
	r := NewRedactor()
	log := slog.New(LogHandler(slog.NewTextHandler(&buf, nil), r))
	log.Info("before", "v", key)
	if !strings.Contains(buf.String(), key) {
		t.Fatalf("before Set, the line should pass as is:\n%s", buf.String())
	}
	buf.Reset()
	r.Set(hideKey)
	log.Info("after", "v", key)
	if strings.Contains(buf.String(), key) {
		t.Errorf("after Set, the line still holds the key:\n%s", buf.String())
	}
}

func TestNilRedactor(t *testing.T) {
	var r *Redactor
	if got := r.Redact(key); got != key {
		t.Errorf("a nil Redactor changed the text to %q", got)
	}
}

func TestSpanExporterRedacts(t *testing.T) {
	r := NewRedactor()
	r.Set(hideKey)
	mem := tracetest.NewInMemoryExporter()
	// A syncer exports each span when it ends, so the test needs no flush.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(redactExporter{next: mem, r: r}))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	_, span := tp.Tracer("test").Start(context.Background(), "call")
	span.SetAttributes(attribute.String("server.url", "https://x.example/?key="+key),
		attribute.StringSlice("args", []string{"a", key}), attribute.Int("n", 2))
	span.RecordError(errors.New("invalid key " + key))
	span.SetStatus(codes.Error, "invalid key "+key)
	span.End()

	spans := mem.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	var all []string
	for _, kv := range s.Attributes {
		all = append(all, kv.Value.String())
	}
	for _, ev := range s.Events {
		for _, kv := range ev.Attributes {
			all = append(all, kv.Value.String())
		}
	}
	all = append(all, s.Status.Description)
	text := strings.Join(all, "\n")
	if strings.Contains(text, key) {
		t.Fatalf("the exported span holds the key:\n%s", text)
	}
	if !strings.Contains(text, "invalid key [secret:test_key]") || !strings.Contains(text, "?key=[secret:test_key]") {
		t.Errorf("the span lacks the redacted text:\n%s", text)
	}
}
