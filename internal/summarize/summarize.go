// This file holds the Summarizer: the loop that runs once a minute, the
// choice of which sessions to summarize, the text the fast model reads, the
// model call, and the embedding of new summaries.

package summarize

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// Every is how often the Summarizer looks for quiet sessions.
const Every = time.Minute

// maxPerTick caps the summaries one pass writes. The first run after an
// upgrade finds every old transcript without a summary; at five a minute,
// 100 old sessions take 20 minutes. Each pass keeps the fast model busy
// for about a second on the lite profile (5 summaries at about 200 ms), so
// a question asked meanwhile waits that long at most. The newest sessions
// go first: a person most often asks about them.
const maxPerTick = 5

// maxEmbedPerTick caps the summaries one pass embeds, in one call to the
// embedding model. Embedding is fast; the cap matters after a change of
// embedding model, when every summary needs a new vector.
const maxEmbedPerTick = 32

// The limits on what the model reads and writes.
const (
	// maxInputChars caps the conversation text the model reads, about 1,000
	// tokens, which the lite model reads in well under a second.
	maxInputChars = 4000
	// maxLineChars caps any one question or answer inside that text, so one
	// long answer can't fill it.
	maxLineChars = 800
	// maxTokens caps the summary the model writes; two sentences need far
	// fewer. maxSummaryChars cuts what comes back, in case it rambles.
	maxTokens       = 150
	maxSummaryChars = 600
)

// instructions is the system prompt of the summary call. The summary is
// what later recall searches, so it asks for the names, numbers and
// decisions a later question would name. It fixes how the summary names the
// person: left free, the 2B model wrote "you" in one summary and guessed
// "her" and a misspelt name in another.
const instructions = "You summarize a conversation between a user and Meru, their assistant. " +
	"Write one or two sentences in the past tense that say what the user wanted " +
	"and what was decided, found or done. Keep names, numbers and dates. " +
	"Call the person \"the user\" every time, never \"you\", \"he\" or \"she\". " +
	"Write plain text with no preamble, list or heading."

// Summarizer writes and embeds session summaries. Build it with New and run
// it with Run.
type Summarizer struct {
	st    *store.Store
	eng   engine.Engine
	model string        // the fast model, from config
	idle  time.Duration // [agent] summary_idle
	dir   string        // the sessions folder, usually ~/.meru/sessions
	log   *slog.Logger
	// now returns the current time. Tests replace it to move the clock.
	now func() time.Time
}

// New returns a Summarizer that reads and appends to the transcripts in
// sessionsDir, keeps st up to date, and calls model on eng, the fast model
// from config. A session counts as ended once it has gone idle without a
// question or an answer. log may be nil, which means no log lines.
func New(st *store.Store, eng engine.Engine, model string, idle time.Duration, sessionsDir string, log *slog.Logger) *Summarizer {
	if log == nil {
		log = obs.Discard()
	}
	return &Summarizer{st: st, eng: eng, model: model, idle: idle, dir: sessionsDir, log: log, now: time.Now}
}

// Run makes a pass at once, then one every minute, until ctx is cancelled.
// It returns nothing: each pass logs its own errors, and the next pass
// tries again.
func (s *Summarizer) Run(ctx context.Context) {
	// A Ticker sends the time on its channel C once per interval. Stop
	// frees it when Run returns.
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		s.Tick(ctx)
		// select waits for whichever channel is ready first: ctx.Done()
		// closes when merud stops, and t.C delivers the next tick.
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick makes one pass: it summarizes up to maxPerTick sessions that have
// been quiet for the idle time, then embeds up to maxEmbedPerTick
// summaries that lack a vector. It returns how many summaries it wrote. A
// session that fails is logged and skipped; the next pass tries it again.
func (s *Summarizer) Tick(ctx context.Context) int {
	ids, err := s.st.DueSummaries(ctx, s.now().Add(-s.idle), maxPerTick)
	if err != nil {
		s.log.WarnContext(ctx, "list sessions due a summary", "err", err)
		return 0
	}
	written := 0
	for _, id := range ids {
		err := s.summarize(ctx, id)
		if ctx.Err() != nil {
			return written // merud is stopping; that isn't worth a warning
		}
		if err != nil {
			s.log.WarnContext(ctx, "session not summarized; next pass tries again", "session", id, "err", err)
			continue
		}
		written++
	}
	s.embed(ctx)
	return written
}

// summarize writes one session's summary inside a meru.summarize span: it
// reads the transcript, asks the fast model, appends the summary line and
// replays the file into the store. It logs the summary's length and time at
// debug level, never its text. It fails when the file can't be read or
// written, the model call fails, or the replay fails.
//
// err is a named result so the deferred function can mark the span with it.
func (s *Summarizer) summarize(ctx context.Context, id string) (err error) {
	// Each summary starts its own trace: it belongs to no turn.
	ctx, span := obs.Tracer().Start(ctx, "meru.summarize")
	defer func() {
		obs.EndSpanErr(ctx, span, err)
		span.End()
	}()
	span.SetAttributes(attribute.String("meru.session.id", id))
	start := time.Now()

	sess, err := transcript.Open(s.dir, id)
	if err != nil {
		return err
	}
	lines, err := transcript.ReadLines(sess.Path())
	if err != nil {
		return err
	}
	input := Input(lines, maxInputChars)
	if input == "" {
		return fmt.Errorf("session %s has no question or answer to summarize", id)
	}
	text, err := s.ask(ctx, input)
	if err != nil {
		return err
	}
	if text == "" {
		// A model that writes nothing would leave the session due forever,
		// costing a model call every minute. Its first question says
		// something about it, so that stands in.
		text = clip(firstQuestion(lines), maxSummaryChars)
	}
	if err := sess.Append(transcript.Line{TS: s.now(), Type: transcript.TypeSummary, Text: text}); err != nil {
		return err
	}
	if _, err := s.st.ReplaySession(ctx, s.dir, id); err != nil {
		return err
	}

	chars := utf8.RuneCountInString(text)
	span.SetAttributes(
		attribute.Int("meru.summarize.input_chars", utf8.RuneCountInString(input)),
		attribute.Int("meru.summarize.chars", chars),
	)
	s.log.DebugContext(ctx, "session summarized", "session", id, "chars", chars,
		"ms", time.Since(start).Milliseconds())
	return nil
}

// ask sends input to the fast model with thinking off, inside a
// gen_ai.chat span, and returns its summary with the whitespace tidied and
// cut to maxSummaryChars. Thinking would cost seconds and add nothing to
// two sentences.
func (s *Summarizer) ask(ctx context.Context, input string) (string, error) {
	ctx, span := obs.StartChat(ctx, obs.Chat{Tier: "fast", Model: s.model, MaxTokens: maxTokens})
	defer span.End()
	start := time.Now()
	msgs := []engine.Message{
		{Role: engine.RoleSystem, Content: instructions},
		{Role: engine.RoleUser, Content: "The conversation:\n\n" + input},
	}
	comp, err := s.eng.Generate(ctx, msgs, nil, engine.Options{Model: s.model, MaxTokens: maxTokens, NoThink: true})
	if err != nil {
		obs.EndSpanErr(ctx, span, err)
		return "", fmt.Errorf("fast model %s: %w", s.model, err)
	}
	u := obs.Usage{
		PromptTokens: comp.Usage.PromptTokens, OutputTokens: comp.Usage.OutputTokens,
		LoadDuration: comp.Usage.LoadDuration, PromptEvalDuration: comp.Usage.PromptEvalDuration,
		EvalDuration: comp.Usage.EvalDuration,
	}
	obs.ChatResult(span, u, comp.DoneReason)
	obs.RecordModelCall(ctx, obs.ModelCall{
		Tier: "fast", Model: s.model, Operation: "chat", Duration: time.Since(start), Usage: u,
	})
	return clip(tidy(comp.Text), maxSummaryChars), nil
}

// embed gives a vector to up to maxEmbedPerTick summaries that lack one,
// in one call to the embedding model. A failure only logs a warning: the
// summaries stay listed, and the next pass tries again.
func (s *Summarizer) embed(ctx context.Context) {
	todo, err := s.st.SummariesWithoutVector(ctx, maxEmbedPerTick)
	if err != nil {
		s.log.WarnContext(ctx, "list summaries without a vector", "err", err)
		return
	}
	if len(todo) == 0 {
		return
	}
	texts := make([]string, len(todo))
	for i, t := range todo {
		texts[i] = t.Summary
	}
	vecs, err := s.eng.Embed(ctx, texts)
	if ctx.Err() != nil {
		return
	}
	if err != nil || len(vecs) != len(texts) {
		s.log.WarnContext(ctx, "embed session summaries", "summaries", len(texts), "vectors", len(vecs), "err", err)
		return
	}
	for i, t := range todo {
		if err := s.st.SetSessionVector(ctx, t.Session, t.Summary, vecs[i]); err != nil {
			s.log.WarnContext(ctx, "store a summary vector", "session", t.Session, "err", err)
		}
	}
	s.log.DebugContext(ctx, "summaries embedded", "summaries", len(todo))
}

// Input returns the conversation text the model summarizes: each question
// as "User: …" and each answer as "Meru: …", oldest first, with each one
// cut to maxLineChars. It keeps the whole within limit characters by
// taking the newest lines first, and always keeps the first question,
// which usually names what the session is about. "[…]" marks the lines it
// left out. It returns "" when lines hold no question or answer.
func Input(lines []transcript.Line, limit int) string {
	var parts []string
	for _, l := range lines {
		text := clip(tidy(l.Text), maxLineChars)
		if text == "" {
			continue
		}
		switch l.Type {
		case transcript.TypeUser:
			parts = append(parts, "User: "+text)
		case transcript.TypeAssistant:
			parts = append(parts, "Meru: "+text)
		}
	}
	if len(parts) == 0 {
		return ""
	}

	// Keep the first line, then walk back from the newest while the lines
	// fit. +1 counts the line break before each line.
	used := utf8.RuneCountInString(parts[0])
	from := len(parts)
	for from > 1 {
		n := utf8.RuneCountInString(parts[from-1]) + 1
		if used+n > limit {
			break
		}
		used += n
		from--
	}
	out := []string{parts[0]}
	if from > 1 {
		out = append(out, "[…]")
	}
	out = append(out, parts[from:]...)
	return strings.Join(out, "\n")
}

// firstQuestion returns the text of the first user line, or "".
func firstQuestion(lines []transcript.Line) string {
	for _, l := range lines {
		if l.Type == transcript.TypeUser {
			if text := tidy(l.Text); text != "" {
				return text
			}
		}
	}
	return ""
}

// tidy turns every run of spaces and line breaks in text into one space
// and trims the ends, so a summary or a quoted line reads as one line.
func tidy(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// clip cuts text to at most limit characters, ending in "…" when it cut.
// It counts runes, Go's name for Unicode characters, so it never splits
// one.
func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	r := []rune(text)
	return strings.TrimSpace(string(r[:limit-1])) + "…"
}
