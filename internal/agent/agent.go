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

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// defaultTurnTimeout is how long a turn may run when config sets no
// turn_timeout. It matches the default in internal/config.
const defaultTurnTimeout = 5 * time.Minute

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
// sets. Without it, a small model read "did I visit Lisbon?" as a
// question about Meru and answered that Meru had no record of a visit,
// while the excerpts in front of it named the user as the traveller. The
// user's files are about the user, so "I" in a question points at them.
const whoIsWho = "The person asking is the user, and the files are theirs. " +
	"In a question, \"I\", \"me\" and \"my\" mean the user, never you. " +
	"When an excerpt names a person, that is often the user."

// filesNote joins the system prompt on every turn and tells the model which
// folders Meru searches. Without it a small model answers "I don't have
// access to your files" even while it reads excerpts from them, and can't say
// what it has indexed. With agentic retrieval, Meru searches nothing up
// front, so the note leaves out the search.
//
// The note stays the same on every turn, so Ollama can reuse its work on
// it. What the model may do with the file tools goes in a note of its own,
// on file turns only; see fileToolsNoteFor.
func filesNote(folders []string, agentic bool) string {
	if len(folders) == 0 {
		return "Meru hasn't indexed any of the user's files yet. " +
			"To search their files, the user lists folders under [index] folders in ~/.meru/config.toml."
	}
	if agentic {
		return "Meru indexes the user's files in these folders: " + strings.Join(folders, ", ") + "."
	}
	return "Meru indexes and searches the user's files in these folders: " + strings.Join(folders, ", ") + ". " +
		"When a question needs them, Meru searches first and puts the best excerpts below."
}

// noResults stands in for the excerpts when a search finds nothing, or when
// nothing is indexed yet, so the model answers without pretending it looked.
const noResults = "A search of the user's files found nothing relevant to this question. " +
	"Answer from what you know, and don't cite any files."

// noResultsWeb takes noResults' place on a turn that offers web_search.
// "Answer from what you know" would contradict webFallbackNote, which
// tells the model to search the web when the files come up empty, and a
// small model follows the nearer, plainer line. This one points it at
// web_search for the facts a model most often gets wrong from memory.
const noResultsWeb = "A search of the user's files found nothing relevant to this question, so don't cite any files. " +
	"For how to use a program or command, or for anything that may have changed, call web_search; " +
	"otherwise answer from what you know."

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
	search      Searcher      // nil turns search off
	tools       ToolRunner    // nil turns tools off
	turns       TurnRecorder  // nil keeps no turn rows
	profile     Profile       // nil leaves the profile out of the prompt
	skills      Skills        // nil turns skills off; set by UseSkills
	machine     string        // describes the user's computer; set by UseMachine
	maxRounds   int           // model calls per turn, at most; see converse
	maxTokens   int           // tokens one main-model call may write, thinking included
	turnTimeout time.Duration // how long one turn may run; see Handle
	agentic     bool          // [index] retrieval = "agentic": no search before the answer
	models      config.Models
	folderNames []string // last part of each [index] folder, lower case; see namesFolder
	// folders, when set by UseFolders, returns the [index] folders as they
	// are now, and a turn builds folderNames and filesNote from it.
	folders     func() []string
	historyN    int          // earlier turns to put in the prompt
	system      string       // system prompt, with whoIsWho and honestyRule; the profile follows it
	outputDir   string       // [skills] output_dir as the model reads it, such as ~/meru-output
	filesNote   string       // filesNote for the [index] folders; follows the profile
	sessionsDir string       // where transcripts live, usually ~/.meru/sessions
	home        string       // the home folder, for showing paths as ~/...; "" if unknown
	log         *slog.Logger // merud's logger; lines carry the turn's trace ID
	// readImage and vision, set by UseImages, read a question's images
	// and say whether a model can look at them; see images.go.
	readImage func(path string) ([]byte, error)
	vision    func(ctx context.Context, model string) (bool, error)
}

// New returns an Agent that answers with eng, routes with router, and keeps
// transcripts under cfg.Dir/sessions. It searches the user's files with
// search on every route but "direct", and on a direct question that names
// one of cfg.Index.Folders. search may be nil, which turns search off. It
// offers the model the tools from tools on the "tools" and "search+tools"
// routes, and only the file tools on "search", for at most
// cfg.Agent.MaxRounds model calls per turn. tools may be
// nil, which turns tools off. With cfg.Index.Retrieval "agentic" it
// searches nothing before the answer and leaves the model to explore with
// the file tools; see searchesFirst. It writes a row for each answered turn to
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
	system += "\n\n" + whoIsWho + "\n\n" + honestyRule
	agentic := cfg.Index.Retrieval == config.RetrievalAgentic
	// config.Load has checked turn_timeout already. A zero Config, as some
	// tests build, has none, so it falls back to the default.
	timeout, err := time.ParseDuration(cfg.Agent.TurnTimeout)
	if err != nil || timeout <= 0 {
		timeout = defaultTurnTimeout
	}
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
		maxTokens:   cfg.Agent.MaxOutputTokens,
		turnTimeout: timeout,
		agentic:     agentic,
		models:      cfg.Models,
		folderNames: folderNames(cfg.Index.Folders),
		historyN:    cfg.Agent.HistoryTurns,
		system:      system,
		outputDir:   displayDir(home, cfg.Skills.OutputDir),
		filesNote:   filesNote(cfg.Index.Folders, agentic),
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
// as each call ends, then a new "sources" event when its calls returned
// excerpts (see runTools). It has the rpc.Handler signature, so merud passes
// a.Handle straight to rpc.Serve. The server holds the "done" back until
// Handle returns, and sends "error" in its place if Handle fails.
//
// While tool calls run, Handle calls emit from several goroutines at once,
// and dispatch calls approve from them too. The rpc server's emit takes a
// lock for each write, so that is safe.
//
// A turn never runs forever. It has [agent] turn_timeout to answer, and
// each model call may write at most [agent] max_output_tokens. A turn that
// runs out of time, hits the token cap, or ends its rounds with no text
// still answers: see endTurn. The user reads a short apology, or the text
// so far with a note that it was cut off, instead of silence.
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
	scope, err := scopeOf(req.Scope)
	if err != nil {
		return err
	}
	imagePaths, images, err := a.loadImages(req)
	if err != nil {
		return err
	}

	ctx, span := obs.Tracer().Start(ctx, "meru.turn")
	var route, sessionID string
	var rep reply // the answer's stats, summed over the rounds
	// ended is how a turn without a full answer ended: endTimeout,
	// endCutOff or endGaveUp; "" for a full answer.
	var ended string
	// unbacked is true when the answer claims an action no tool took.
	var unbacked bool
	// t carries what the rounds need; its rounds field counts model calls.
	t := &turn{emit: emit, approve: approve, scope: scope, images: images}
	defer func() {
		outcome := outcomeOf(ctx, err)
		if err == nil && ended != "" {
			outcome = ended
		}
		span.SetAttributes(
			attribute.String("meru.route", route),
			attribute.String("meru.source", source),
			attribute.String("meru.scope", scope),
			attribute.String("meru.session.id", sessionID),
			attribute.Int("meru.turn.iterations", t.rounds),
			attribute.Int("meru.turn.repeated_calls", t.repeats),
			attribute.Bool("meru.turn.empty_retry", t.emptyRetry),
			attribute.Bool("meru.turn.output_retry", t.outputRetry),
			attribute.Bool("meru.turn.unbacked_claim", unbacked),
			attribute.Int("meru.turn.images", len(images)),
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
		a.logTurn(ctx, start, sessionID, route, source, scope, outcome, unbacked, len(images), rep, err)
	}()
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("meru.question", question))
	}
	a.logStart(ctx, req.Session, source, question)
	if len(images) > 0 {
		// Counts and sizes only: the bytes never reach a log or a span.
		a.log.DebugContext(ctx, "images attached", "images", len(images), "bytes", imageBytes(images))
	}

	// The turn's deadline. tctx ends when turn_timeout runs out, and with it
	// every model call and tool call the turn makes: Ollama sees its request
	// cancelled and stops. ctx itself only ends when the client hangs up, so
	// comparing the two tells a turn that ran out of time from one the user
	// cancelled. cancel frees the timer when Handle returns.
	tctx, cancel := context.WithTimeout(ctx, a.turnTimeout)
	defer cancel()

	sess, history, err := a.openSession(tctx, req.Session)
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
	t.question = userWords(question, history)
	// The user line carries start as its time, so a turns row rebuilt from
	// the transcript gets the same time as the row written live. It names
	// the question's images by path and never holds their bytes.
	if err := a.appendLine(ctx, sess, transcript.Line{TS: start, Type: transcript.TypeUser, Text: question, Images: imagePaths, TraceID: traceID}); err != nil {
		return err
	}

	res, err := a.respond(tctx, t, question, history)
	route, rep = res.route, res.rep
	// badOutput is true when the rounds ended on output Ollama couldn't
	// read, even after the retry. The turn answers with badOutputAnswer
	// instead of failing with Ollama's raw error.
	badOutput := errors.Is(err, engine.ErrModelOutput) && ctx.Err() == nil
	// noVision is true when the question carried images the main model
	// can't look at. The turn answers with noVisionAnswer.
	noVision := errors.Is(err, errNoVision)
	if err != nil && !badOutput && !noVision && (tctx.Err() == nil || ctx.Err() != nil) {
		// A failure that isn't the turn's own deadline, unreadable model
		// output or a model without vision: Ollama down or refusing the
		// request, or the user hung up.
		return err
	}
	ended = endOf(err, rep)
	if ended != "" {
		// err is only ever the deadline, unreadable output or no vision
		// here, which endTurn answers for.
		var text string
		text, err = a.endTurn(ctx, t, ended, rep.text)
		if err != nil {
			return err
		}
		rep.text = text
	}
	if obs.CaptureContent() {
		span.SetAttributes(attribute.String("meru.answer", rep.text))
	}
	// A full answer from a turn in which no tool call succeeded can't have
	// changed anything, so a claim that it did is false. The "notice"
	// event warns the user under the answer, and the assistant line keeps
	// the warning. See honest.go. A turn that ended without a full answer
	// (ended != "") gets no check: its text ends in Meru's own message,
	// such as badOutputAnswer, and claims nothing.
	var notice string
	if ended == "" && t.succeeded == 0 && claimsAction(rep.text) {
		notice = unbackedNotice
		unbacked = true
		if err := emit(rpc.Event{Type: rpc.EventNotice, Text: notice}); err != nil {
			return err
		}
	}

	// The assistant line holds the turn's facts, so `meru usage` can
	// rebuild from the files: the route after the override rules, how long
	// the turn took, and which files it read. Outcome is set only on a turn
	// that ended without a full answer.
	answer := transcript.Line{
		Type:      transcript.TypeAssistant,
		Text:      rep.text,
		TokensIn:  rep.usage.PromptTokens,
		TokensOut: rep.usage.OutputTokens,
		Route:     route,
		Ms:        time.Since(start).Milliseconds(),
		Sources:   res.docs,
		Outcome:   ended,
		Notice:    notice,
		TraceID:   traceID,
	}
	if err := a.appendLine(ctx, sess, answer); err != nil {
		return err
	}
	a.recordUsage(ctx, sessionID, source, start, answer, t.calls)
	return emit(doneEvent(start, rep))
}

// response is what respond hands back to Handle: the route after the
// override rules, the answer with its stats, and the full paths of the
// files whose excerpts went into the prompt, for the transcript.
type response struct {
	route string
	rep   reply
	docs  []string
}

// respond does the part of a turn between the question and the answer:
// route and pick skills, apply the route rules, search, build the prompt,
// and run the rounds. It sends the "route" event and any "sources" event
// itself.
//
// It fails when a model call fails, emit fails, or ctx ends. Even then it
// returns the route so far and the text the last round streamed before it
// stopped, so Handle can close a turn that ran out of time with what the
// user already read.
//
// A question with images first checks that the main model can look at
// them (see checkVision), and then skips the router, whose fast model
// reads text alone: it takes the route its scope gives, and "direct" in
// auto (see respondScoped).
func (a *Agent) respond(ctx context.Context, t *turn, question string, history []engine.Message) (response, error) {
	var res response
	if len(t.images) > 0 {
		if err := a.checkVision(ctx, t); err != nil {
			res.route = "direct"
			return res, err
		}
	}
	if t.scope != rpc.ScopeAuto || len(t.images) > 0 {
		return a.respondScoped(ctx, t, question, history)
	}
	dec, picked, err := a.routeAndPick(ctx, question, history)
	if err != nil {
		return res, err
	}
	// The router can't always tell that a question names one of the user's
	// own projects: "what database does meru use" can look like general
	// knowledge. When a direct question names an indexed folder, search
	// anyway. A wrong guess costs one search of about 50 ms.
	if dec.Route == "direct" && a.search != nil && namesFolder(question, a.currentFolderNames()) {
		a.log.DebugContext(ctx, "route changed to search: the question names an indexed folder",
			"confidence", dec.Confidence)
		dec.Route = "search"
	}
	// The same gap for tools: "search my obsidian vault" can route to
	// search, which offers only the file tools. When the question points at
	// a connected tool (toolTarget in toolnouns.go lists the four signs) and
	// the route lacks the full set of tools, add them. A wrong guess costs
	// a prompt that holds the tool schemas, and the model need not call any.
	var target string
	if a.tools != nil {
		target = toolTarget(question, a.tools.Tools())
	}
	if r, ok := withTools(dec.Route); ok && target != "" {
		a.log.DebugContext(ctx, "route changed: the question "+target,
			"from", dec.Route, "to", r, "confidence", dec.Confidence)
		dec.Route = r
	}
	// fileTurn says whether the turn is about the user's files; see
	// aboutFiles.
	fileTurn := aboutFiles(dec.Route, target)
	// The last rule: a picked skill brings the tools it names, when config
	// allows them, and a skill whose tools are all off stays out of the
	// prompt. See skillTools.
	var skillTools []string
	picked.names, skillTools = a.skillTools(ctx, picked)
	if missing := notOffered(skillTools, a.toolSpecs(dec.Route)); len(missing) > 0 {
		if r, ok := withTools(dec.Route); ok {
			a.log.DebugContext(ctx, "route changed: a picked skill uses tools the route lacks",
				"skills", strings.Join(picked.names, ","), "tools", strings.Join(missing, ","),
				"from", dec.Route, "to", r, "confidence", dec.Confidence)
			// A skill that adds file tools makes the turn about the user's
			// files, as the route it widens to would. One that adds only
			// other tools, such as web-research, leaves the turn as it was:
			// a direct question about btop needs no excerpts from the files.
			if slices.ContainsFunc(missing, builtin.IsFileTool) {
				fileTurn = aboutFiles(r, target)
			}
			dec.Route = r
		}
	}
	res.route = dec.Route
	if dec.Route == "tools" && !fileTurn {
		a.log.DebugContext(ctx, "no search first: the question points at a connected tool", "target", target)
	}
	// Any outcome but "ok" means the router wasn't sure and used the
	// fallback route; the chat screen marks such a route.
	routeEv := rpc.Event{Type: rpc.EventRoute, Route: dec.Route, Confidence: dec.Confidence, Fallback: dec.Outcome != "ok", Skills: skillInfos(picked.names)}
	if err := t.emit(routeEv); err != nil {
		return res, err
	}

	// A file turn looks in the user's files first. "tools" searches too,
	// because the router sends some questions about the user's files
	// there, and an answer from the files beats one from the model alone.
	// A "tools" turn whose question points at a connected tool doesn't:
	// excerpts from the files only crowd a web or mail answer, and cost
	// time. With agentic retrieval no turn searches first: the model looks
	// with the file tools instead.
	// specs is the tools the turn offers. The search below needs to know
	// whether web_search is among them; the list may grow below, when a
	// tool server reconnects, but web_search is built in and doesn't wait
	// on one.
	specs := a.toolSpecs(dec.Route)
	var files string
	if a.searchesFirst(fileTurn) {
		var sources []rpc.Citation
		files, sources, res.docs, err = a.searchFiles(ctx, searchQuery(question, history), offersWebSearch(specs))
		if err != nil {
			return res, err
		}
		if len(sources) > 0 {
			if err := t.emit(rpc.Event{Type: rpc.EventSources, Sources: sources}); err != nil {
				return res, err
			}
		}
		// Excerpts a tool finds later in the turn number on from these.
		t.sources, t.cites = sources, len(sources)
		// Past sessions join the files' section: no numbers, no sources event.
		files = joinSections(files, a.earlierSection(ctx, searchQuery(question, history), t.sess.ID()))
	}
	// A turn on a tools route first refreshes the tool servers, then lists
	// the tools again. Refresh asks each connected server for its tools, so
	// a server that restarted with new tools offers them now, and gives each
	// server that isn't connected one try, so one the user started after
	// merud, or one that crashed, is back for this turn. This is the only
	// place merud reconnects or re-lists, so nothing runs while nobody asks
	// (ARCHITECTURE.md, "MCP"). A server that still fails is left out.
	// The search route offers only the file tools and commands, never a
	// server's tools, so it doesn't wait on a server that may take 30
	// seconds to start.
	if len(specs) > 0 && (dec.Route == "tools" || dec.Route == "search+tools") {
		a.tools.Refresh(ctx)
		specs = a.toolSpecs(dec.Route)
	}
	res.rep, err = a.finishPrompt(ctx, t, question, history, picked, files, specs, fileTurn)
	return res, err
}

// finishPrompt does the end of a turn that respond and respondScoped
// share: recall the memories that fit the question and tell the client
// which ones, build the prompt, put the question's images on it, and run
// the rounds.
func (a *Agent) finishPrompt(ctx context.Context, t *turn, question string, history []engine.Message,
	picked pickedSkills, files string, specs []engine.ToolSpec, fileTurn bool) (reply, error) {
	memories, recalled := a.memorySection(ctx, searchQuery(question, history))
	if len(recalled) > 0 {
		if err := t.emit(rpc.Event{Type: rpc.EventMemories, Memories: recalled}); err != nil {
			return reply{}, err
		}
	}
	skillList, skillBodies := a.skillsSection(ctx, picked)
	msgs := a.prompt(ctx, history, question, sections{
		memories: memories, skillList: skillList, skillBodies: skillBodies,
		files: files, toolsNote: noteFor(specs), fileTools: a.fileToolsNoteFor(specs, fileTurn),
	})
	// The images ride on this turn's question alone; see withImages.
	msgs = withImages(msgs, t.images)
	if len(specs) > 0 {
		obs.RecordContextTokens(ctx, "tools", schemaChars(specs)/4)
	}
	return a.converse(ctx, t, msgs, specs)
}

// How a turn can end without a full answer. Each is a value of the turn's
// outcome, beside "ok", "error" and "cancelled", in the span, the "turn"
// log line and the meru.turn.duration metric, and sits in the assistant
// line's outcome field in the transcript.
const (
	endTimeout = "timeout" // [agent] turn_timeout ran out
	endCutOff  = "cut_off" // the last model call hit [agent] max_output_tokens
	endGaveUp  = "gave_up" // the rounds ended with no text, such as only tool calls
	// endBadOutput: Ollama couldn't read what the model wrote, even after
	// the one retry (see retriesOutput).
	endBadOutput = "bad_output"
)

// sorry is the answer to a turn that ended with no text at all.
const sorry = "Sorry, I couldn't answer that. Try asking again, or rephrase the question."

// The answers to a turn that ended endBadOutput: badOutputAnswer after the
// retry failed too, badOutputOnce when the turn had no round or time left
// to retry. Ollama's own error text goes to the log, not here: "XML syntax
// error on line 8" tells the user nothing they can act on.
const (
	badOutputAnswer = "The model wrote a tool call that Ollama couldn't read, twice. " +
		"Try asking again, or rephrase the question."
	badOutputOnce = "The model wrote a tool call that Ollama couldn't read. " +
		"Try asking again, or rephrase the question."
)

// The notes that follow an answer cut off part way, by how it ended.
const (
	timeoutNote = "[Meru stopped the answer here: the question took longer than its time limit.]"
	cutOffNote  = "[Meru stopped the answer here: it reached the length limit.]"
)

// endOf says how a turn ended: endNoVision when err is errNoVision,
// endBadOutput when err wraps engine.ErrModelOutput, endTimeout for any
// other err (Handle passes only those three kinds here), endCutOff when the
// last model call stopped at the token cap, endGaveUp when the answer holds
// no text, and "" for a full answer.
func endOf(err error, rep reply) string {
	switch {
	case errors.Is(err, errNoVision):
		return endNoVision
	case errors.Is(err, engine.ErrModelOutput):
		return endBadOutput
	case err != nil:
		return endTimeout
	case rep.doneReason == "length":
		return endCutOff
	case strings.TrimSpace(rep.text) == "":
		return endGaveUp
	}
	return ""
}

// endTurn closes a turn that ended without a full answer and returns the
// answer text for the transcript. text is what the last round streamed.
//
// A turn that ended endNoVision gets noVisionAnswer, and nothing else:
// no model ran. A turn that ended endBadOutput always gets badOutputAnswer, or
// badOutputOnce when it didn't retry, after a blank line when the round
// streamed some text first. Otherwise, when the user has read no text yet,
// endTurn sends sorry as the answer; when some text streamed first, that
// text stays, and a note follows it saying the answer was cut off:
// timeoutNote after a deadline, cutOffNote otherwise. Either way the words
// go out as "token" events through t.emit, so both clients show them as the
// answer with no change. It logs why the turn ended at debug level, and
// fails only when emit does.
func (a *Agent) endTurn(ctx context.Context, t *turn, ended, text string) (string, error) {
	a.log.DebugContext(ctx, "turn ended without a full answer", "outcome", ended,
		"answer_chars", utf8.RuneCountInString(text))
	streamed := strings.TrimSpace(text) != ""
	var add string
	switch {
	case ended == endNoVision:
		add = noVisionAnswer(a.models.Main)
	case ended == endBadOutput:
		add = badOutputOnce
		if t.outputRetry {
			add = badOutputAnswer
		}
		if streamed {
			add = "\n\n" + add
		}
	case !streamed:
		add = sorry
	case ended == endTimeout:
		add = "\n\n" + timeoutNote
	default:
		add = "\n\n" + cutOffNote
	}
	if err := t.emit(rpc.Event{Type: rpc.EventToken, Text: add}); err != nil {
		return "", err
	}
	return text + add, nil
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

// logTurn writes the one info line each turn gets. scope is where the
// user let the turn look, one of the rpc.Scope constants. images, the
// count of images the question carried, joins the line when above zero.
// ttft_ms counts
// from when Handle started to the first token of the answer, so it
// includes routing; it is 0 when no text arrived. unbacked_claim joins the
// line, set to true, only when the answer claimed an action no tool took
// (see claimsAction). err joins the line only when the turn failed.
func (a *Agent) logTurn(ctx context.Context, start time.Time, sessionID, route, source, scope, outcome string, unbacked bool, images int, rep reply, err error) {
	var ttft int64
	if !rep.firstToken.IsZero() {
		ttft = rep.firstToken.Sub(start).Milliseconds()
	}
	args := []any{"session", sessionID, "route", route, "source", source, "scope", scope,
		"outcome", outcome, "ms", time.Since(start).Milliseconds(), "ttft_ms", ttft,
		"tokens_in", rep.usage.PromptTokens, "tokens_out", rep.usage.OutputTokens}
	if unbacked {
		args = append(args, "unbacked_claim", true)
	}
	if images > 0 {
		args = append(args, "images", images)
	}
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
// the model made, the runtime's usage counters, when the first piece of
// text arrived (the zero time.Time when none did), and why the model
// stopped: "stop", or "length" when it hit the token cap.
type reply struct {
	text       string
	calls      []engine.ToolCall
	usage      engine.Usage
	firstToken time.Time
	doneReason string
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

// aboutFiles reports whether a turn on route is about the user's files.
// target is what toolTarget found in the question, "" for nothing.
//
// "search" and "search+tools" are always about files: the router, or the
// rule for a question that names an indexed folder, saw files in the
// question. "tools" is about files unless the question points at a
// connected tool: the router sends some file questions there, but "search
// the web for the latest Go release" or "what's on my calendar?" needs no
// excerpts. "direct" never is.
//
// One rule decides two things (ARCHITECTURE.md, "Retrieval"): a file turn
// searches first in "auto" mode, and a file turn that offers the file
// tools gets the note on using them. A turn that isn't about files still
// offers the file tools, so the model can look when it has to.
func aboutFiles(route, target string) bool {
	switch route {
	case "search", "search+tools":
		return true
	case "tools":
		return target == ""
	}
	return false
}

// searchesFirst reports whether a turn searches the user's files before
// the model answers: a file turn (see aboutFiles), when the agent has a
// Searcher and [index] retrieval is "auto". With "agentic" no turn does,
// and earlier conversations stay out of the prompt too, since they come
// from the same search step.
func (a *Agent) searchesFirst(fileTurn bool) bool {
	return fileTurn && a.search != nil && !a.agentic
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
// agents behind specs: "obsidian" for "obsidian.obsidian_simple_search" and
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
// own. "and the watering?" has one and "how long was the stay?" two, so both
// borrow the earlier question; "i did some work on the bakery site, remind
// me" has four (work, bakery, site, remind) and doesn't. Two would be too few:
// "how long was the stay" would lose which stay.
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
// short section saying so and no citations: noResultsWeb when web is true,
// which means the turn offers web_search, and noResults otherwise. A search that fails for any
// reason but a cancelled turn is logged and treated the same way: the
// answer can still come from the model alone. It returns an error only when
// ctx ends.
func (a *Agent) searchFiles(ctx context.Context, query string, web bool) (string, []rpc.Citation, []string, error) {
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
		results[i].Path = rpc.ShortPath(a.home, results[i].Path)
	}
	section := noResults
	if web {
		section = noResultsWeb
	}
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

// displayDir returns dir, a folder from config such as "~/meru-output", as
// the model should read it: as config wrote it when it starts with "~",
// and otherwise shortened by rpc.ShortPath. It returns "" for "".
func displayDir(home, dir string) string {
	if dir == "" || strings.HasPrefix(dir, "~") {
		return dir
	}
	return rpc.ShortPath(home, filepath.Clean(dir))
}

// canDo returns canDoNote for every tool config allows, or for none when
// tools are off.
func (a *Agent) canDo() string {
	var all []engine.ToolSpec
	if a.tools != nil {
		all = a.tools.Tools()
	}
	return canDoNote(all, a.outputDir)
}

// prompt builds the messages for the main model inside a meru.prompt span,
// and reports their size. The system prompt holds, in order (budget.go
// explains why):
//
//   - the parts that stay the same from turn to turn: the configured prompt
//     with whoIsWho and honestyRule, the user's profile, filesNote, the
//     line on what Meru can do (canDoNote), the tools note on a turn that
//     offers tools, and the list of skills;
//   - the note on the file tools, on a file turn that offers them. It
//     changes only with the kind of turn, so it comes after the parts every
//     turn shares and before the parts each question changes;
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
	add(today(time.Now()))
	add(a.machine)
	profile := a.profileSection(ctx)
	add(profile)
	add(a.currentFilesNote())
	add(a.canDo())
	add(sec.toolsNote)
	add(sec.skillList)
	add(sec.fileTools)
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
	// context added, and the text streamed so far: the user has read it,
	// so a turn that ran out of time can keep it.
	fail := func(err error) (reply, error) {
		obs.EndSpanErr(ctx, span, err)
		return reply{text: text.String(), firstToken: firstToken}, fmt.Errorf("main model %s: %w", model, err)
	}

	// MaxTokens becomes Ollama's num_predict, which counts every token the
	// model writes, its hidden thinking included. It stops a thinking model
	// that would reason for minutes and never answer.
	stream, err := a.engine.Stream(ctx, msgs, tools, engine.Options{Model: model, MaxTokens: a.maxTokens})
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
	return reply{text: text.String(), calls: calls, usage: usage, firstToken: firstToken, doneReason: doneReason}, nil
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
	case rpc.SourceCLI, rpc.SourceTUI, rpc.SourceJob, rpc.SourceDesktop:
		return string(s), nil
	default:
		return "", fmt.Errorf("unknown source %q", s)
	}
}

// scopeOf checks the request's scope and returns it. An empty scope means
// rpc.ScopeAuto. Any other value is refused, because the scope goes in the
// turn's log line and span, which must hold a small fixed set.
func scopeOf(s string) (string, error) {
	if s == "" {
		return rpc.ScopeAuto, nil
	}
	if slices.Contains(rpc.Scopes(), s) {
		return s, nil
	}
	return "", fmt.Errorf("unknown scope %q; use one of %s", s, strings.Join(rpc.Scopes(), ", "))
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
