// This file holds the Agent and its one entry point, Handle, which runs a
// whole turn for one rpc request: session, route, prompt, streamed answer,
// transcript lines, spans and metrics. The tool rounds live in tools.go.

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// DefaultSystemPrompt is the system prompt when config sets none.
const DefaultSystemPrompt = "You are Meru, a personal assistant that runs entirely on the user's own computer. " +
	"Answer clearly and briefly. If you don't know something, say so."

// citeRule joins the system prompt on turns that search the user's files.
// Small models invent sources when they aren't told not to, so the rule
// says it plainly.
const citeRule = "Below, under \"From your files\", are numbered excerpts from the user's own files. " +
	"When they help answer the question, answer from them and cite each excerpt you use by its number " +
	"in square brackets, like [1]. Cite only the numbers listed there. " +
	"Never invent a file, a quote or a citation. If the excerpts don't answer the question, say so."

// whoIsWho joins the system prompt on every turn, whatever prompt config
// sets. Without it, a small model read "did I visit Amsterdam?" as a
// question about Meru and answered that Meru had no record of a visit,
// while the excerpts in front of it named the user as the traveller. The
// user's files are about the user, so "I" in a question points at them.
const whoIsWho = "The person asking is the user, and the files are theirs. " +
	"In a question, \"I\", \"me\" and \"my\" mean the user, never you. " +
	"When an excerpt names a person, that is often the user."

// filesNote joins the system prompt on every turn and tells the model which
// folders Meru searches. Without it a small model answers "I don't have
// access to your files" even while it reads excerpts from them, and can't say
// what it has indexed.
func filesNote(folders []string) string {
	if len(folders) == 0 {
		return "Meru hasn't indexed any of the user's files yet. " +
			"To search their files, the user lists folders under [index] folders in ~/.meru/config.toml."
	}
	return "Meru indexes and searches the user's files in these folders: " + strings.Join(folders, ", ") + ". " +
		"When a question needs them, Meru searches first and puts the best excerpts below. " +
		"You can't open or list files yourself."
}

// noResults stands in for the excerpts when a search finds nothing, or when
// nothing is indexed yet, so the model answers without pretending it looked.
const noResults = "A search of the user's files found nothing relevant to this question. " +
	"Answer from what you know, and don't cite any files."

// Searcher finds the excerpts from the user's files that best answer query,
// best first. merud passes an adapter around retrieve.Search; tests pass a
// fake. It fails when the embedding or the store fails.
//
// SearchSessions finds the n past sessions that best match query, best
// first, leaving out the session excludeSession; merud's adapter calls
// retrieve.SearchSessions. See earlier.go.
type Searcher interface {
	Search(ctx context.Context, query string) ([]retrieve.Result, error)
	SearchSessions(ctx context.Context, query, excludeSession string, n int) ([]retrieve.SessionResult, error)
}

// Router picks a route for one turn. The agent defines the interface with
// only the method it calls, so this package doesn't depend on the router's
// own types; merud passes in a small adapter around internal/router.
type Router interface {
	// Decide returns the route for question, given the session's history. It
	// returns an error only when the model call fails; an unsure model gives
	// the fallback route with a non-"ok" outcome instead.
	Decide(ctx context.Context, question string, history []engine.Message) (Decision, error)
}

// TurnRecorder keeps one row per answered turn for `meru usage`. merud
// passes *store.Store; tests pass a fake or nil. Every field but the source
// also sits in the transcript, so the store can rebuild the rows.
type TurnRecorder interface {
	InsertTurn(ctx context.Context, t store.Turn) error
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
	search      Searcher     // nil turns search off
	tools       ToolRunner   // nil turns tools off
	turns       TurnRecorder // nil keeps no turn rows
	profile     Profile      // nil leaves the profile out of the prompt
	skills      Skills       // nil turns skills off; set by UseSkills
	maxRounds   int          // model calls per turn, at most; see converse
	models      config.Models
	folderNames []string     // last part of each [index] folder, lower case; see namesFolder
	historyN    int          // earlier turns to put in the prompt
	system      string       // system prompt, with whoIsWho; the profile follows it
	filesNote   string       // filesNote for the [index] folders; follows the profile
	sessionsDir string       // where transcripts live, usually ~/.meru/sessions
	home        string       // the home folder, for showing paths as ~/...; "" if unknown
	log         *slog.Logger // merud's logger; lines carry the turn's trace ID
}

// New returns an Agent that answers with eng, routes with router, and keeps
// transcripts under cfg.Dir/sessions. It searches the user's files with
// search on every route but "direct", and on a direct question that names
// one of cfg.Index.Folders. search may be nil, which turns search off. It
// offers the model the tools from tools on the "tools" and "search+tools"
// routes, for at most cfg.Agent.MaxRounds model calls per turn. tools may be
// nil, which turns tools off. It writes a row for each answered turn to
// turns, which may be nil to keep none. It puts the user's profile from
// profile into every prompt; a nil profile leaves it out. log may be nil,
// which means no log lines.
func New(cfg config.Config, eng engine.Engine, router Router, search Searcher, tools ToolRunner, turns TurnRecorder, profile Profile, log *slog.Logger) *Agent {
	if log == nil {
		log = obs.Discard()
	}
	// Without a home folder, paths show in full; that is only cosmetic.
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	system := cfg.Agent.SystemPrompt
	if system == "" {
		system = DefaultSystemPrompt
	}
	system += "\n\n" + whoIsWho
	// &Agent{...} builds the struct and returns a pointer to it, so every
	// caller shares one Agent instead of copying it.
	return &Agent{
		engine:      eng,
		router:      router,
		search:      search,
		tools:       tools,
		turns:       turns,
		profile:     profile,
		maxRounds:   cfg.Agent.MaxRounds,
		models:      cfg.Models,
		folderNames: folderNames(cfg.Index.Folders),
		historyN:    cfg.Agent.HistoryTurns,
		system:      system,
		filesNote:   filesNote(cfg.Index.Folders),
		sessionsDir: filepath.Join(cfg.Dir, "sessions"),
		home:        home,
		log:         log,
	}
}

// Handle runs one turn for req and sends its events through emit, in this
// order: "session", "route", "sources" when the turn searched the user's
// files and found something, then the rounds, and last a "done" that
// carries the turn's stats. Each round sends one "token" per piece of text;
// a round that calls tools adds a "tool_call" per call and a "tool_result"
// as each call ends. It has the rpc.Handler signature, so merud passes
// a.Handle straight to rpc.Serve. The server holds the "done" back until
// Handle returns, and sends "error" in its place if Handle fails.
//
// While tool calls run, Handle calls emit from several goroutines at once,
// and dispatch calls approve from them too. The rpc server's emit takes a
// lock for each write, so that is safe.
//
// A turn that answers also writes a row to the turns table and records
// the usage metrics; see recordUsage.
//
// Handle fails when the request is bad (empty question, unknown session or
// source), when the transcript can't be written, when a model call fails, or
// when ctx is cancelled. A cancelled turn writes no assistant line.
//
// Each stage runs in its own span under one meru.turn span, and at debug
// level logs a line tagged with the turn's trace ID. The info log gets one
// "turn" line when the turn ends. No span or log line carries the question
// or answer text unless capture_content is on.
//
// err is a named result, so the deferred function below can read the final
// error and record the turn's outcome whichever return statement ran.
func (a *Agent) Handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) (err error) {
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
	var rep reply // the answer's stats, summed over the rounds
	// t carries what the rounds need; its rounds field counts model calls.
	t := &turn{emit: emit, approve: approve}
	defer func() {
		outcome := outcomeOf(ctx, err)
		span.SetAttributes(
			attribute.String("meru.route", route),
			attribute.String("meru.source", source),
			attribute.String("meru.session.id", sessionID),
			attribute.Int("meru.turn.iterations", t.rounds),
			attribute.String("meru.turn.outcome", outcome),
		)
		obs.EndSpanErr(ctx, span, err)
		span.End()
		// The turn's ctx may be cancelled by now. WithoutCancel keeps its
		// values (the trace) but drops the cancel, so the metric still records.
		obs.RecordTurn(context.WithoutCancel(ctx), obs.Turn{
			Route: route, Source: source, Outcome: outcome,
			Duration: time.Since(start), Iterations: t.rounds,
		})
		a.logTurn(ctx, start, sessionID, route, source, outcome, rep, err)
	}()
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("meru.question", question))
	}
	a.logStart(ctx, req.Session, source, question)

	sess, history, err := a.openSession(ctx, req.Session)
	if err != nil {
		return err
	}
	sessionID = sess.ID()
	if req.Session == "" {
		obs.RecordSession(ctx, source)
	}
	if err := emit(rpc.Event{Type: rpc.EventSession, Session: sessionID}); err != nil {
		return err
	}

	traceID := traceIDOf(span)
	t.sess, t.source, t.traceID = sess, rpc.Source(source), traceID
	// The user line carries start as its time, so a turns row rebuilt from
	// the transcript gets the same time as the row written live.
	if err := a.appendLine(ctx, sess, transcript.Line{TS: start, Type: transcript.TypeUser, Text: question, TraceID: traceID}); err != nil {
		return err
	}

	dec, picked, err := a.routeAndPick(ctx, question, history)
	if err != nil {
		return err
	}
	// The router can't always tell that a question names one of the user's
	// own projects: "what database does meru use" can look like general
	// knowledge. When a direct question names an indexed folder, search
	// anyway. A wrong guess costs one search of about 50 ms.
	if dec.Route == "direct" && a.search != nil && namesFolder(question, a.folderNames) {
		a.log.DebugContext(ctx, "route changed to search: the question names an indexed folder",
			"confidence", dec.Confidence)
		dec.Route = "search"
	}
	// The same gap for tools: "search my obsidian vault" can route to
	// search, which offers no tools. When a question names a connected tool
	// server and the route has no tools, add them.
	if r, ok := withTools(dec.Route); ok && a.tools != nil && namesFolder(question, toolServers(a.tools.Tools())) {
		a.log.DebugContext(ctx, "route changed: the question names a tool server",
			"from", dec.Route, "to", r, "confidence", dec.Confidence)
		dec.Route = r
	}
	// And for memory: the router can send "remember that my name is Amit"
	// to direct, and a direct turn offers no remember tool, so the model
	// would say it will remember and save nothing. When the question holds
	// "remember" as a whole word and the route has no tools, add them. A
	// wrong guess ("do you remember the budget?") costs a prompt that holds
	// the tool schemas, and the model need not call any.
	if r, ok := withTools(dec.Route); ok && a.tools != nil && asksToRemember(question, a.tools.Tools()) {
		a.log.DebugContext(ctx, "route changed: the question asks Meru to remember",
			"from", dec.Route, "to", r, "confidence", dec.Confidence)
		dec.Route = r
	}
	route = dec.Route
	// Any outcome but "ok" means the router wasn't sure and used the
	// fallback route; the chat screen marks such a route.
	routeEv := rpc.Event{Type: rpc.EventRoute, Route: dec.Route, Confidence: dec.Confidence, Fallback: dec.Outcome != "ok", Skills: skillInfos(picked.names)}
	if err := emit(routeEv); err != nil {
		return err
	}

	// Every route but "direct" looks in the user's files first. "tools"
	// searches too, because the router sends some questions about the
	// user's files there, and an answer from the files beats one from the
	// model alone.
	var files string
	var docs []string // the full paths of the files in the prompt, for the transcript
	if searches(dec.Route) && a.search != nil {
		var sources []rpc.Citation
		files, sources, docs, err = a.searchFiles(ctx, searchQuery(question, history))
		if err != nil {
			return err
		}
		if len(sources) > 0 {
			if err := emit(rpc.Event{Type: rpc.EventSources, Sources: sources}); err != nil {
				return err
			}
		}
		// Past sessions join the files' section: no numbers, no sources event.
		files = joinSections(files, a.earlierSection(ctx, searchQuery(question, history), sessionID))
	}
	specs := a.toolSpecs(dec.Route)
	memories := a.memorySection(ctx, searchQuery(question, history))
	skillList, skillBodies := a.skillsSection(ctx, picked)
	msgs := a.prompt(ctx, history, question, sections{
		memories: memories, skillList: skillList, skillBodies: skillBodies,
		files: files, tools: len(specs) > 0,
	})
	if len(specs) > 0 {
		obs.RecordContextTokens(ctx, "tools", schemaChars(specs)/4)
	}
	rep, err = a.converse(ctx, t, msgs, specs)
	if err != nil {
		return err
	}
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("meru.answer", rep.text))
	}

	// The assistant line holds the turn's facts, so `meru usage` can
	// rebuild from the files: the route after the override rules above,
	// how long the turn took, and which files it read.
	answer := transcript.Line{
		Type:      transcript.TypeAssistant,
		Text:      rep.text,
		TokensIn:  rep.usage.PromptTokens,
		TokensOut: rep.usage.OutputTokens,
		Route:     route,
		Ms:        time.Since(start).Milliseconds(),
		Sources:   docs,
		TraceID:   traceID,
	}
	if err := a.appendLine(ctx, sess, answer); err != nil {
		return err
	}
	a.recordUsage(ctx, sessionID, source, start, answer, t.calls)
	return emit(doneEvent(start, rep))
}

// recordUsage writes the turns row for an answered turn and records
// meru.turn.tokens and meru.turn.docs. answer is the assistant line just
// written, and calls the number of tool calls the turn made.
//
// A failed insert only logs a warning: the transcript already holds the
// turn, and the next rebuild of meru.db brings the row back. The insert
// runs even when the client hung up after the answer: WithoutCancel keeps
// ctx's values (the trace) and drops its cancel.
func (a *Agent) recordUsage(ctx context.Context, sessionID, source string, start time.Time, answer transcript.Line, calls int) {
	obs.RecordTurnUsage(ctx, obs.TurnUsage{
		Route: answer.Route, Source: source,
		TokensIn: answer.TokensIn, TokensOut: answer.TokensOut, Docs: len(answer.Sources),
	})
	if a.turns == nil {
		return
	}
	err := a.turns.InsertTurn(context.WithoutCancel(ctx), store.Turn{
		Session: sessionID, Time: start, Source: source, Route: answer.Route,
		TokensIn: int64(answer.TokensIn), TokensOut: int64(answer.TokensOut),
		DurationMillis: answer.Ms, ToolCalls: calls, Docs: answer.Sources, TraceID: answer.TraceID,
	})
	if err != nil {
		a.log.WarnContext(ctx, "turn row not written; the transcript still has the turn", "err", err)
	}
}

// logStart writes the debug line that opens a turn: which session it asks
// to continue (empty for a new one), where it came from, and how long the
// question is. The question's first 200 characters join the line only when
// capture_content is on.
func (a *Agent) logStart(ctx context.Context, session, source, question string) {
	args := []any{"session", session, "source", source, "question_chars", utf8.RuneCountInString(question)}
	if obs.CaptureContent() {
		args = append(args, "question", obs.Preview(question))
	}
	a.log.DebugContext(ctx, "turn started", args...)
}

// logTurn writes the one info line each turn gets. ttft_ms counts from when
// Handle started to the first token of the answer, so it includes routing;
// it is 0 when no text arrived. err joins the line only when the turn failed.
func (a *Agent) logTurn(ctx context.Context, start time.Time, sessionID, route, source, outcome string, rep reply, err error) {
	var ttft int64
	if !rep.firstToken.IsZero() {
		ttft = rep.firstToken.Sub(start).Milliseconds()
	}
	args := []any{"session", sessionID, "route", route, "source", source,
		"outcome", outcome, "ms", time.Since(start).Milliseconds(), "ttft_ms", ttft,
		"tokens_in", rep.usage.PromptTokens, "tokens_out", rep.usage.OutputTokens}
	if err != nil {
		args = append(args, "err", err)
	}
	// Only the error, never the question or answer, goes in this line.
	a.log.InfoContext(ctx, "turn", args...)
}

// doneEvent builds the "done" event that ends a turn, with the turn's stats.
// Both times count from start, when merud received the question, so they
// match what the person waiting at the terminal sees. A turn whose answer
// was empty has no first token, and its TTFTMillis stays zero.
func doneEvent(start time.Time, rep reply) rpc.Event {
	ev := rpc.Event{
		Type:           rpc.EventDone,
		DurationMillis: time.Since(start).Milliseconds(),
		TokensIn:       rep.usage.PromptTokens,
		TokensOut:      rep.usage.OutputTokens,
		EvalMillis:     rep.usage.EvalDuration.Milliseconds(),
	}
	if !rep.firstToken.IsZero() {
		ev.TTFTMillis = rep.firstToken.Sub(start).Milliseconds()
	}
	return ev
}

// reply is what answer hands back: the whole answer text, the tool calls
// the model made, the runtime's usage counters, and when the first piece of
// text arrived (the zero time.Time when none did).
type reply struct {
	text       string
	calls      []engine.ToolCall
	usage      engine.Usage
	firstToken time.Time
}

// openSession opens the session named id, or starts a new one when id is
// empty, and reads its history, inside a meru.session span. It returns the
// session and up to historyN earlier turns as model messages.
//
// It reads the history before the turn writes its question, so the
// question isn't in it twice.
func (a *Agent) openSession(ctx context.Context, id string) (*transcript.Session, []engine.Message, error) {
	ctx, span := obs.Tracer().Start(ctx, "meru.session")
	defer span.End()
	start := time.Now()

	var sess *transcript.Session
	var err error
	msg := "session opened"
	if id == "" {
		msg = "session created"
		sess, err = transcript.New(a.sessionsDir)
	} else {
		sess, err = transcript.Open(a.sessionsDir, id)
	}
	if err != nil {
		obs.EndSpanErr(ctx, span, err)
		return nil, nil, err
	}
	span.SetAttributes(
		attribute.String("meru.session.id", sess.ID()),
		attribute.Bool("meru.session.new", id == ""),
	)
	a.log.DebugContext(ctx, msg, "session", sess.ID(), "ms", time.Since(start).Milliseconds())

	start = time.Now()
	history, err := sess.History(a.historyN)
	if err != nil {
		obs.EndSpanErr(ctx, span, err)
		return nil, nil, err
	}
	// History holds a user and an assistant message per turn.
	turns := len(history) / 2
	span.SetAttributes(
		attribute.Int("meru.history.turns", turns),
		attribute.Int("meru.history.messages", len(history)),
	)
	a.log.DebugContext(ctx, "history loaded", "session", sess.ID(), "turns", turns,
		"messages", len(history), "ms", time.Since(start).Milliseconds())
	return sess, history, nil
}

// appendLine writes l to the session's transcript inside a
// meru.transcript.append span. The span and the log line name the line's
// type and length, never its text.
func (a *Agent) appendLine(ctx context.Context, sess *transcript.Session, l transcript.Line) error {
	ctx, span := obs.Tracer().Start(ctx, "meru.transcript.append", trace.WithAttributes(
		attribute.String("meru.transcript.type", l.Type),
		attribute.String("meru.session.id", sess.ID()),
	))
	defer span.End()
	start := time.Now()
	if err := sess.Append(l); err != nil {
		obs.EndSpanErr(ctx, span, err)
		return err
	}
	a.log.DebugContext(ctx, "transcript appended", "type", l.Type,
		"chars", utf8.RuneCountInString(l.Text), "ms", time.Since(start).Milliseconds())
	return nil
}

// route asks the router for this turn's route. The router records the
// meru.route span, its debug line and the meru.route.decisions metric
// itself, so the agent records none of them.
func (a *Agent) route(ctx context.Context, question string, history []engine.Message) (Decision, error) {
	dec, err := a.router.Decide(ctx, question, history)
	if err != nil {
		return Decision{}, fmt.Errorf("route: %w", err)
	}
	return dec, nil
}

// searches reports whether a route looks in the user's files: every route
// but "direct"; see Handle.
func searches(route string) bool {
	return route != "direct"
}

// folderNames returns the last part of each folder, in lower case, such as
// "meru" for "~/repos/meru". Names under three letters are left out, because
// they match too many ordinary words.
func folderNames(folders []string) []string {
	var names []string
	for _, f := range folders {
		n := strings.ToLower(filepath.Base(filepath.FromSlash(f)))
		if utf8.RuneCountInString(n) >= 3 && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// withTools returns the route that adds tools to route: "tools" for
// "direct" and "search+tools" for "search". ok is false for a route that
// has tools already.
func withTools(route string) (string, bool) {
	switch route {
	case "direct":
		return "tools", true
	case "search":
		return "search+tools", true
	}
	return "", false
}

// toolServers returns, in lower case, the names of the MCP servers and A2A
// agents behind specs: "obsidian" for "obsidian.search_vault" and
// "research" for "a2a.research.summarize". Built-in tools belong to no
// server; their owner, "meru", is also the assistant's name, so it would
// match nearly every question addressed to it.
func toolServers(specs []engine.ToolSpec) []string {
	var names []string
	for _, t := range specs {
		name := strings.ToLower(t.Name)
		var server string
		switch toolKind(name) {
		case dispatch.KindA2A:
			server, _, _ = strings.Cut(strings.TrimPrefix(name, "a2a."), ".")
		case dispatch.KindMCP:
			server, _, _ = strings.Cut(name, ".")
		default:
			continue
		}
		if !slices.Contains(names, server) {
			names = append(names, server)
		}
	}
	return names
}

// namesFolder reports whether question holds one of names as a whole word,
// ignoring case: "meru's" and "Meru" match "meru", "merudaemon" doesn't.
func namesFolder(question string, names []string) bool {
	for _, w := range words(question) {
		if slices.Contains(names, w) {
			return true
		}
	}
	return false
}

// words splits text into lower-case words at every rune that isn't a letter,
// a digit or a hyphen, so "personal-knowledge-base" stays one word.
func words(text string) []string {
	// FieldsFunc splits text at every rune for which the function returns
	// true.
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

// standaloneWords is how many subject words make a question stand on its
// own. "and the budget?" has one and "how much did it cost?" two, so both
// borrow the earlier question; "i did some work on the bakery site, remind
// me" has four (work, bakery, site, remind) and doesn't. Two would be too few:
// "how much did it cost" would lose what "it" is.
const standaloneWords = 3

// searchQuery is the text a turn searches for. No model rewrites the query.
// A question with at least standaloneWords subject words is searched on
// its own. A shorter one is a follow-up: it gets the session's latest
// earlier question that names a subject, because "and the one after that?"
// means nothing to a search on its own. The current question comes first:
// keyword search keeps only a query's first words.
//
// The standalone test came from a real miss: a question about a work project,
// asked after one about a trip, searched for both, and the travel papers
// crowded out every note on the project.
//
// An earlier question made only of filler, such as "try the last question
// again", names no subject, so the walk skips it and keeps going back.
// Without the skip, "search again" after "try again" searched for those
// words alone and found nothing on the subject.
func searchQuery(question string, history []engine.Message) string {
	if subjectWords(question) >= standaloneWords {
		return question
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == engine.RoleUser && namesSubject(history[i].Content) {
			return question + "\n" + history[i].Content
		}
	}
	return question
}

// namesSubject reports whether text holds at least one word that isn't
// filler, so it can steer a search.
func namesSubject(text string) bool {
	return subjectWords(text) > 0
}

// subjectWords counts the distinct words in text that aren't filler.
func subjectWords(text string) int {
	var seen []string
	for _, w := range words(text) {
		if !isFiller(w) && !slices.Contains(seen, w) {
			seen = append(seen, w)
		}
	}
	return len(seen)
}

// isFiller reports whether w is a word that says nothing about a subject:
// short common words, and the words people use to ask for a retry or a
// search. The list is short on purpose; a word missing from it only means
// an earlier question joins the query when it could have been skipped.
func isFiller(w string) bool {
	switch w {
	case "a", "an", "the", "and", "or", "but", "so", "is", "are", "was", "were", "be",
		"it", "its", "this", "that", "there", "here", "i", "im", "me", "my", "you", "your",
		"we", "do", "does", "did", "can", "could", "would", "will", "please", "ok", "okay",
		"to", "in", "on", "of", "for", "at", "about", "again", "try", "retry", "search",
		"look", "check", "find", "last", "previous", "question", "answer", "now", "think",
		"sure", "time", "once", "more", "docs", "files", "notes", "what", "how", "why",
		"which", "where", "when", "who", "say", "says", "said", "tell", "specified", "mentioned",
		// Words that point back at something said before, and the pieces
		// an apostrophe leaves: "what's" splits into "what" and "s".
		"one", "ones", "other", "else", "after", "before", "next", "first", "then",
		"they", "them", "those", "these", "some", "much", "many", "with", "from", "as",
		"s", "t", "d", "ll", "re", "ve", "m":
		return true
	}
	return false
}

// searchFiles searches the user's files for query inside a meru.search
// span. It returns the prompt section to add under the system prompt, the
// citations for the "sources" event, numbered as the section numbers them,
// and the cited files' absolute paths, each once, for the transcript.
//
// A search that finds nothing, or runs before anything is indexed, gives a
// short section saying so and no citations. A search that fails for any
// reason but a cancelled turn is logged and treated the same way: the
// answer can still come from the model alone. It returns an error only when
// ctx ends.
func (a *Agent) searchFiles(ctx context.Context, query string) (string, []rpc.Citation, []string, error) {
	ctx, span := obs.Tracer().Start(ctx, "meru.search")
	defer span.End()
	start := time.Now()

	results, err := a.search.Search(ctx, query)
	if err != nil {
		if ctx.Err() != nil {
			obs.EndSpanErr(ctx, span, err)
			return "", nil, nil, fmt.Errorf("search: %w", err)
		}
		// The turn goes on without excerpts, so this is a warning, not the
		// turn's error.
		a.log.WarnContext(ctx, "search failed; answering without your files", "err", err)
		obs.EndSpanErr(ctx, span, err)
		results = nil
	}

	// Keep each file's full path for the transcript before shortening it:
	// the turns table must name a file the same way whoever reads it.
	var docs []string
	for _, r := range results {
		if !slices.Contains(docs, r.Path) {
			docs = append(docs, r.Path)
		}
	}
	// Show each path as ~/... to the model and to the client: it is shorter,
	// and the model has no use for the full path.
	for i := range results {
		results[i].Path = shortPath(a.home, results[i].Path)
	}
	section := noResults
	if len(results) > 0 {
		section = citeRule + "\n\nFrom your files\n\n" + retrieve.Format(results)
	}
	obs.RecordContextTokens(ctx, "chunks", utf8.RuneCountInString(section)/4)

	sources := make([]rpc.Citation, len(results))
	for i, r := range results {
		sources[i] = rpc.Citation{
			N: i + 1, Path: r.Path, Heading: r.Heading,
			StartLine: r.StartLine, EndLine: r.EndLine, Page: r.Page, Score: r.Score,
		}
	}
	span.SetAttributes(attribute.Int("meru.search.results", len(results)))
	a.log.DebugContext(ctx, "search done", "results", len(results),
		"chars", utf8.RuneCountInString(section), "ms", time.Since(start).Milliseconds())
	return section, sources, docs, nil
}

// shortPath writes p under the home folder as ~/..., using the OS's path
// separator. Any other path, or any path when home is "", stays as it is.
func shortPath(home, p string) string {
	if home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return "~" + string(filepath.Separator) + rel
}

// prompt builds the messages for the main model inside a meru.prompt span,
// and reports their size. The system prompt holds, in order (budget.go
// explains why):
//
//   - the parts that stay the same from turn to turn: the configured prompt
//     with whoIsWho, the user's profile, filesNote, toolsNote on a turn that
//     offers tools, and the list of skills;
//   - the parts each question changes: the recalled memories, the picked
//     skills' instructions, and the excerpts from the user's files with any
//     earlier conversations.
//
// The profile sits right after whoIsWho, so the rule that "I" means the user
// and the facts about who the user is read together. The recalled memories
// stay apart from the numbered excerpts, so the model never cites a memory
// as a file.
//
// The history is cut to maxHistoryChars, oldest turns first. est_tokens is
// characters divided by four, a rough rule for English text; the model's
// own count arrives with its answer.
func (a *Agent) prompt(ctx context.Context, history []engine.Message, question string, sec sections) []engine.Message {
	ctx, span := obs.Tracer().Start(ctx, "meru.prompt")
	defer span.End()
	// add appends one section, leaving out an empty one.
	system := a.system
	add := func(part string) {
		if part != "" {
			system += "\n\n" + part
		}
	}
	profile := a.profileSection(ctx)
	add(profile)
	add(a.filesNote)
	if sec.tools {
		add(toolsNote)
	}
	add(sec.skillList)
	add(sec.memories)
	add(sec.skillBodies)
	add(sec.files)
	recordMemoryTokens(ctx, profile, sec.memories)

	history, dropped := trimHistory(history, maxHistoryChars)
	if dropped > 0 {
		a.log.DebugContext(ctx, "history cut to its budget", "dropped_messages", dropped, "cap_chars", maxHistoryChars)
	}
	historyChars := 0
	for _, m := range history {
		historyChars += utf8.RuneCountInString(m.Content)
	}
	obs.RecordContextTokens(ctx, "history", historyChars/4)

	msgs := buildMessages(system, history, question)
	chars := 0
	for _, m := range msgs {
		chars += utf8.RuneCountInString(m.Content)
	}
	span.SetAttributes(
		attribute.Int("meru.prompt.messages", len(msgs)),
		attribute.Int("meru.prompt.chars", chars),
		attribute.Int("meru.prompt.est_tokens", chars/4),
	)
	a.log.DebugContext(ctx, "prompt built", "messages", len(msgs), "chars", chars, "est_tokens", chars/4)
	return msgs
}

// answer streams the main model's reply to msgs, offering it tools (nil for
// none), sends each piece of text through emit as a "token" event, and
// returns the whole text, the tool calls, the runtime's usage counters and
// the first token's arrival time. It records one gen_ai.chat span, with a
// first_token event, and the model-call metrics. Each round of a turn calls
// it once.
func (a *Agent) answer(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, emit func(rpc.Event) error) (reply, error) {
	model := a.models.Main
	ctx, span := obs.StartChat(ctx, obs.Chat{Tier: "main", Model: model, Stream: true})
	defer span.End()

	start := time.Now()
	var ttft time.Duration   // time to first token; zero until text arrives
	var firstToken time.Time // when that token arrived
	var usage engine.Usage
	var doneReason string
	var text strings.Builder
	var calls []engine.ToolCall

	// fail marks the span as failed or cancelled and returns err with
	// context added.
	fail := func(err error) (reply, error) {
		obs.EndSpanErr(ctx, span, err)
		return reply{}, fmt.Errorf("main model %s: %w", model, err)
	}

	stream, err := a.engine.Stream(ctx, msgs, tools, engine.Options{Model: model})
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
				firstToken = time.Now()
				ttft = firstToken.Sub(start)
				span.AddEvent("first_token", trace.WithAttributes(attribute.Int64("meru.ttft_ms", ttft.Milliseconds())))
			}
			text.WriteString(delta.Text)
			if err := emit(rpc.Event{Type: rpc.EventToken, Text: delta.Text}); err != nil {
				return fail(err)
			}
		}
		// Ollama sends each tool call whole, in a chunk of its own, so
		// collecting them needs no joining of pieces.
		calls = append(calls, delta.ToolCalls...)
		if delta.Done {
			usage = delta.Usage
			doneReason = delta.DoneReason
		}
	}
	// A stream can end early without an error when ctx is cancelled.
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	ou := obs.Usage{
		PromptTokens: usage.PromptTokens, OutputTokens: usage.OutputTokens,
		LoadDuration: usage.LoadDuration, PromptEvalDuration: usage.PromptEvalDuration,
		EvalDuration: usage.EvalDuration,
	}
	obs.ChatResult(span, ou, doneReason)
	obs.RecordModelCall(ctx, obs.ModelCall{
		Tier: "main", Model: model, Operation: "chat",
		Duration: time.Since(start), TimeToFirstToken: ttft, Usage: ou,
	})
	args := []any{"model", model, "ms", time.Since(start).Milliseconds(),
		"ttft_ms", ttft.Milliseconds(), "tokens_in", usage.PromptTokens,
		"tokens_out", usage.OutputTokens, "answer_chars", utf8.RuneCountInString(text.String()),
		"tool_calls", len(calls)}
	if obs.CaptureContent() {
		args = append(args, "answer", obs.Preview(text.String()))
	}
	a.log.DebugContext(ctx, "answer finished", args...)
	return reply{text: text.String(), calls: calls, usage: usage, firstToken: firstToken}, nil
}

// buildMessages puts the prompt together: the system prompt, the session's
// earlier turns, then the new question. The excerpts from the user's files,
// when a turn has them, sit at the end of the system prompt: some models'
// chat templates accept a system message only in first place.
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
