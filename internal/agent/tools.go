// This file holds the tool rounds of a turn: which routes offer tools, the
// loop that calls the main model until it stops calling tools, and the step
// that runs one round's calls through dispatch at the same time.

package agent

import (
	"context"
	"encoding/json"
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

// fileToolsNote replaces toolsNote on a turn that offers only the file
// tools, and perhaps commands: the "search" route with datetime off. It
// tells the model when to reach for them: when the excerpts from search
// don't hold enough.
const fileToolsNote = "When the excerpts below aren't enough, you may read whole files with read_file, " +
	"list folders with list_folder, find every matching line with grep, and search again in other words with search_files."

// exploreNote joins the tools note when [index] retrieval is "agentic" and
// the turn offers the file tools. No excerpts sit in the prompt, so it
// tells the model to look for itself, in the order that finds things
// fastest, and carries the rule on citing that citeRule carries in "auto"
// mode. The two-or-three-rounds limit keeps a small model from searching
// until it runs out of rounds.
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

// noteFor returns the note for a turn that offers specs: toolsNote when
// any is an MCP tool, an A2A skill or a built-in other than the file tools
// (datetime included); otherwise fileToolsNote for file tools and
// commandsNote for commands, the "search" route's two kinds; and "" for
// none. With agentic retrieval, a turn that offers the file tools also gets
// exploreNote, in place of fileToolsNote, since no excerpts sit in its
// prompt.
func (a *Agent) noteFor(specs []engine.ToolSpec) string {
	var files, cmds, others bool
	for _, s := range specs {
		switch {
		case builtin.IsFileTool(s.Name):
			files = true
		case toolKind(s.Name) == dispatch.KindCommand:
			cmds = true
		default:
			others = true
		}
	}
	var notes []string
	if others {
		notes = append(notes, toolsNote)
	} else {
		if files && !a.agentic {
			notes = append(notes, fileToolsNote)
		}
		if cmds {
			notes = append(notes, commandsNote)
		}
	}
	if files && a.agentic {
		notes = append(notes, exploreNote)
	}
	return strings.Join(notes, " ")
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
	// ConnectMissing gives each tool server that isn't connected one try
	// and returns when the tries have ended. Handle calls it once per turn
	// that offers tools, before it lists them.
	ConnectMissing(ctx context.Context)
}

// turn holds what the rounds need to know about the turn they run in.
// Handle fills it in as the turn goes.
type turn struct {
	sess    *transcript.Session
	source  rpc.Source
	traceID string
	emit    func(rpc.Event) error
	approve rpc.ApproveFunc
	// rounds counts the model calls so far. The turn span and metric
	// report it as the turn's iterations.
	rounds int
	// calls counts the tool calls so far, to number their IDs.
	calls int
	// question is what the user typed, for dispatch.Call.Question: see
	// userWords.
	question string

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
// ToolRunner allows on "tools" and "search+tools"; on "search", the three
// file tools (read_file, list_folder, grep) and the local commands that
// don't ask first; and nil on "direct" or when tools are off. A model can't
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
			if s.Name == builtin.DateTime || builtin.IsFileTool(s.Name) ||
				(toolKind(s.Name) == dispatch.KindCommand && !a.tools.Asks(s.Name)) {
				specs = append(specs, s)
			}
		}
		return specs
	}
	// Every other route, "direct" included, gets datetime alone: it reads
	// the clock, costs a short schema, and "what time is it?" or "what day
	// is Christmas?" routes direct.
	for _, s := range a.tools.Tools() {
		if s.Name == builtin.DateTime {
			return []engine.ToolSpec{s}
		}
	}
	return nil
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

// converse runs the turn's rounds (ARCHITECTURE.md, "Agent loop", steps 3
// to 5). Each round streams one answer from the main model with specs on
// offer. When the model calls tools, converse runs them, adds the calls and
// their results to msgs, and starts the next round. The turn ends when the
// model answers without calling a tool.
//
// The last round a.maxRounds allows offers no tools, so the model has to
// answer with what it has. With no specs, a turn has one round.
//
// It returns the final round's text, with the usage counters summed over
// every round and the time of the turn's first text token. It fails when a
// model call fails, when emit fails, or when ctx ends.
func (a *Agent) converse(ctx context.Context, t *turn, msgs []engine.Message, specs []engine.ToolSpec) (reply, error) {
	var total reply
	for {
		t.rounds++
		start := time.Now()
		offer := specs
		if t.rounds >= a.maxRounds {
			offer = nil
		}
		rep, err := a.answer(ctx, msgs, offer, t.emit)
		if err != nil {
			return total, err
		}
		total.add(rep)
		// A model that calls a tool it wasn't offered gets no call run: the
		// round is its answer.
		if len(rep.calls) == 0 || len(offer) == 0 {
			a.log.DebugContext(ctx, "round finished", "round", t.rounds, "tool_calls", 0,
				"ms", time.Since(start).Milliseconds())
			total.text = rep.text
			return total, nil
		}

		// The model reads its own calls back before their results, as
		// Ollama's chat format expects.
		msgs = append(msgs, engine.Message{Role: engine.RoleAssistant, Content: rep.text, ToolCalls: rep.calls})
		results, err := a.runTools(ctx, t, rep.calls)
		if err != nil {
			return total, err
		}
		msgs = append(msgs, results...)
		a.log.DebugContext(ctx, "round finished", "round", t.rounds, "tool_calls", len(rep.calls),
			"ms", time.Since(start).Milliseconds())
	}
}

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
