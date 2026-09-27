// This file holds the tool rounds of a turn: which routes offer tools, the
// loop that calls the main model until it stops calling tools, and the step
// that runs one round's calls through dispatch at the same time.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// toolsNote joins the system prompt on turns that offer tools. It tells the
// model that it may call them, and that the user may say no, so a declined
// call doesn't surprise it.
const toolsNote = "You may call the tools offered with this question when they help you answer. " +
	"Some calls ask the user first, and the user may say no. " +
	"When a tool asks for the user's email address, use the one in what you know about the user; don't search for it."

// fileToolsNote joins the system prompt on a file turn that offers the
// file tools, in "auto" mode (see fileToolsNoteFor). It tells the model
// when to reach for them: when the excerpts from search don't hold enough.
const fileToolsNote = "When the excerpts below aren't enough, you may read whole files with read_file, " +
	"list folders with list_folder, find every matching line with grep, and search again in other words with search_files."

// exploreNote takes fileToolsNote's place when [index] retrieval is
// "agentic". No excerpts sit in the prompt, so it tells the model to look
// for itself, in the order that finds things fastest, and carries the rule
// on citing that citeRule carries in "auto" mode. The two-or-three-rounds
// limit keeps a small model from searching until it runs out of rounds.
const exploreNote = "To answer from the user's files, look in them first: call search_files, which finds passages " +
	"by meaning and by words, or grep for an exact name or phrase; use list_folder to see what a folder holds. " +
	"Then read_file the files that matter, and answer. Stop after two or three rounds of tool calls. " +
	"search_files numbers each excerpt: cite the ones you use by their number in square brackets, like [1]. " +
	"Never invent a file, a quote or a citation. If what you find doesn't answer the question, say so."

// commandsNote joins the system prompt on a "search" turn that offers
// local commands. It names them by their prefix, so the model knows the
// cmd. tools are there to run.
const commandsNote = "You may also run the cmd. tools offered with this question. " +
	"Each runs one program the user declared and returns what it printed."

// webFallbackNote follows toolsNote on a turn that offers both web_search
// and a file tool. A real turn showed why. Asked "help me understand btop
// with some simple commands", a small model grepped the user's folders,
// found only pages that name btop in passing, never called web_search,
// and answered with flags btop doesn't have. The note tells it to go to
// the web when the files come up empty, and not to invent flags or
// versions.
const webFallbackNote = "When the user's files don't answer the question, because a search, grep or read " +
	"found nothing on it, call web_search before you answer from memory. " +
	"Never make up a command's flags or options, or a version number: look them up, or say you don't know."

// noteFor returns the tools note for a turn that offers specs: toolsNote
// when any is an MCP tool, an A2A skill or a built-in other than the file
// tools (datetime included); otherwise commandsNote when any is a command;
// and "" for none. The file tools get a note of their own; see
// fileToolsNoteFor.
//
// When specs hold web_search and a file tool, webFallbackNote follows
// toolsNote. It depends only on which tools the turn offers, not on the
// question, so it stays the same from one such turn to the next and can
// sit with the other parts Ollama reuses (see budget.go). web_fetch alone
// doesn't bring the note: with no search, the model would have to guess a
// URL, and web_fetch asks the user before it fetches a URL that no search
// result gave.
func noteFor(specs []engine.ToolSpec) string {
	var cmds, other, files, web bool
	for _, s := range specs {
		switch {
		case builtin.IsFileTool(s.Name):
			files = true
		case toolKind(s.Name) == dispatch.KindCommand:
			cmds = true
		default:
			other = true
			if s.Name == builtin.WebSearch {
				web = true
			}
		}
	}
	// A switch with no value after the keyword runs the first case whose
	// test is true, so the web line wins over the plain toolsNote.
	switch {
	case web && files:
		return toolsNote + " " + webFallbackNote
	case other:
		return toolsNote
	case cmds:
		return commandsNote
	}
	return ""
}

// offersWebSearch reports whether specs hold the web_search tool.
func offersWebSearch(specs []engine.ToolSpec) bool {
	return slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return s.Name == builtin.WebSearch })
}

// fileToolsNoteFor returns the note on the file tools for a turn that
// offers specs: fileToolsNote, or exploreNote with agentic retrieval, when
// fileTurn is true and specs hold a file tool; "" otherwise. fileTurn comes
// from aboutFiles.
//
// A turn that isn't about files, such as a web question, gets no note even
// when it offers the file tools. Its prompt stays shorter, and the model
// isn't told to go looking in the user's folders for an answer that lives
// on the web or in their mail.
func (a *Agent) fileToolsNoteFor(specs []engine.ToolSpec, fileTurn bool) string {
	if !fileTurn || !slices.ContainsFunc(specs, func(s engine.ToolSpec) bool { return builtin.IsFileTool(s.Name) }) {
		return ""
	}
	if a.agentic {
		return exploreNote
	}
	return fileToolsNote
}

// ToolRunner lists the tools the model may use and runs the calls it makes.
// merud passes *dispatch.Dispatcher, the one path every tool call takes
// (AGENTS.md, non-negotiable 4); tests pass a fake.
type ToolRunner interface {
	// Tools returns the allowed tools' schemas, as the model sees them.
	Tools() []engine.ToolSpec
	// Dispatch runs one call and says how it ended. It never fails: a call
	// that couldn't run comes back as an outcome other than "ok", with a
	// Result the model can read.
	Dispatch(ctx context.Context, c dispatch.Call) (dispatch.Result, dispatch.Outcome)
	// Asks reports whether a call to the named tool would ask the user
	// first.
	Asks(name string) bool
	// Refresh lists each connected tool server's tools again, gives each
	// server that isn't connected one try, and returns when all of that has
	// ended. Handle calls it once per turn that offers tools, before it
	// lists them.
	Refresh(ctx context.Context)
}

// turn holds what the rounds need to know about the turn they run in.
// Handle fills it in as the turn goes.
type turn struct {
	sess   *transcript.Session
	source rpc.Source
	// scope is where the user let the turn look, one of the rpc.Scope
	// constants; see scope.go.
	scope string
	// images are the bytes of the images the question carried, for the
	// question's message; nil for none. See images.go.
	images  [][]byte
	traceID string
	emit    func(rpc.Event) error
	approve rpc.ApproveFunc
	// rounds counts the model calls so far. The turn span and metric
	// report it as the turn's iterations.
	rounds int
	// calls counts the tool calls so far, to number their IDs.
	calls int
	// succeeded counts the calls that ended "ok". A turn where it stays 0
	// changed nothing, so Handle checks its answer for a claim that it
	// did; see claimsAction.
	succeeded int
	// question is what the user typed, for dispatch.Call.Question: see
	// userWords.
	question string
	// seen maps each call the turn ran, by callKey, to the result the
	// model read, so a repeat gets the same result without running again.
	// Only converse's goroutine touches it, so it needs no lock.
	seen map[string]string
	// repeats counts the calls the model repeated this turn. At maxRepeats
	// the turn stops offering tools.
	repeats int
	// emptyRetry is true once the turn has asked the model a second time
	// after a round with no text (see retriesEmpty). A turn does it once.
	emptyRetry bool
	// outputRetry is true once the turn has run a round again because
	// Ollama couldn't read what the model wrote (see retriesOutput). A turn
	// does it once, apart from the empty-reply retry.
	outputRetry bool

	// mu guards cites, which the calls of one round, each in its own
	// goroutine, count up at the same time.
	mu sync.Mutex
	// cites counts the citation numbers handed out so far: to the
	// excerpts in the prompt, then to those tools return (see nextCites).
	cites int
	// sources holds every excerpt the turn has shown the model, numbered,
	// for the "sources" event. runTools adds the ones tools return.
	sources []rpc.Citation
}

// nextCites reserves n citation numbers and returns the first. runTools
// hands it to dispatch through the context, so a tool such as search_files
// numbers its excerpts after the prompt's and after any other call's, and
// the model's [n] marks name one excerpt across the whole turn.
func (t *turn) nextCites(n int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	first := t.cites + 1
	t.cites += n
	return first
}

// userWords returns question, then each earlier question of the user's in
// history, newest first, one per line. It holds only what the user typed:
// never the prompt's excerpts, memories or tool results, where a URL could
// come from a file or a web page instead of the user. web_fetch's guard
// reads it to tell a URL the user gave from one the model made up.
func userWords(question string, history []engine.Message) string {
	words := []string{question}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == engine.RoleUser {
			words = append(words, history[i].Content)
		}
	}
	return strings.Join(words, "\n")
}

// toolSpecs returns the tool schemas to offer on route: every tool the
// ToolRunner allows on "tools" and "search+tools"; on "search", the file
// tools, the local commands that don't ask first, and datetime and
// about_meru; on "direct", datetime and about_meru alone; and nil when
// tools are off. A model can't
// call a tool it hasn't seen, and the prompt stays shorter (ARCHITECTURE.md,
// "Who decides what").
//
// "search" gets the file tools because a question such as "write about
// everything in my work folder" lands there, and ten excerpts can't cover
// a folder. The tools only read what search could already reach. It gets
// the commands that don't ask because the user declared them to report on
// something, and a question such as "what changed in the meru repo this
// week?" lands on "search" when meru is an indexed folder. A command that
// asks first changes something, so it waits for a tools route.
func (a *Agent) toolSpecs(route string) []engine.ToolSpec {
	if a.tools == nil {
		return nil
	}
	switch route {
	case "tools", "search+tools":
		return a.tools.Tools()
	case "search":
		var specs []engine.ToolSpec
		for _, s := range a.tools.Tools() {
			if builtin.EveryRoute(s.Name) || builtin.IsFileTool(s.Name) ||
				(toolKind(s.Name) == dispatch.KindCommand && !a.tools.Asks(s.Name)) {
				specs = append(specs, s)
			}
		}
		return specs
	}
	// Every other route, "direct" included, gets datetime and about_meru
	// alone. They only read, cost short schemas, and "what day is
	// Christmas?" or "which model are you?" route direct.
	var specs []engine.ToolSpec
	for _, s := range a.tools.Tools() {
		if builtin.EveryRoute(s.Name) {
			specs = append(specs, s)
		}
	}
	return specs
}

// schemaChars returns the size of the tool schemas in characters: each
// tool's name, description and argument schema. Divided by four, it gives
// the rough token count the context-budget metric records.
func schemaChars(specs []engine.ToolSpec) int {
	n := 0
	for _, s := range specs {
		n += utf8.RuneCountInString(s.Name) + utf8.RuneCountInString(s.Description) + utf8.RuneCount(s.Parameters)
	}
	return n
}

// toolKind says which kind of source a tool's full name points at:
// "a2a.<agent>.<skill>" is an A2A agent, "cmd.<name>" a local command,
// "<server>.<tool>" an MCP server, and a name with no dot a built-in tool.
func toolKind(name string) string {
	switch {
	case strings.HasPrefix(name, "a2a."):
		return dispatch.KindA2A
	case strings.HasPrefix(name, "cmd."):
		return dispatch.KindCommand
	case strings.Contains(name, "."):
		return dispatch.KindMCP
	default:
		return dispatch.KindBuiltin
	}
}

// maxRepeats is how many repeated calls a turn allows before it stops
// offering tools. A model that asks for the same thing a second time has
// ignored the note that the first repeat carried, so a third ask is
// unlikely to go better.
const maxRepeats = 2

// The notes a repeated call's result carries, after the earlier result.
const (
	repeatNote = "[Meru didn't run this call again: you made the same call earlier in this answer, " +
		"and the result above is the one it gave. Use it, or call a different tool.]"
	lastRepeatNote = "[Meru didn't run this call again: you made the same call earlier in this answer, " +
		"and the result above is the one it gave. Tools are off for the rest of this answer: answer now with what you have.]"
)

// converse runs the turn's rounds (ARCHITECTURE.md, "Agent loop", steps 3
// to 5). Each round streams one answer from the main model with specs on
// offer. When the model calls tools, converse runs them through runRound,
// adds the calls and their results to msgs, and starts the next round. The
// turn ends when the model answers without calling a tool.
//
// The last round a.maxRounds allows offers no tools, so the model has to
// answer with what it has. So does every round after the model has
// repeated a call maxRepeats times. With no specs, a turn has one round.
// A round that ends the turn with no text gets one more try when the turn
// has a round left: see retriesEmpty. So does a round whose output Ollama
// couldn't read: see retriesOutput.
//
// It returns the final round's text and stop reason, with the usage
// counters summed over every round and the time of the turn's first text
// token. It fails when a model call fails, when emit fails, or when ctx
// ends; the reply then holds the text the failed round streamed. A round
// whose output Ollama couldn't read, with no retry left, fails with an
// error that wraps engine.ErrModelOutput, and Handle ends the turn
// endBadOutput.
func (a *Agent) converse(ctx context.Context, t *turn, msgs []engine.Message, specs []engine.ToolSpec) (reply, error) {
	var total reply
	// nudge is true for the round that retries one whose output Ollama
	// couldn't read. That round's call alone carries outputNudge.
	nudge := false
	for {
		t.rounds++
		start := time.Now()
		offer := specs
		if t.rounds >= a.maxRounds || t.repeats >= maxRepeats {
			offer = nil
		}
		call := msgs
		if nudge {
			// slices.Concat builds a new slice, so msgs keeps no nudge for
			// the rounds after this one.
			call = slices.Concat(msgs, []engine.Message{{Role: engine.RoleUser, Content: outputNudge}})
			nudge = false
		}
		rep, err := a.answer(ctx, call, offer, t.emit)
		total.add(rep)
		if err != nil {
			total.text = rep.text
			if !errors.Is(err, engine.ErrModelOutput) {
				return total, err
			}
			retry := a.retriesOutput(ctx, t)
			// Ollama's own words go to the log, never to the chat. The
			// model name comes from config, so it is one of a few values.
			a.log.WarnContext(ctx, "ollama couldn't read the model's output", "model", a.Main(),
				"round", t.rounds, "retry", retry, "err", err)
			if !retry {
				return total, err
			}
			t.outputRetry = true
			// The user has read the failed round's text, if any. A blank
			// line keeps the retry's text from running on from it.
			if strings.TrimSpace(rep.text) != "" {
				if err := t.emit(rpc.Event{Type: rpc.EventToken, Text: "\n\n"}); err != nil {
					return total, err
				}
			}
			nudge = true
			continue
		}
		// A model that calls a tool it wasn't offered gets no call run: the
		// round is its answer.
		if len(rep.calls) == 0 || len(offer) == 0 {
			a.log.DebugContext(ctx, "round finished", "round", t.rounds, "tool_calls", 0,
				"ms", time.Since(start).Milliseconds())
			if a.retriesEmpty(t, rep) {
				return a.retryEmpty(ctx, t, msgs, total)
			}
			total.text, total.doneReason = rep.text, rep.doneReason
			return total, nil
		}

		// The model reads its own calls back before their results, as
		// Ollama's chat format expects.
		msgs = append(msgs, engine.Message{Role: engine.RoleAssistant, Content: rep.text, ToolCalls: rep.calls})
		results, err := a.runRound(ctx, t, rep.calls)
		if err != nil {
			return total, err
		}
		msgs = append(msgs, results...)
		a.log.DebugContext(ctx, "round finished", "round", t.rounds, "tool_calls", len(rep.calls),
			"ms", time.Since(start).Milliseconds())
	}
}

// retriesEmpty says whether converse should ask the model once more after
// rep, a round that ended the turn with no text. A thinking model can spend
// a whole round reasoning and then stop with nothing to show. A real turn
// ran web_search and got eight good results; the next round held 205
// tokens of hidden thinking and no answer, and the user read "Sorry, I
// couldn't answer that." with seven rounds left. Asked again, the same
// question worked.
//
// It says yes once per turn, only when the turn has a round left, and never
// after a round that stopped at the token cap: a model that thought until
// max_output_tokens ran out would likely do it again, and the user would
// wait twice as long for the same apology. A turn out of time never gets
// here, because its last round fails with the deadline's error.
func (a *Agent) retriesEmpty(t *turn, rep reply) bool {
	return strings.TrimSpace(rep.text) == "" && rep.doneReason != "length" &&
		!t.emptyRetry && t.rounds < a.maxRounds
}

// emptyNudge is the message the empty-reply retry adds after the turn's
// messages. It goes in as a user message, because some models' chat
// templates accept a system message only in first place (see
// buildMessages). It lives only in that one model call. The transcript
// never records it, so the session's history holds only what the user typed.
const emptyNudge = "Answer the question now in plain text, from the tool results above " +
	"and what you know. Don't call a tool."

// retryEmpty makes the one retry that retriesEmpty allows. It calls the
// model with msgs, the turn so far, plus emptyNudge, and offers no tools,
// so the model has to answer in text. The empty round's hidden thinking
// stays out: feeding it back would hand the model its own dead end.
//
// It returns total, the turn's stats so far, with this call's usage added
// and its text and stop reason as the turn's. When this call is empty too,
// Handle ends the turn with the usual apology. It fails as answer does.
func (a *Agent) retryEmpty(ctx context.Context, t *turn, msgs []engine.Message, total reply) (reply, error) {
	t.rounds++
	t.emptyRetry = true
	a.log.DebugContext(ctx, "empty reply: asking the model once more, with no tools", "round", t.rounds)
	start := time.Now()
	// slices.Concat builds a new slice, so msgs itself doesn't change.
	nudged := slices.Concat(msgs, []engine.Message{{Role: engine.RoleUser, Content: emptyNudge}})
	rep, err := a.answer(ctx, nudged, nil, t.emit)
	total.add(rep)
	total.text, total.doneReason = rep.text, rep.doneReason
	if err != nil {
		return total, err
	}
	a.log.DebugContext(ctx, "round finished", "round", t.rounds, "tool_calls", 0,
		"empty_retry", true, "ms", time.Since(start).Milliseconds())
	return total, nil
}

// retriesOutput says whether converse should run a round again after
// Ollama couldn't read what the model wrote (engine.ErrModelOutput). A real
// turn showed why. qwen3.6:35b on Ollama 0.34 wrote a malformed tool call,
// Ollama's tool-call parser sent "XML syntax error on line 8: element
// <function> closed by </parameter>" in the middle of the stream, and the
// user read that error as the answer. A model that slips once often gets
// the call right when asked again.
//
// It says yes once per turn, only when the turn has a round left and time
// to spend. This retry counts apart from the empty-reply one: each fixes a
// different slip, and max_rounds and turn_timeout still bound the turn.
func (a *Agent) retriesOutput(ctx context.Context, t *turn) bool {
	return !t.outputRetry && t.rounds < a.maxRounds && ctx.Err() == nil
}

// outputNudge is the message the retry after unreadable output adds after
// the turn's messages, for that one call. Like emptyNudge it goes in as a
// user message and never reaches the transcript. The retry offers the same
// tools the failed round did (none when it is the turn's last round), and
// the failed round's text and broken call stay out: feeding them back would
// show the model the mistake to copy.
const outputNudge = "Your last tool call didn't parse, so it didn't run. " +
	"Call the tool again with valid arguments, or answer in plain text."

// add folds one round's reply into r: the token counts and durations sum,
// and firstToken keeps the earliest text of the turn. It leaves r.text
// alone; converse sets it from the final round.
//
// add has a pointer receiver (r *reply), so it changes the caller's reply
// instead of a copy.
func (r *reply) add(next reply) {
	r.usage.PromptTokens += next.usage.PromptTokens
	r.usage.OutputTokens += next.usage.OutputTokens
	r.usage.LoadDuration += next.usage.LoadDuration
	r.usage.PromptEvalDuration += next.usage.PromptEvalDuration
	r.usage.EvalDuration += next.usage.EvalDuration
	r.usage.TotalDuration += next.usage.TotalDuration
	if r.firstToken.IsZero() {
		r.firstToken = next.firstToken
	}
}

// runRound answers one round's tool calls and returns one RoleTool message
// per call, in call order. A call the model already made this turn, with
// the same name and arguments (see callKey), doesn't run again: its message
// holds the earlier result and repeatNote, or lastRepeatNote once the turn
// reaches maxRepeats. The rest run through runTools. A call that appears
// twice in one round runs once, and the second copy counts as a repeat.
//
// A repeat is not a tool call. It never reaches dispatch, runs nothing, and
// gets no "tool_call" event, transcript line or tool_calls row, so the
// audit log holds what ran and nothing else. Only a debug log line and the
// turn span's meru.turn.repeated_calls count record it. Running a repeat
// through dispatch would log a second call that did no new work, and would
// ask the user a second time for a call that asks first.
//
// It fails when runTools does.
func (a *Agent) runRound(ctx context.Context, t *turn, calls []engine.ToolCall) ([]engine.Message, error) {
	if t.seen == nil {
		t.seen = make(map[string]string)
	}
	keys := make([]string, len(calls))
	fresh := make([]bool, len(calls)) // true for a call to run now
	var run []engine.ToolCall
	for i, c := range calls {
		keys[i] = callKey(c)
		_, before := t.seen[keys[i]]
		if before || slices.Contains(keys[:i], keys[i]) {
			continue
		}
		fresh[i] = true
		run = append(run, c)
	}

	out := make([]engine.Message, len(calls))
	if len(run) > 0 {
		results, err := a.runTools(ctx, t, run)
		if err != nil {
			return nil, err
		}
		// results come back in the order of run, which keeps call order.
		j := 0
		for i := range calls {
			if fresh[i] {
				out[i] = results[j]
				t.seen[keys[i]] = results[j].Content
				j++
			}
		}
	}
	for i, c := range calls {
		if fresh[i] {
			continue
		}
		t.repeats++
		note := repeatNote
		if t.repeats >= maxRepeats {
			note = lastRepeatNote
		}
		a.log.DebugContext(ctx, "tool call repeated; handed back the earlier result",
			"tool", c.Name, "repeats", t.repeats, "tools_off", t.repeats >= maxRepeats)
		out[i] = engine.Message{Role: engine.RoleTool, ToolName: c.Name, Content: t.seen[keys[i]] + "\n\n" + note}
	}
	return out, nil
}

// callKey returns the text that tells one call from another: the tool's
// name and its arguments in canonical JSON. Decoding the arguments and
// encoding them again puts object keys in sorted order and drops spaces, so
// {"a":1, "b":2} and {"b":2,"a":1} match. Missing arguments count as {}.
// Arguments that aren't valid JSON are compared as they came.
func callKey(c engine.ToolCall) string {
	args := argsOf(c.Arguments)
	// any holds whatever the JSON decodes to; json.Marshal writes the keys
	// of a map in sorted order.
	var v any
	if err := json.Unmarshal(args, &v); err == nil {
		if b, err := json.Marshal(v); err == nil {
			args = b
		}
	}
	return c.Name + "\x00" + string(args)
}

// runTools runs one round's tool calls through dispatch, all at the same
// time, and returns one RoleTool message per call, in call order, for the
// model to read next round.
//
// It gives each call an ID ("call-1", "call-2", ... across the turn, or the
// engine's own ID when it sent one) and sends a "tool_call" event for each
// before any runs. Each call then runs in its own goroutine and sends its
// "tool_result" event as it ends, so a quick call reports before a slow one.
// Dispatch writes the calls' transcript lines through Call.Append.
//
// A call that returns excerpts from the user's files, such as search_files,
// numbers them through t.nextCites, and its tool_result event carries them.
// Once every call has ended, runTools adds them to t.sources and sends a
// "sources" event with every source so far, which the clients read in
// place of the one before, so their Sources: list covers the excerpts the
// model found through tools.
//
// It fails when emit fails or ctx ends. Either way it waits for every call
// to return first, so no goroutine outlives the turn.
func (a *Agent) runTools(ctx context.Context, t *turn, calls []engine.ToolCall) ([]engine.Message, error) {
	ids := make([]string, len(calls))
	for i, c := range calls {
		t.calls++
		ids[i] = c.ID
		if ids[i] == "" {
			ids[i] = fmt.Sprintf("call-%d", t.calls)
		}
		ev := &rpc.ToolEvent{ID: ids[i], Name: c.Name, Kind: toolKind(c.Name), Args: c.Arguments}
		if err := t.emit(rpc.Event{Type: rpc.EventToolCall, Tool: ev}); err != nil {
			return nil, err
		}
	}

	// appendLine records a span and a debug line for each transcript line
	// dispatch writes, as it does for the agent's own lines.
	appendTo := func(l transcript.Line) error { return a.appendLine(ctx, t.sess, l) }

	// Each goroutine writes only its own slot of out and found, so they
	// need no lock.
	out := make([]engine.Message, len(calls))
	found := make([][]rpc.Citation, len(calls))
	outcomes := make([]string, len(calls))
	// errgroup.WithContext returns a group and a ctx that ends when any of
	// the group's functions returns an error. g.Go starts a function in a
	// new goroutine; g.Wait waits for all of them and returns the first
	// error.
	g, gctx := errgroup.WithContext(ctx)
	gctx = dispatch.WithCiteNumbers(gctx, t.nextCites)
	for i, c := range calls {
		g.Go(func() error {
			res, outcome := a.tools.Dispatch(gctx, dispatch.Call{
				ID:       ids[i],
				Name:     c.Name,
				Args:     argsOf(c.Arguments),
				Session:  t.sess.ID(),
				Question: t.question,
				Source:   t.source,
				Append:   appendTo,
				Approve:  t.approve,
				TraceID:  t.traceID,
			})
			out[i] = engine.Message{Role: engine.RoleTool, ToolName: c.Name, Content: res.Text}
			found[i] = res.Sources
			outcomes[i] = outcome.Outcome
			return t.emit(rpc.Event{Type: rpc.EventToolResult, Tool: &rpc.ToolEvent{
				ID: ids[i], Name: c.Name, Kind: toolKind(c.Name),
				Outcome: outcome.Outcome, DurationMillis: outcome.Duration.Milliseconds(),
				Sources: res.Sources,
			}})
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for _, o := range outcomes {
		if o == dispatch.OutcomeOK {
			t.succeeded++
		}
	}
	// A call cut short by a cancelled turn still returns, with the
	// "cancelled" outcome; the turn stops here instead of asking the model
	// again.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.addSources(found); err != nil {
		return nil, err
	}
	return out, nil
}

// addSources adds the excerpts one round's calls returned to t.sources,
// in number order, and sends a "sources" event with them all. A round
// whose calls returned none sends nothing. It fails only when emit does.
func (t *turn) addSources(found [][]rpc.Citation) error {
	added := false
	for _, f := range found {
		if len(f) > 0 {
			t.sources = append(t.sources, f...)
			added = true
		}
	}
	if !added {
		return nil
	}
	// Calls that ran at the same time took their numbers in the order they
	// asked, not call order, so sort by number.
	slices.SortFunc(t.sources, func(x, y rpc.Citation) int { return x.N - y.N })
	return t.emit(rpc.Event{Type: rpc.EventSources, Sources: slices.Clone(t.sources)})
}

// argsOf returns a call's arguments, or an empty JSON object when the model
// sent none, since dispatch expects an object.
func argsOf(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}
