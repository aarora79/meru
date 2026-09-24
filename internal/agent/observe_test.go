// This file tests what a turn tells the log and the tracer. It runs whole
// turns through the rpc server, the agent and the real router over a fake
// engine, then checks the span tree, the debug and info log lines, and that
// no question or answer text leaks into either unless capture_content is on.

package agent

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/router"
	"github.com/aarora79/meru/internal/rpc"
)

// The question and answer carry words that appear nowhere else, so a search
// of the log or the spans for them finds only leaked text.
const (
	secretQuestion = "what does the zebracorn eat?"
	secretAnswer   = "The quokkafruit."
)

// lockedBuffer is a bytes.Buffer that many goroutines may write to at once:
// the rpc server logs from its connection goroutines. Embedding bytes.Buffer
// gives lockedBuffer all its methods; Write and String replace two of them
// with locked versions.
type lockedBuffer struct {
	mu sync.Mutex // guards the buffer
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

// bufferLog returns a logger at level that writes merud's key=value lines,
// trace IDs included, to the returned buffer.
func bufferLog(level slog.Level) (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	return slog.New(obs.LogHandler(h)), buf
}

// recordSpans installs a tracer provider that keeps every finished span in
// memory, and puts the no-op provider back when the test ends.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(tracenoop.NewTracerProvider()) })
	return rec
}

// captureContent turns capture_content on for one test. obs.Setup installs
// its own tracer provider, so call this before recordSpans.
func captureContent(t *testing.T) {
	t.Helper()
	if _, err := obs.Setup(context.Background(), config.Observability{CaptureContent: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = obs.Setup(context.Background(), config.Observability{})
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	})
}

// realRouter adapts router.Decide to the agent's Router interface, the way
// merud does, so the turn gets a real meru.route span and log line.
type realRouter struct {
	eng engine.Engine
	cfg router.Config
}

func (r realRouter) Decide(ctx context.Context, question string, history []engine.Message) (Decision, error) {
	d, err := router.Decide(ctx, r.eng, r.cfg, router.Turn{History: history, Question: question})
	if err != nil {
		return Decision{}, err
	}
	return Decision{Route: string(d.Route), Confidence: d.Confidence, Outcome: string(d.Outcome)}, nil
}

// observedAgent builds an agent over a fake engine whose Generate answers
// the router with "A" (direct) at about 0.78 after the default temperature
// of 1.25, and whose Stream writes secretAnswer. Every part logs to log.
func observedAgent(t *testing.T, log *slog.Logger) (*Agent, config.Config) {
	t.Helper()
	cfg := testConfig(t)
	eng := &fakeEngine{
		pieces: []string{"The quokka", "fruit."},
		usage:  engine.Usage{PromptTokens: 40, OutputTokens: 5},
		gen: engine.Completion{
			DoneReason: "length",
			Usage:      engine.Usage{PromptTokens: 90, OutputTokens: 1},
			LogProbs: []engine.PositionLogProbs{{Top: []engine.TokenLogProb{
				{Token: "A", LogProb: -0.1}, {Token: "B", LogProb: -2.5},
				{Token: "C", LogProb: -3.0}, {Token: "D", LogProb: -4.0},
			}}},
		},
	}
	rc, err := router.ConfigFrom(cfg.Router, "fast-model")
	if err != nil {
		t.Fatal(err)
	}
	rc.Log = log
	return New(cfg, eng, realRouter{eng: eng, cfg: rc}, nil, nil, nil, log), cfg
}

// askOverSocket serves a on a fresh Unix socket, asks each question in turn
// in one session, and stops the server. It returns the session ID. Stopping
// the server waits for every connection to finish, so all spans have ended
// and all log lines are written when it returns.
func askOverSocket(t *testing.T, a *Agent, log *slog.Logger, questions ...string) string {
	t.Helper()
	// A short socket path: macOS caps them at 104 bytes.
	sockDir, err := os.MkdirTemp("", "meru")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "merud.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := rpc.Listen(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(ctx, ln, a.Handle, log) }()

	session := ""
	for _, q := range questions {
		for ev, err := range rpc.Do(context.Background(), sock, rpc.Request{Op: rpc.OpAsk, Text: q, Session: session, Source: rpc.SourceTUI}, nil) {
			if err != nil {
				t.Fatalf("ask: %v", err)
			}
			if ev.Type == rpc.EventSession {
				session = ev.Session
			}
			if ev.Type == rpc.EventError {
				t.Fatalf("error event: %s", ev.Error)
			}
		}
	}
	cancel()
	if err := <-served; err != nil {
		t.Errorf("Serve: %v", err)
	}
	return session
}

// spanTree holds the finished spans of a test, so it can find a span by
// name and attributes and walk from it to its children.
type spanTree struct {
	spans []sdktrace.ReadOnlySpan
}

// roots returns the spans with no parent.
func (st spanTree) roots() []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range st.spans {
		if !s.Parent().IsValid() {
			out = append(out, s)
		}
	}
	return out
}

// children returns the sorted names of parent's direct children.
func (st spanTree) children(parent sdktrace.ReadOnlySpan) []string {
	var names []string
	for _, s := range st.spans {
		if s.Parent().SpanID() == parent.SpanContext().SpanID() {
			names = append(names, s.Name())
		}
	}
	slices.Sort(names)
	return names
}

// find returns the first span named name that has every attribute in want,
// and fails the test when there is none.
func (st spanTree) find(t *testing.T, name string, want ...attribute.KeyValue) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range st.spans {
		if s.Name() != name {
			continue
		}
		ok := true
		for _, kv := range want {
			if v, found := attrOf(s, string(kv.Key)); !found || v != kv.Value {
				ok = false
			}
		}
		if ok {
			return s
		}
	}
	t.Fatalf("no %s span with %v", name, want)
	return nil
}

// attrOf returns the value of key on span, and false when it is missing.
func attrOf(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// leaks reports every span attribute or event attribute that holds the
// question's or the answer's words.
func leaks(spans []sdktrace.ReadOnlySpan) []string {
	var out []string
	check := func(where string, kvs []attribute.KeyValue) {
		for _, kv := range kvs {
			v := kv.Value.String()
			if strings.Contains(v, "zebracorn") || strings.Contains(v, "quokka") {
				out = append(out, where+" "+string(kv.Key))
			}
		}
	}
	for _, s := range spans {
		check(s.Name(), s.Attributes())
		for _, ev := range s.Events() {
			check(s.Name()+"/"+ev.Name, ev.Attributes)
		}
	}
	return out
}

func TestTurnSpanTree(t *testing.T) {
	rec := recordSpans(t)
	log, _ := bufferLog(slog.LevelDebug)
	a, _ := observedAgent(t, log)
	askOverSocket(t, a, log, secretQuestion)

	st := spanTree{spans: rec.Ended()}
	roots := st.roots()
	if len(roots) != 1 || roots[0].Name() != "rpc.request" {
		var names []string
		for _, r := range roots {
			names = append(names, r.Name())
		}
		t.Fatalf("roots = %v, want one rpc.request", names)
	}
	root := roots[0]
	if got := st.children(root); !slices.Equal(got, []string{"meru.turn"}) {
		t.Errorf("rpc.request children = %v, want [meru.turn]", got)
	}
	turn := st.find(t, "meru.turn")
	wantTurn := []string{"gen_ai.chat", "meru.prompt", "meru.route", "meru.session",
		"meru.transcript.append", "meru.transcript.append"}
	if got := st.children(turn); !slices.Equal(got, wantTurn) {
		t.Errorf("meru.turn children = %v, want %v", got, wantTurn)
	}
	route := st.find(t, "meru.route")
	if got := st.children(route); !slices.Equal(got, []string{"gen_ai.chat"}) {
		t.Errorf("meru.route children = %v, want [gen_ai.chat]", got)
	}

	// Every span shares the root's trace.
	for _, s := range st.spans {
		if s.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Errorf("%s is in another trace", s.Name())
		}
	}

	st.find(t, "rpc.request", attribute.String("meru.rpc.op", "ask"), attribute.String("meru.source", "tui"))
	st.find(t, "meru.turn", attribute.String("meru.route", "direct"), attribute.String("meru.turn.outcome", "ok"))
	st.find(t, "meru.session", attribute.Bool("meru.session.new", true), attribute.Int("meru.history.turns", 0))
	st.find(t, "meru.prompt", attribute.Int("meru.prompt.messages", 2))
	st.find(t, "meru.transcript.append", attribute.String("meru.transcript.type", "user"))
	st.find(t, "meru.transcript.append", attribute.String("meru.transcript.type", "assistant"))
	st.find(t, "gen_ai.chat",
		attribute.String("meru.tier", "fast"),
		attribute.String("gen_ai.request.model", "fast-model"),
		attribute.Int("gen_ai.request.max_tokens", 1),
		attribute.Int("gen_ai.usage.input_tokens", 90),
		attribute.StringSlice("gen_ai.response.finish_reasons", []string{"length"}))
	main := st.find(t, "gen_ai.chat",
		attribute.String("meru.tier", "main"),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.Int("gen_ai.usage.output_tokens", 5))

	// The route's probabilities are numbers, one attribute per route.
	for _, key := range []string{"meru.route.p.direct", "meru.route.p.search", "meru.route.p.tools", "meru.route.p.search+tools"} {
		if _, ok := attrOf(route, key); !ok {
			t.Errorf("meru.route lacks %s", key)
		}
	}
	if v, _ := attrOf(route, "meru.route.p.direct"); v.AsFloat64() < 0.7 {
		t.Errorf("meru.route.p.direct = %v, want above 0.7", v.AsFloat64())
	}

	var first bool
	for _, ev := range main.Events() {
		if ev.Name == "first_token" {
			first = true
		}
	}
	if !first {
		t.Error("main gen_ai.chat has no first_token event")
	}

	if got := leaks(st.spans); len(got) > 0 {
		t.Errorf("spans hold question or answer text with capture_content off: %v", got)
	}
}

func TestTurnCapturesContentWhenOn(t *testing.T) {
	captureContent(t)
	rec := recordSpans(t)
	log, buf := bufferLog(slog.LevelDebug)
	a, _ := observedAgent(t, log)
	askOverSocket(t, a, log, secretQuestion)

	st := spanTree{spans: rec.Ended()}
	st.find(t, "meru.turn",
		attribute.String("meru.question", secretQuestion),
		attribute.String("meru.answer", secretAnswer))
	out := buf.String()
	if !strings.Contains(out, `question="`+secretQuestion+`"`) || !strings.Contains(out, `answer="`+secretAnswer+`"`) {
		t.Errorf("debug log lacks the question and answer previews:\n%s", out)
	}
}

func TestDebugLogFollowsEachStage(t *testing.T) {
	recordSpans(t) // gives each turn a trace ID
	log, buf := bufferLog(slog.LevelDebug)
	a, cfg := observedAgent(t, log)
	session := askOverSocket(t, a, log, secretQuestion, "and what about zebracorn foals?")
	out := buf.String()

	for _, want := range []string{
		`msg="rpc request" op=ask source=tui session="" question_chars=28`,
		`msg="turn started"`,
		`msg="session created"`,
		`msg="session opened" session=` + session,
		`msg="history loaded" session=` + session + ` turns=1 messages=2`,
		`msg="transcript appended" type=user`,
		`msg=route route=direct confidence=`,
		`probs="direct=`,
		`msg="prompt built" messages=4`,
		`msg="answer finished" model=`,
		`msg="transcript appended" type=assistant`,
		`msg=turn session=` + session,
		`msg="rpc done sent"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s", want)
		}
	}

	// Every line of a turn carries its trace ID, and the trace ID matches
	// the one on the transcript lines.
	lines := readLines(t, cfg, session)
	for line := range strings.Lines(out) {
		if !strings.Contains(line, "trace_id=") {
			t.Errorf("line has no trace_id: %s", line)
		}
	}
	for _, l := range lines {
		if !strings.Contains(out, "trace_id="+l.TraceID) || l.TraceID == "" {
			t.Errorf("transcript trace ID %q isn't in the log", l.TraceID)
		}
	}
	if strings.Contains(out, "zebracorn") || strings.Contains(out, "quokka") {
		t.Errorf("debug log holds question or answer text with capture_content off:\n%s", out)
	}
}

func TestInfoLogIsOneLinePerTurn(t *testing.T) {
	recordSpans(t)
	tests := []struct {
		name    string
		failing bool
	}{
		{"ok", false},
		{"failed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := bufferLog(slog.LevelInfo)
			a, _ := observedAgent(t, log)
			if tt.failing {
				a.engine.(*fakeEngine).streamErr = context.DeadlineExceeded
			}
			_, _ = run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: secretQuestion})

			out := strings.TrimSpace(buf.String())
			if n := strings.Count(out, "\n") + 1; n != 1 {
				t.Fatalf("info log has %d lines, want 1:\n%s", n, out)
			}
			for _, want := range []string{"level=INFO msg=turn ", "ttft_ms=", "tokens_out=", "trace_id="} {
				if !strings.Contains(out, want) {
					t.Errorf("turn line lacks %s: %s", want, out)
				}
			}
			if got := strings.Contains(out, "err="); got != tt.failing {
				t.Errorf("turn line has err = %v, want %v: %s", got, tt.failing, out)
			}
			if strings.Contains(out, "zebracorn") {
				t.Errorf("turn line holds the question: %s", out)
			}
		})
	}
}
