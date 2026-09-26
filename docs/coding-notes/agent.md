# agent

**Code:** `internal/agent/` (`doc.go`, `agent.go`, `tools.go`, `profile.go`, `recall.go`, `skills.go`, `earlier.go`, `agent_test.go`, `tools_test.go`, `files_test.go`, `agentic_test.go`, `search_test.go`, `observe_test.go`, `usage_test.go`, `profile_test.go`, `recall_test.go`, `skills_test.go`, `earlier_test.go`, `skills_integration_test.go`, `e2e_test.go`)
**Milestone:** v0.1; search in v0.2; tool rounds and usage in v0.3; the profile, recall, skills and earlier conversations in v0.4
**Architecture:** [Agent loop](../../ARCHITECTURE.md#agent-loop), [A question, end to end](../../ARCHITECTURE.md#a-question-end-to-end), [Who decides what](../../ARCHITECTURE.md#who-decides-what), [Retrieval](../../ARCHITECTURE.md#retrieval)

## What it does

`agent` runs one turn: the work between "a question arrived" and "the answer
is saved". `merud` hands `Agent.Handle` to the socket server, and the server
calls it once per question.

The router picks a route, and the route decides whether the turn looks in
your files first and whether the model may call tools:

| Route | Searches your files? | Offers tools? |
| --- | --- | --- |
| `direct` | no, unless the question names an indexed folder | no |
| `search` | yes | only the four file tools, `datetime` and the local commands that don't ask |
| `tools` | yes (the router sends some file questions here, see below) | yes |
| `search+tools` | yes | yes |

With `[index] retrieval = "agentic"` no route searches first. The routes keep
the tools the table gives them, and the model finds text in your files with the
file tools: `search_files`, `grep`, `list_folder` and `read_file`.

A turn with no tools makes one model call. A turn with tools runs in
**rounds**: each round is one model call, and a round in which the model calls
tools runs them and starts another. The turn ends when the model answers
without calling a tool, or at the round cap.

## The picture

```mermaid
sequenceDiagram
    participant S as rpc server
    participant A as Agent.Handle
    participant T as transcript
    participant R as Router
    participant F as Engine (fast)
    participant E as Engine (main)
    participant D as ToolRunner (dispatch)
    S->>A: request
    A->>T: New or Open session
    A->>T: History(history_turns)
    A-->>S: emit session
    A->>T: Append user line
    par route and pick skills at once
        A->>R: Decide(question, history)
    and
        A->>F: Generate(pick prompt), when there are skills
    end
    A-->>S: emit route (with the picked skills)
    opt route isn't direct, or the question names an indexed folder
        A->>A: Searcher.Search(question)
        A-->>S: emit sources (when it found some)
    end
    opt the route offers tools
        A->>D: Refresh (list tools again; one try per server that isn't connected)
    end
    A->>A: Profile.Recall(question), on every route
    loop each round, up to max_rounds
        A->>E: Stream(system + excerpts + history + question + earlier rounds, tool schemas)
        E-->>A: text deltas (emit token) and tool calls
        opt the model called tools
            A-->>S: emit tool_call (one per call)
            par each call at the same time
                A->>D: ToolRunner.Dispatch(call)
                D->>T: tool lines
                A-->>S: emit tool_result
            end
        end
    end
    A->>T: Append assistant line (text, tokens, route, ms, sources)
    A->>A: TurnRecorder.InsertTurn (turns row)
    A-->>S: emit done with stats
    A-->>S: return nil (server sends the done)
```

## Walk through the code

### The Router interface

```go
type Router interface {
    Decide(ctx context.Context, question string, history []engine.Message) (Decision, error)
}
```

An **interface** lists methods; any type with those methods satisfies it. The
agent declares only the one method it calls, so it doesn't import the router
package, and tests pass in a fake that returns a fixed route. `merud` wraps the
real router in a small adapter.

### The Searcher interface

```go
type Searcher interface {
    Search(ctx context.Context, query string) ([]retrieve.Result, error)
    SearchSessions(ctx context.Context, query, excludeSession string, n int) ([]retrieve.SessionResult, error)
}
```

The same trick as `Router`: the agent names the methods it needs. `merud`
passes `searchAdapter`, which calls `retrieve.Search` and
`retrieve.SearchSessions` over the store; tests pass `fakeSearcher`, which
returns fixed results. A `nil` Searcher turns search off, which is what most of
the older tests pass. `SearchSessions` (v0.4) recalls past conversations; see
[Earlier conversations](#earlier-conversations-earliergo).

### The ToolRunner interface

```go
type ToolRunner interface {
    Tools() []engine.ToolSpec
    Dispatch(ctx context.Context, c dispatch.Call) (dispatch.Result, dispatch.Outcome)
    Asks(name string) bool
    Refresh(ctx context.Context)
}
```

`Tools` lists the tools config allows, with the schemas the model reads.
`Dispatch` runs one call. Every tool call in Meru goes through dispatch, which
checks the allowlist, asks you when the tool needs a yes, writes the
transcript lines and the `tool_calls` row, and records the span and metrics.
The agent only builds the `dispatch.Call` and reads what comes back.
`Dispatch` returns no error: a call that couldn't run still comes back with
an outcome (`denied`, `declined`, `error`, `timeout` or `cancelled`) and a
`Result` whose text tells the model what happened. `Asks` says whether a call
to a tool would ask you first; `toolSpecs` uses it to pick the commands the
"search" route may offer. `Refresh` asks each connected tool server for its
tools again, gives each server that isn't connected one try, and returns when
all of that has ended (see
[The tool rounds](#the-tool-rounds-toolsgo)).

`merud` passes `*dispatch.Dispatcher`, which has these four methods. Tests
pass `fakeTools`, which answers from a table by tool name. A `nil`
ToolRunner turns tools off.

### The TurnRecorder interface

```go
type TurnRecorder interface {
    InsertTurn(ctx context.Context, t store.Turn) error
}
```

After each answered turn the agent hands a `store.Turn` row to its
TurnRecorder, and `meru usage` adds the rows up. `merud` passes
`turnRecorder`, which writes the row to the store and then replays the turn's
session into the past-conversation tables (v0.4), so another session can
recall this one at once. Tests pass `fakeTurns`, which keeps the rows in a slice. A `nil` TurnRecorder
keeps no rows, which is what most tests pass.

The agent writes the row, rather than `merud` reading it back from the
transcript, because the agent already holds every fact the row needs: the
session, the source, the final route and the tool-call count. `merud` would
have to reopen the session file after each turn to find them.

### The Profile interface

```go
type Profile interface {
    Profile() ([]memory.Memory, error)
    Recall(ctx context.Context, query string) ([]retrieve.Memory, error)
}
```

The profile is what Meru knows about you: the memory files in `me/` and
`preferences/` (`rpc.ProfileKinds()`). `merud` passes `profileAdapter`, which
reads those two folders with `memory.Store.ListKind`. Tests pass
`fakeProfile`. A `nil` Profile leaves the section out, and recall with it.

`Recall` returns the memories outside the profile kinds that fit a question,
best first. `profileAdapter` calls `retrieve.SearchMemories` over the store. The
two methods share one interface because both hand the agent your memories, and
one more parameter on `New` would touch every test that builds an agent.

`Profile` may return memories and an error together, when one file can't be
read and the rest can. The agent logs the error as a warning and uses what it
got. A folder it can't read at all gives no memories, and the turn goes on
without the section.

### New and filesNote

`New` builds the system prompt once and adds `filesNote(cfg.Index.Folders,
agentic)` to the end of it, so every turn tells the model which folders Meru
searches:

```text
Meru indexes and searches the user's files in these folders: ~/notes, ~/repos/meru. When a question needs them, Meru searches first and puts the best excerpts below.
```

With `[index] retrieval = "agentic"`, `New` sets `a.agentic`, and the note
leaves out the second sentence, since Meru searches nothing first. The note
says nothing about the file tools: that goes in a note of its own, which only
file turns get (see `fileToolsNoteFor` below). So the note every turn carries
stays the same, and Ollama can reuse its work on it. With no folders, the note says that Meru hasn't indexed any
files yet and that the user lists folders under `[index] folders` in
`~/.meru/config.toml`. Without the note a small model answered "I don't have
access to your files" while it read excerpts from them, and couldn't say what
Meru had indexed.

Before the files note comes `whoIsWho`: the person asking is the user, the files
are theirs, and "I", "me" and "my" in a question mean the user, never the model.
It joins every system prompt, a custom one from config too. Without it, the 2B
model read "did I visit Lisbon?" as a question about Meru, and answered that
Meru had no record of a visit while the excerpts named the user as the
traveller.

`New` also keeps `folderNames(cfg.Index.Folders)`: the last part of each folder
path, in lower case, such as `meru` for `~/repos/meru`. It drops names under
three letters, which match too many ordinary words, and keeps each name once.

### The profile section (profile.go)

`New` keeps the configured prompt with `whoIsWho` in `a.system`, and the files
note in `a.filesNote`. On each turn, `prompt` puts the profile between them:

```text
<system prompt>

<whoIsWho>

What you know about the user:
- Name is Dana Reyes
- Works on the AI registry team at Example Corp
- Likes short answers

<filesNote>
```

The profile follows `whoIsWho`, so the rule that "I" means the user and the
facts about who the user is sit side by side. Without them, the 2B model read a
hotel booking and guessed that you were the other guest it named.

The whole system prompt, in order: the configured prompt, `whoIsWho`, today's
date (`today`), the profile, `filesNote`, `toolsNote` on a turn that offers tools, and the list of
skills; then `fileToolsNote` or `exploreNote` on a file turn that offers the
file tools; then the recalled memories, the picked skills' instructions, and last
the files section, which holds the numbered excerpts and then the earlier
conversations. `prompt` takes the changing parts in one `sections` struct and
leaves out each empty part.

### budget.go

`budget.go` holds the context budget in one place: a cap per section in
characters, and the order above. The order puts every part that stays the same
from turn to turn before every part the question changes. Ollama reuses its
work on a prompt's opening and stops at the first token that differs, so a
follow-up in the same session reprocesses only the changing parts, the history
and the question. Before v0.4's budget, the recalled memories sat right after
the profile, and each question's new memories cost Ollama the rest of the
prompt.

`skillsSection` returns two strings for that reason: the list of skills, which
stays put, and the picked skills' instructions, which move with the question.

`trimHistory` cuts the history to `maxHistoryChars`, oldest first. It drops a
question together with its answer, so the history never opens with an answer
to a question that isn't there. `[agent] history_turns` still caps the number
of turns; the character cap catches a few long answers that the turn count
would let through. `budget_test.go` checks both the cut and the order.

`formatProfile` builds the section, and it is a plain function so the tests can
call it with any memories:

- **Order.** `me` first, then `preferences`, oldest first inside each, so your
  name, usually the first fact saved, leads. `Created` holds only a date, so the
  file's modification time breaks a tie between two facts from one day, and the
  ID breaks any tie left.
- **One line each.** `strings.Fields` splits a fact at every run of spaces and
  line breaks, and joining the pieces with one space gives one line.
- **The cap.** The section holds at most 2,000 characters, header included. When
  the facts hold more, `formatProfile` walks them newest first and keeps each one
  that still fits, so a newer fact, which more often corrects an older one, wins.
  It returns how many it left out, and `profileSection` logs that at debug.
- **Empty means nothing.** With no facts, the section is `""` and the prompt has
  no header. `meru chat` is the one that tells you Meru doesn't know you yet.

`profileSection` reads the files on every turn, so a hand edit shows in the next
answer. Reading 20 files takes about 0.6 ms, too little to earn a cache.

### Recalled memories (recall.go)

`Handle` calls `memorySection` on every route, `direct` included, right before it
builds the prompt. A preference such as "always ask before sending mail" matters
most on a turn that runs tools, and a fact about a person matters on a direct
question about them. The query is the same `searchQuery` the file search uses, so a
follow-up borrows the earlier question's subject.

`memorySection` asks `Profile.Recall` and hands the result to `formatMemories`:

```text
Things you remember that may matter here:
- (people) Sam Lee is the user's manager
- (projects) Plans a vegetable garden
```

- **One line each, with the kind.** The kind tells the model what sort of fact it
  reads. A fact's line breaks become spaces, as in the profile.
- **Best first, capped.** The lines keep recall's order. The section holds at most
  2,400 characters (600 tokens at four characters a token), header included; a
  line that doesn't fit is left out, and a shorter one after it may still fit.
- **Its own section.** It sits right after the profile and before `filesNote`,
  apart from the numbered excerpts, so the model never cites a memory as a file.
- **A failure is a warning.** When recall fails, the turn goes on without the
  section.

**One metric for both sections.** `prompt` records the profile and the recalled
memories together as `meru.context.tokens` with section `memories`, through
`recordMemoryTokens`. Two records under one label would count two samples per
turn and halve the average; one number gives the prompt's whole share of memory,
which is what a context budget needs.

### The remember rule

A third rule gives tools to a turn that asks Meru to remember something. The
router can send "remember that my name is Dana" to `direct`, which offers no
tools, and the model then says it will remember and saves nothing. So when the
question holds `remember` as a whole word, the route lacks the full set of tools, and the
tools on offer include `remember`, `withTools` adds them, as for a tool server.
`asksToRemember` makes the check with `namesFolder`, so "remembered" doesn't
count. It is one of the four signs `toolTarget` checks, so a `tools` turn that
asks Meru to remember also skips the search of your files. A wrong guess, such as "do you remember the trip?", costs a prompt
that holds the tool schemas; the model need not call any.

### Skills (skills.go)

A skill is a Markdown file of instructions for one kind of task (see
[skills](skills.md)). The agent lists every skill in the prompt and loads the
full instructions of only the ones a question needs. That split is called
**progressive disclosure**: a new skill costs the prompt one line until a
question calls for it.

```go
type Skills interface {
    Registry(ctx context.Context) *skills.Registry
}
```

`merud` passes its skill service, which loads the registry again when you edit
`~/.meru/skills` (see [merud](merud.md)). Tests pass `fixedSkills`. The agent
gets it through `UseSkills`, called once before the first turn, rather than as
a parameter of `New`, so the many tests that build an agent without skills
stay as they were. With no `Skills`, turns list and load none.

**The pick runs beside the router.** `routeAndPick` replaces the plain
`route` call in `Handle`. It starts two goroutines with an `errgroup`: one asks
the router for the route, the other asks the fast model which skills the
question needs. Neither needs the other's answer. Only the route can fail the
turn; a failed pick logs a warning and the turn goes on with no skills.

Side by side is also faster than it looks. On the development machine with
MiniCPM5-2B, `TestIntegrationRouteAndPick` measured 70 ms a turn for the route
then the pick, and 28 ms for both at once. One after the other, the two prompts
take turns in one Ollama slot and each throws away the other's cached prompt;
side by side, each keeps a slot and its cache.

**The pick call.** `pickSkills` skips the call when the registry is empty.
Otherwise it sends the fast model a short prompt with thinking off
(`engine.Options.NoThink`), temperature 0 and at most 20 tokens:

```text
You choose which skills help answer the user's message. A skill is a set of instructions for one kind of task.

Skills:
- explainer: Build a self-contained HTML explainer for a technical topic ...
- writing: Write prose people will actually read. ...

Reply with the names of the skills this message needs, at most 2, separated by commas. Reply "none" when no skill fits, as for a plain question, a lookup or small talk. Reply with names only, no other words.
```

The question follows as the user's message. `parsePick` splits the answer at
anything that can't be part of a skill name, keeps the words that name a loaded
skill, drops repeats and stops at two. "none", "None." or a made-up name all
give no skills, so the model can't load something that isn't there. On the
built-in skills, `TestIntegrationPickSkills` saw it pick `writing` for "write a
short email to my landlord" and for a paragraph to tidy, `web-research` for
"search the web for the latest Go release" and for the weather, nothing for
"what is the capital of France?" or "hi there", and `web-research` with
`explainer` for an explainer page, each in about 35 to 65 ms. It asserts the
landlord and Go release picks; the rest it logs, because a 2B model's second
choice moves with every skill added. With `web-research` loaded, questions about
the latest version also pull in `writing` as a second skill, which costs prompt
space but no wrong answer.

**Scoring the pick.** `make pick-eval` runs `TestPickEval`, which asks the pick
each question in `testdata/picks.jsonl` three times and prints the share it got
right, by kind and overall. The file holds 21 invented questions of four kinds:
how to use a program, current facts, the user's own files, and plain chat.
`pickRight` in `pickeval_test.go` decides what counts: the pick names every
wanted skill and no research skill (`file-research` or `web-research`) the
question doesn't want. An extra `writing` or `explainer` still counts, since it
changes how the answer reads, not where the facts come from. On the `lite` fast
model the old descriptions scored 50 of 63 and sent the btop question to
`file-research` all three times; the new ones score 52 with no stray
`file-research`. A line in the pick prompt, "pick file-research only when the
message asks about the user's own files", scored 42: naming the skill drew the
model to it, so the prompt stays as it was. The three asks of one question
mostly agree, but the first can differ from the other two, because Ollama
reuses its work on the prompt from the second ask on.

**The prompt sections.** `skillsSection` builds the text that `prompt` puts
after the tools note and before the excerpts from your files:

```text
Skills you can use:
- explainer: Build a self-contained HTML explainer ...
- writing: Write prose people will actually read. ...

Follow these instructions for this answer:

Skill: writing

# Writing Skill
...
```

The list goes into every turn that has skills; the second part only when the
pick chose some. `formatBodies` caps the instructions at 3,000 tokens (12,000
characters). The first skill goes in whole even past the cap, because half a
skill's steps can mislead the model more than none. A second skill gets what is
left, cut at a line break and closed with a note that Meru cut the rest.
`skillsSection` records the whole section's size as `meru.context.tokens` with
section `skills`.

**Who sees the pick.** The `route` event carries the names in `Skills`, each an
`rpc.SkillInfo` with only `Name` set, and `meru chat` shows them in the route
badge, as in `direct · 0.91 · writing`. The turn span gets `meru.skills`, the
names joined with commas; they come from the registry, so the attribute stays
a small set. The pick has its own `meru.skills.pick` span with a `gen_ai.chat`
span under it, and a `skills picked` debug line.

**A picked skill brings its tools.** A skill's `allowed-tools` key names the
tools its steps use (see [skills](skills.md)). The bug that led here: "help me
understand btop with some simple commands" routed `direct` at 0.761 (`tools`
0.022), and the pick chose `web-research`, whose first step is "search first
with web_search". A `direct` turn offers only `datetime`, so the 2B model
called `datetime` with no arguments in all eight rounds. The router can't fix
this: it never sees the skill.

`skillTools` runs in `respond` after the other route rules and before the
route event. For each picked skill it keeps the names that look like Meru
tools (`toolShaped`: a built-in name, or a name with a dot) and checks which of
them the `ToolRunner` lists:

```go
picked.names, skillTools = a.skillTools(ctx, picked)
if missing := notOffered(skillTools, a.toolSpecs(dec.Route)); len(missing) > 0 {
    if r, ok := withTools(dec.Route); ok {
        a.log.DebugContext(ctx, "route changed: a picked skill uses tools the route lacks", ...)
        dec.Route = r
    }
}
```

The `ToolRunner` is dispatch, and dispatch lists only what config allows, so a
skill can't turn on a tool you left off. When a missing tool would come from
an MCP server or A2A agent, `skillTools` first calls `Refresh`, as a
tools route does. A skill whose tools are all off, such as `web-research` with
no SearXNG and no `web_fetch`, drops out of `picked.names`, so its body stays
out of the prompt and its name off the route event. A debug line says why.

Widening from `direct` to `tools` would normally make the turn a file turn
(see `aboutFiles`) and search your files first. A skill that adds only web
tools shouldn't do that, so `fileTurn` changes only when the added tools
include a file tool. `slices.ContainsFunc` reports whether any item passes the
function.

### Handle

`Handle` has the signature of `rpc.Handler`, so `merud` passes `a.Handle`
straight to `rpc.Serve`. Its steps follow the diagram, and each is a short
method that opens its own span under `meru.turn` and writes one debug line:
`openSession` (`meru.session`), `appendLine` (`meru.transcript.append`),
`routeAndPick` (the router's `meru.route` and `meru.skills.pick`, side by
side), `searchFiles` (`meru.search`, with
retrieval's `meru.retrieve` under it), `prompt` (`meru.prompt`) and `answer`
(`gen_ai.chat`, once per round). Two details:

**The turn span and metrics are recorded in one deferred function.**

```go
func (a *Agent) Handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) (err error) {
    ...
    defer func() {
        outcome := outcomeOf(ctx, err)
        ...
        span.End()
        obs.RecordTurn(context.WithoutCancel(ctx), obs.Turn{...})
    }()
```

`err` is a **named result**: the deferred function reads the error `Handle`
returns, from any of its `return` statements, and records
`ok`, `error` or `cancelled`, with the number of rounds as
`meru.turn.iterations`. `context.WithoutCancel` keeps the trace but drops
the cancel, so a cancelled turn still gets its metric. The same function
writes the turn's one info line, `logTurn`.

`approve` is the rpc server's way to ask you about a tool call. The agent
never calls it; it hands it to dispatch in each `dispatch.Call`.

**`respond` does the middle of the turn.** Routing, the route rules, the
search, the prompt and the rounds live in `respond`, which returns a
`response`: the route, the reply and the files it read. `Handle` keeps the
start (session, user line) and the end (assistant line, `turns` row, `done`).
The split lets `Handle` decide in one place what to do when the middle stops
early.

**A turn has a deadline.** `Handle` wraps the context:

```go
tctx, cancel := context.WithTimeout(ctx, a.turnTimeout)
defer cancel()
...
res, err := a.respond(tctx, t, question, history)
if err != nil && (tctx.Err() == nil || ctx.Err() != nil) {
    return err // a model error, or the user hung up
}
```

`context.WithTimeout` returns a child context that ends when the parent ends
or when the time runs out, whichever comes first. `cancel` frees its timer;
`defer` runs it when `Handle` returns. Every model and tool call under `tctx`
stops when it ends: the engine's HTTP request to Ollama is cancelled, so
Ollama stops too. When `tctx` ended but `ctx` didn't, the turn ran out of time
and still answers; when `ctx` ended, you hung up and the turn fails as before.

**A turn that ends without a full answer still answers.** `endOf` names how it
ended: `timeout`, `cut_off` (the last model call stopped with
`done_reason = "length"`) or `gave_up` (the answer holds no text, as when the
last round only called tools). `endTurn` then sends the words as ordinary
`token` events, so `meru` and `meru chat` show them with no change:

- no text yet: `Sorry, I couldn't answer that. Try asking again, or rephrase the question.`
- some text already streamed: that text stays, and a note follows it, one for
  the deadline and one for the length limit.

The outcome replaces `ok` in the turn span, the `turn` log line and
`meru.turn.duration`, and goes in the assistant line's `Outcome` field. All
three values come from a fixed list, so the metric stays bounded.

**History is read before the question is written**, so the new question doesn't
show up twice in the prompt.

### searchFiles

On a file turn, `Handle` calls `searchFiles` between routing and the prompt.
`aboutFiles(route, target)` says which turns those are: `search` and
`search+tools` always, `tools` unless the question points at a connected tool
(see the tool rule below), and `direct` never. `tools` searches because the
router sends some questions about your files there, and an answer from the
files beats one from the model alone.

One rule runs first. When the router says `direct` and the question names an
indexed folder, the route becomes `search`:

```go
if dec.Route == "direct" && a.search != nil && namesFolder(question, a.folderNames) {
    dec.Route = "search"
}
```

`namesFolder` splits the question into words with `words` and looks for one of
the folder names. It matches whole words and ignores case, so "Meru" and
"meru's" match `meru` and "merudaemon" doesn't. Even with the folders in its
prompt, the router sent "what database does Meru use to store its index?" to
`direct` at 0.621, and the model made up an answer. A wrong guess costs one
search of about 50 ms, and the model uses only the excerpts that help. "hey
meru, what's the capital of France" searches too, because the assistant shares
its name with the folder. The `route` event, the turn's log line and its span
show `search`, with a debug line that says why; the router's own `meru.route`
span and metric keep what the router chose.

A second rule does the same for tools. `toolTarget` (toolnouns.go) looks for
four signs that a question points at a connected tool, and returns the first
it finds as words for the log line, or `""` for none:

| Sign | Example | Found by |
| --- | --- | --- |
| names a tool server | "search my obsidian vault" | `namesFolder` over `toolServers` |
| asks Meru to remember | "remember that my name is Dana" | `asksToRemember` |
| asks for the web | "search the web: what is SearXNG?" | `asksForWeb` |
| names what a tool handles | "what was the last email I sent?" | `asksAboutToolNoun` |

When it finds one and the route is `direct` or `search`, `withTools` adds the
rest: `direct` becomes `tools` and `search` becomes `search+tools`.
`toolServers` reads the server names from the tool names: `obsidian` from
`obsidian.obsidian_simple_search`, `research` from `a2a.research.summarize`.
It leaves out the built-in tools, whose owner, `meru`, is also the
assistant's name. `Handle` reads the tools on each turn, because the
configure tool can add a server while `merud` runs. In testing, the router
sent "Search my Obsidian vault for notes mentioning 'AI'" to `search`, and
the model, offered no tools, said it couldn't search the vault.

`toolnouns.go` also holds `ConnectedTools`, which tells the router what is
connected before it picks, so it needs these signs less. It reads config, not
the live tool list, so a server that hasn't connected yet still counts. For
each MCP server and A2A agent that allows a tool it writes the name and up to
five nouns, through `describeSource`, which hands the allowed names to
`toolNouns` as `server.tool`, the way dispatch names them:

```go
ConnectedTools(cfg) // ["google (gmail, message, thread, event, drive)", "obsidian (vault)", "git-log", "web search"]
```

Each `[[commands]]` entry adds its name. Of the built-ins only the web goes
in, as "web search", or "web pages" when `web_fetch` is on without SearXNG:
adding "remember" and a second web entry cut the router's accuracy on the
labelled fit set from 0.907 to 0.850. `merud` passes the list to the router;
see [router.md](router.md). The signs above stay: on the labelled set the
tool-server sign still catches the two Obsidian questions the router sends to
`search`, and the tool-noun sign still rescues one email question.

`Handle` keeps what `toolTarget` found in `target` and uses it a second time,
in `aboutFiles`: a `tools` turn with a target isn't a file turn, so it skips
the search.

```go
fileTurn := aboutFiles(dec.Route, target)
```

Before this rule, "search the web for the latest Go release" searched your
folders too. The excerpts crowded the prompt and cost time, and `web_search`
numbers its results from `[1]` as the excerpts are, so a `[1]` meant for a web
page also named the first excerpt, and the Sources list under a web answer
showed one of your files. The file tools stay
on offer on such a turn, so the model can still look at your files when it
has to. `search` and `search+tools` keep their search whatever the question
says: the router, or the folder rule, saw files in it. So "search my obsidian
vault", sent to `search` and moved to `search+tools`, still searches.

Then the search:

```go
if a.searchesFirst(fileTurn) {
    var sources []rpc.Citation
    files, sources, docs, err = a.searchFiles(ctx, searchQuery(question, history))
    ...
    if len(sources) > 0 {
        emit(rpc.Event{Type: rpc.EventSources, Sources: sources})
    }
    files = joinSections(files, a.earlierSection(ctx, searchQuery(question, history), sessionID))
}
memories := a.memorySection(ctx, searchQuery(question, history))
msgs := a.prompt(ctx, history, question, memories, files, a.skillsSection(ctx, picked), len(specs) > 0)
```

- **What it searches for.** No model rewrites the query, so `searchQuery`
  uses the question. A question with at least three words that aren't filler
  (`subjectWords`, `standaloneWords`) is searched alone: it names its own
  subject. "i think i did some work on the bakery site, remind me" has four
  (work, bakery, site, remind). In testing, a question like it about a work
  project, searched together with the question before it about a trip, came
  back with travel papers and no project notes. A shorter question is a follow-up, and `searchQuery` adds one earlier
  question after it, because "and the one after that?" finds nothing alone.
  "how long was the stay?" has two subject words, so it still borrows; the
  filler list also holds words that point back, such as one, other, after and
  those. It walks back from the
  newest and takes the first one that `namesSubject`: a question with at least
  one word that `isFiller` doesn't list. The filler list holds short common
  words and the words people use to retry, such as try, again, search, check,
  last, question, docs, files and notes. So "search again i think it is
  specified", after "try the last question again now", after "what database
  does meru use", searches for `search again i think it is specified` and `what
  database does meru use`. The question goes first: keyword search keeps only a
  query's first 32 words.
- **What the model sees.** `retrieve.Format` numbers the excerpts `[1]`,
  `[2]` and so on, each under a citation line with its file, heading and
  lines. `searchFiles` puts `citeRule` in front of them, which tells the model
  to cite with `[n]`, to cite only the numbers listed, and never to invent a
  source. The whole section joins the end of the system prompt, because some
  chat templates accept a system message only in first place.
- **When it finds nothing.** An empty index, a search with no match, and a
  search that fails all give the model the `noResults` note ("found nothing
  relevant … don't cite any files") and no `sources` event, and the turn
  answers anyway. On a turn that offers `web_search`, `noResultsWeb` takes its
  place: it says to call `web_search` for how to use a program or for anything
  that may have changed, and to answer from memory otherwise. `respond` lists
  the turn's tools before the search to know which note to use.
  `noResults` says "answer from what you know", which would contradict the web
  line in the tools note (below). A failed search is logged as a warning; only a cancelled
  turn stops here.
- **Paths.** `shortPath` writes a file under your home folder as
  `~/notes/garden.md`, for the model and for the client. Other paths stay
  whole. Before it shortens them, `searchFiles` keeps each file's full path
  once, in the order the results rank them, and returns that list as `docs`
  for the transcript. A shortened path would read differently on a machine
  with another home folder.
- **The sources event** lists every excerpt the model got, numbered as the
  prompt numbers them, with its heading, lines or page, and score. It goes out
  before the first token, so a client can show it however it likes; `meru`
  and `meru chat` show the ones the answer cites (see `rpc.Cited`).
- **The metric.** `meru.context.tokens` with `section = "chunks"` records the
  section's size, estimated as characters divided by four.
- **The numbers go on.** `Handle` keeps the sources in `t.sources` and their
  count in `t.cites`, so excerpts that `search_files` returns later in the turn
  number from 11 after ten up-front ones (see the tool rounds below).

**Agentic retrieval.** `searchesFirst(fileTurn)` is `fileTurn`, a
Searcher, and `!a.agentic`. With `[index] retrieval = "agentic"` it is false
on every route, so the turn has no file excerpts, no `noResults` note, no
up-front `sources` event and no earlier conversations, which hang off the same
`if`. The profile and the recalled memories stay: they cost one embedding, and
they are about you, which no file tool can find. The routes still offer what
they offered, so `search` gets the four file tools, and on a file turn
`exploreNote` tells the model to use them (see `fileToolsNoteFor` below). The folder rule still turns a
`direct` question that names an indexed folder into `search`, which is now how
that question gets the file tools. `ARCHITECTURE.md`, "Retrieval", has the
numbers that compare the two modes.

### Earlier conversations (earlier.go)

On the same turns, right after the file search, one line in `Handle` adds past
sessions to the prompt (v0.4):

```go
files = joinSections(files, a.earlierSection(ctx, searchQuery(question, history), sessionID))
```

`earlierSection` asks the Searcher for up to three past sessions that match
the same query the file search used, leaving out the session asking. It hands
them to `formatEarlier`, which writes one line per session under the header
"From earlier conversations:":

```text
- 2026-09-17 (7 days ago): The user chose two raised beds for the garden. The user said: "how many raised beds should the garden have?"
```

- **The date** is the day the session started, with how many days ago that
  was. A small model can't work out "last week" from a date alone, and the
  prompt holds no "today" to count from.
- **The summary** comes from the session's newest `summary` line (see
  [summarize](summarize.md)). A session with no summary yet shows only its
  matching message.
- **The matching message** is the question or answer that best matched by
  keyword: "The user said" for a question, "You said" for Meru's own answer,
  since the system prompt calls the model "you".
- **The cap.** The section stays within 2,400 characters, about 600 tokens.
  Each summary is cut to 400 characters and each message to 300, so one session
  can't take the whole budget, and a line that would pass the cap is left out
  with every line after it.
- **No numbers.** These aren't files, so they get no citation number and no
  `sources` event, and the header tells the model not to cite them.
- **Failure.** A failed recall logs a warning and the turn goes on without the
  section, as a failed file search does.
- **The metric.** `meru.context.tokens` with `section = "sessions"`.

`joinSections` joins two parts of the system prompt with a blank line and
leaves out an empty one. Adding the section to `files` keeps `prompt` as it
was: the section lands after the file excerpts, at the end of the system
prompt.

### answer

`answer` streams one round of the main model's reply:

```go
stream, err := a.engine.Stream(ctx, msgs, tools, engine.Options{Model: model, MaxTokens: a.maxTokens})
...
for delta, err := range stream {
    if err != nil {
        return fail(err)
    }
    if delta.Text != "" {
        if ttft == 0 {
            firstToken = time.Now()
            ttft = firstToken.Sub(start)
        }
        text.WriteString(delta.Text)
        if err := emit(rpc.Event{Type: rpc.EventToken, Text: delta.Text}); err != nil { ... }
    }
    calls = append(calls, delta.ToolCalls...)
    if delta.Done {
        usage = delta.Usage
    }
}
```

- `tools` holds the schemas to offer, or `nil` for none. Ollama sends each
  tool call whole, in a chunk of its own, so `answer` collects them with one
  `append` and never joins pieces.
- `ttft` (time to first token) feeds the v0.1 target "first token in under a
  second".
- The last delta carries Ollama's token counts, which go into the transcript
  line and the metrics. Meru never estimates them.
- If `emit` fails, the client has gone, and the turn stops.
- The first piece of text adds a `first_token` event to the span, with
  `meru.ttft_ms`. At the end, `obs.ChatResult` puts the token counts and
  Ollama's timings on the span.

`answer` returns a small `reply` struct: the text, the usage counters, and
`firstToken`, the moment the first text arrived.

The assistant line holds the answer and the turn's facts, so the `turns`
table can rebuild from the transcript:

```go
answer := transcript.Line{
    Type: transcript.TypeAssistant, Text: rep.text,
    TokensIn: rep.usage.PromptTokens, TokensOut: rep.usage.OutputTokens,
    Route: route, Ms: time.Since(start).Milliseconds(), Sources: docs, ...
}
```

`Route` is the route after the override rules, the one the `route` event
showed. `Ms` counts from `start`, when `merud` received the question. `Sources`
is the `docs` list from `searchFiles`, empty on a turn that didn't search. The
user line carries `start` as its time too, so a row rebuilt from the file gets
the same time as the row written live.

Then `recordUsage` writes the `turns` row through the TurnRecorder and records
`meru.turn.tokens` and `meru.turn.docs`. A failed insert logs a warning and the
turn goes on: the transcript already holds the turn, and a rebuilt `meru.db`
brings the row back. When `Handle` starts a new session it also adds one to
`meru.sessions`.

Once the assistant line is in the transcript, `Handle` sends a last event
built by `doneEvent`:

```go
return emit(doneEvent(start, rep))
```

`doneEvent` measures time to first token and total time from `start`, when
`merud` received the question, so both include routing. That is the wait the
person at the terminal sees. It adds the token counts and Ollama's
`eval_duration` (the model's own writing time), which `meru chat` shows under
the answer. The rpc server holds this `done` back and sends it last, or drops
it if `Handle` fails.

The `route` event also says whether the router fell back: `Fallback` is true
for any outcome but `ok`, and `meru chat` draws such a route in amber.

### The tool rounds (tools.go)

**Refreshing first.** `merud` tries each MCP server once at startup and never
retries or re-lists on a timer (ARCHITECTURE.md, "MCP"). A server the user starts
later, or a stdio child that crashed, comes back on the next turn that offers
tools, and a server the user restarted with new tools offers them on that turn.
`Handle` does that right after it picks the tools:

```go
specs := a.toolSpecs(dec.Route)
if len(specs) > 0 {
    a.tools.Refresh(ctx)
    specs = a.toolSpecs(dec.Route)
}
```

The second `toolSpecs` call lists the tools again, so a server that answers now,
or one whose tool list changed, joins this turn with its current tools. One that still fails is left out, and the model answers without
it. `len(specs) > 0` is the whole test: any turn that offers a tool asks, a `search`
turn with only file tools included, and a `direct` turn never does.
`Refresh` runs once per turn, before the first round, and never between
rounds.

**Which turns offer tools.** `toolSpecs(route)` returns every schema the
ToolRunner offers on `tools` and `search+tools`. On `search` it keeps
`datetime`, the four read-only file tools, `read_file`, `list_folder`, `grep`
and `search_files`, which `builtin.IsFileTool` names, and the local commands
(`cmd.<name>`) for which `Asks` says no:

```go
case "search":
    var specs []engine.ToolSpec
    for _, s := range a.tools.Tools() {
        if s.Name == builtin.DateTime || builtin.IsFileTool(s.Name) ||
            (toolKind(s.Name) == dispatch.KindCommand && !a.tools.Asks(s.Name)) {
            specs = append(specs, s)
        }
    }
    return specs
```

Ten excerpts can't cover "everything in my work folder", and the file tools
read nothing search couldn't. The commands are there because "what
changed in the meru repo this week?" lands on `search` when `meru` is an
indexed folder, and a declared `git log` answers it. A command with
`confirm = true` changes something, so it waits for a tools route. On
`direct`, or when the ToolRunner is `nil`, `toolSpecs` returns `nil`. A model
can't call a tool it hasn't seen, and the prompt stays shorter.

Two functions pick the notes `prompt` adds to the system prompt.
`noteFor(specs)` gives `toolsNote` to a turn that offers any tool but the file
tools and commands, `datetime` included: the model may call the tools, and
some calls ask you first. A turn whose tools are only file tools and
commands gets `commandsNote` when it has commands (it may run the `cmd.`
tools). A turn with no tools gets none.

When the turn offers both `web_search` and a file tool, `noteFor` adds
`webFallbackNote` after `toolsNote`:

```text
When the user's files don't answer the question, because a search, grep or read found nothing on it, call web_search before you answer from memory. Never make up a command's flags or options, or a version number: look them up, or say you don't know.
```

A real turn asked "help me understand btop with some simple commands". The
model grepped the user's folders, found only pages that mention btop in
passing, never called `web_search`, and answered with flags btop doesn't have.
The note depends only on which tools the turn offers, so it stays the same
from one such turn to the next and keeps its place among the parts Ollama
reuses. `web_fetch` alone doesn't bring it: with no search the model would
have to guess a URL, and `web_fetch` asks you before it fetches a URL no
search result gave. On the `lite` model the note alone moved little: over 12
turns with `file-research` loaded, the model called the web once with the note
and once without. The sharper skill descriptions did more, because they keep
`file-research` off such questions (see "Scoring the pick").

`a.fileToolsNoteFor(specs, fileTurn)` gives the note on the file tools, and
only to a file turn that offers them. In `auto` mode that is `fileToolsNote`:
when the excerpts aren't enough, the model may read whole files, list
folders, grep and search again. With agentic retrieval it is `exploreNote`:
look first with `search_files` or `grep`, then `read_file` what matters, stop
after two or three rounds, and cite `search_files`' excerpts by number. It
carries the citing rule that `citeRule` carries when excerpts sit in the
prompt. A web or mail question gets neither, even though its turn offers the
file tools: its prompt stays shorter, and the model isn't told to go looking
in your folders for an answer that lives elsewhere. `prompt` puts this note
after the list of skills, so a file turn and any other turn share the whole
opening of the prompt up to it.

A Go `switch` runs only the first case that matches and never falls through
to the next. `noteFor` walks the tools once and sets a flag for each kind it
sees, then a second `switch` with no value after the keyword picks the note:
each `case` holds a true-or-false test, and the first true one wins, so the
web line comes before the plain `toolsNote`.

`Handle` also records the schemas' size, characters divided by four, as
`meru.context.tokens` with `section = "tools"`.

**The loop.** `converse` runs the rounds:

```go
for {
    t.rounds++
    offer := specs
    if t.rounds >= a.maxRounds {
        offer = nil // the last round: answer with what you have
    }
    rep, err := a.answer(ctx, msgs, offer, t.emit)
    ...
    if len(rep.calls) == 0 || len(offer) == 0 {
        total.text = rep.text
        return total, nil
    }
    msgs = append(msgs, engine.Message{Role: engine.RoleAssistant, Content: rep.text, ToolCalls: rep.calls})
    results, err := a.runTools(ctx, t, rep.calls)
    ...
    msgs = append(msgs, results...)
}
```

- **The cap.** `[agent] max_rounds` (default 8) caps model calls per turn.
  The last round offers no tools, so the model has to answer. A model that
  calls a tool it wasn't offered gets no call run; that round is its answer.
- **Repeats.** `runRound` sits between `converse` and `runTools`. It gives
  each call a key, `callKey`: the tool's name and its arguments decoded and
  encoded again, which sorts the JSON keys, so `{"a":1, "b":2}` and
  `{"b":2,"a":1}` match. A call whose key the turn has seen doesn't run: its
  message holds the earlier result from `t.seen` and a note saying so. After
  `maxRepeats` (2) repeats, `converse` stops offering tools, so the next round
  must answer. The btop turn now runs `datetime` once, hands back the same time
  twice with the notes, and answers in the fourth round instead of the ninth.
- **Why a repeat skips dispatch.** AGENTS.md says every tool call goes through
  `dispatch`. A repeat runs nothing, so it isn't a call: it gets no
  `tool_call` event, no transcript line and no `tool_calls` row. Sending it
  through `dispatch` would log a second call that did no new work, and ask
  you again for a call that asks first. The transcript has no line type for a
  note, so a repeat shows up only in a debug log line (`tool call repeated`)
  and in the turn span's `meru.turn.repeated_calls` count.
- **What the model reads next round.** Its own message with the calls, then
  one `RoleTool` message per call, in call order, with `ToolName` set to the
  tool's full name and `Content` set to `Result.Text`. A denied or declined
  call reaches the model the same way, so it can answer without the tool or
  ask you what to do.
- **The stats.** `reply.add` sums each round's token counts and durations,
  and keeps the turn's first text token for time to first token. The
  transcript's assistant line holds the final round's text and the summed
  counts. `reply.add` has a pointer receiver (`r *reply`), so it changes the
  caller's `reply` in place.
- **`turn`** is a small struct that carries what the rounds need: the
  session, source, trace ID, `emit`, `approve`, three counters, `rounds`,
  `calls` and `repeats`, the `seen` map of results by call key, and the turn's sources with `cites`, the count of citation
  numbers handed out. `Handle` reads `t.rounds` in its deferred function, so a
  failed turn still reports how many rounds it ran.

**Running the calls.** `runTools` does one round's calls:

1. It gives each call an ID, `call-1`, `call-2` and so on across the turn,
   or the engine's own ID when Ollama sent one, and emits a `tool_call`
   event with the name, the kind and the arguments. `toolKind` reads the
   kind from the name: `a2a.` in front means an A2A agent, `cmd.` a local
   command, any other dot an MCP server, and no dot a built-in tool.
2. It starts every call at once in an `errgroup`, each with a
   `dispatch.Call` holding the ID, name, arguments (`{}` when the model sent
   none), session ID, source, trace ID, `approve`, `Append` and `Question`.
   `Append` writes to this session's transcript through `appendLine`, so
   dispatch's tool lines get the same span and debug line as the agent's own.
   `Question` is `t.question`, which `Handle` fills once per turn with
   `userWords`: the question, then the user's earlier questions from the
   history, one per line. It holds only what the user typed, never the
   excerpts, memories or tool results, because `web_fetch`'s guard trusts the
   URLs in it (see [builtin](builtin.md)).
3. Each goroutine writes its result into its own slot of a slice, so the
   results come out in call order with no lock, and emits its `tool_result`
   event (outcome, milliseconds and any `Sources`) as soon as it ends. A
   quick call reports before a slow one.
4. Once every call has ended, `addSources` adds the excerpts the calls
   returned to `t.sources`, sorts them by number, and emits a `sources` event
   with all of them. The clients keep the last `sources` event, so their
   `Sources:` list and `rpc.Cited` cover what the model found through
   `search_files`, and `meru check`'s `sources_any` sees those paths.

```go
g, gctx := errgroup.WithContext(ctx)
gctx = dispatch.WithCiteNumbers(gctx, t.nextCites)
for i, c := range calls {
    g.Go(func() error {
        res, outcome := a.tools.Dispatch(gctx, dispatch.Call{...})
        out[i] = engine.Message{Role: engine.RoleTool, ToolName: c.Name, Content: res.Text}
        return t.emit(rpc.Event{Type: rpc.EventToolResult, ...})
    })
}
if err := g.Wait(); err != nil {
    return nil, err
}
```

`g.Wait` waits for every goroutine, so none outlives the turn. When one
returns an error (only `emit` can fail, when the client has gone), `gctx`
ends and the other calls stop.

**Citation numbers.** `search_files` numbers its excerpts, and those numbers
must follow the prompt's and not clash with another call's in the same round.
The agent puts `t.nextCites` on `gctx` with `dispatch.WithCiteNumbers`, and the
tool calls `dispatch.CiteNumbers(ctx, n)`, which runs it:

```go
func (t *turn) nextCites(n int) int {
    t.mu.Lock()
    defer t.mu.Unlock()
    first := t.cites + 1
    t.cites += n
    return first
}
```

Two searches in one round run in two goroutines, so the counter takes a lock.
Each gets a block of numbers that doesn't overlap the other's, in the order
they asked. The model reads those numbers in the tool results and cites them;
`addSources` then sorts the list by number for the client.

**Events from several goroutines.** While calls run, `emit` runs from
several goroutines at once, and dispatch may call `approve` from them too.
The rpc server's `emit` takes a lock around each write, so that is safe; the
tests' collector takes a lock too.

### Cancellation

The turn's own deadline ends `tctx` and gets an answer (see Handle). When the
client hangs up, the rpc server cancels `ctx`. The engine's stream
ends, `answer` returns `ctx.Err()`, and `Handle` returns without writing an
assistant line or a `turns` row. The user line stays in the file, and `History` leaves an
unanswered question out of later prompts.

A hang-up during a tool call works the same way. `gctx` ends, dispatch
records each open call as `cancelled`, and `runTools` returns `ctx.Err()`
once every call has returned. The model isn't called again.

### The transcript

The agent writes two lines per turn: the question and the final answer. The
answer line carries `outcome` only on a turn that ended without a full answer.
Dispatch writes the tool lines (`tool_call`, `approval`, `tool_result`)
between them. `transcript.History` reads only user and assistant lines, so
earlier tool results stay out of later prompts: the answer already holds
what mattered from them.

### What it logs

At info level, one `turn` line per turn in `merud.log`: session ID, route,
source, outcome, total milliseconds, `ttft_ms`, token counts, the trace ID, and
the error when there is one. At debug level each stage adds a line: `turn
started`, `session created` or `session opened`, `history loaded`,
`transcript appended` (once per line, dispatch's tool lines included),
`route` (from the router), `skills picked` (with the names and the time),
`search done` (on search routes, with the result count, the section's size
and the time), `prompt built`, and per round `answer finished` (with its
`tool_calls` count) and `round finished` (round number, tool calls and
milliseconds).

Every line goes through `a.log.DebugContext(ctx, ...)` or `InfoContext`, so the
log handler from `obs` adds the turn's `trace_id`. The lines carry lengths
(`question_chars`, `answer_chars`) and never the text. With
`capture_content = true`, `turn started` and `answer finished` add the first
200 characters, and the `meru.turn` span gets the whole question and answer.

## Go ideas used here

- **Interfaces** — `Router`, `Searcher`, `ToolRunner`, `TurnRecorder`, and
  `engine.Engine`.
- **`errgroup`** — runs a round's tool calls at the same time and waits for
  them all, and runs the route and the skill pick side by side. More in
  [go-basics/goroutines.md](go-basics/goroutines.md).
- **Embedding a struct** — the test's `pickEngine` holds a `*fakeEngine`
  without a field name, so it gets `fakeEngine`'s methods and replaces only
  `Generate`.
- **Pointer receivers** — `reply.add` changes the reply it is called on.
- **Named results with `defer`** — record the outcome once, whatever path
  returns. More in [go-basics/defer.md](go-basics/defer.md).
- **`context`** — one context runs through the whole turn and stops it. More in
  [go-basics/context.md](go-basics/context.md).
- **Wrapped errors** — `fmt.Errorf("main model %s: %w", model, err)`. More in
  [go-basics/errors.md](go-basics/errors.md).
- **`strings.Builder`** — collects the answer without copying it on every piece.
- **Range over a function** — `for delta, err := range stream`.
- **A function as an argument** — `words` hands `strings.FieldsFunc` a small
  function that says where to split: at any rune that isn't a letter, a digit
  or a hyphen, so "personal-knowledge-base" stays one word.
- **`switch` with a list of cases** — `isFiller` lists its words in one `case`,
  and the switch returns true when `w` matches any of them.

## Try it

```sh
go test -race ./internal/agent/...
```

`search_test.go` checks the search step: the search routes and `tools` add
the excerpts and send `sources` before the tokens, `direct` doesn't search
unless the question names an indexed folder, an empty or failed search still
answers, and `TestSearchQuery` checks which earlier question joins the query.
`TestFilesNote` checks the folders reach the system prompt, and
`TestFolderNames` checks the names the folder rule matches.

`tools_test.go` checks the tool rounds with `fakeTools` and a `fakeEngine`
that scripts one reply per round: which routes offer tools, the event order
(`session`, `route`, `sources`, `tool_call`, `tool_result`, tokens, `done`),
two calls that must run at the same time (one waits on a channel the other
closes) with results in call order, the round cap, denied and declined calls
reaching the model, `approve` and a job's source reaching dispatch, a hang-up
during a call, one `gen_ai.chat` span per round, and a transcript and
history that hold only the question and the answer. `TestToolsOfferedByRoute`
checks which tools and which note each route gets.
`TestTurnConnectsMissingServersOnce` gives `fakeTools` a tool that appears only
after `Refresh`: a `tools` or `search+tools` turn calls it once and offers
the new tool in the same turn, and a `direct` turn never calls it.

`files_test.go` runs a `search` turn over the real built-in tools behind a real
`dispatch.Dispatcher`: the fake engine calls `read_file`, and the next round
must read the file's whole text.

`agentic_test.go` runs turns over the real built-in tools, with a fake
searcher behind `search_files`. `TestAgenticTurnSkipsSearchFirst` sets
`[index] retrieval = "agentic"` and, on `search`, `search+tools` and `tools`,
checks that the turn searched neither files nor past sessions, that the prompt
holds `exploreNote` and the recalled memory but no excerpts, that the four file
tools are on offer, and that the one `sources` event comes from the tool,
numbered from 1. `TestToolSourcesNumberAfterThePrompts` runs an `auto` turn
with two up-front excerpts whose model calls `search_files` twice in one round:
the tools' excerpts must be `[3]` to `[6]` with no overlap, the last `sources`
event must hold all six in order, and `rpc.Cited` must find `[3]` in the
answer. `TestNoteFor` pins which note each mix of tools gets in each mode.

`TestEndToEnd` starts the real socket server with this agent over a fake
engine, asks a question with the real client and checks the streamed answer and
the transcript file. `TestEndToEndToolRound` does the same with the real
`OllamaEngine` against the fake Ollama, which answers the first chat request
with a tool call and the second with text. It checks the events and what the
second request sent Ollama: the call, its result and the tool schema.
`TestTurnReadsASavedAttachment` runs the same kind of turn through a real
`dispatch.Dispatcher` and the built-in tools. A fake `google` tool saves a PDF
in the attachments folder and names it; the test checks that the model called
that one tool, never `read_file`, and that the second request's tool message
held the PDF's pages (see [builtin](builtin.md), attachments.go).

`profile_test.go` checks `formatProfile` (order, one line per fact, the cap
keeping the newest, empty), where the section sits in the system prompt, that
an empty or unreadable profile leaves the header out without failing the
turn, and which questions the remember rule gives tools.

`recall_test.go` checks `formatMemories` (order, the kind, one line per fact,
the cap), where the section sits on each route and that a failed recall leaves
it out. `TestRecallAcrossSessions` is the v0.4 "Done when" for recall. It builds
the real remember tool, dispatcher, memory folder, store and syncer, with a fake
embedding model that puts "manager" and "boss" on one axis. In one session the
model saves "Sam Lee is the user's manager". The test moves the memory's created
date back a week and holds six unrelated memories, so recency alone would leave
Sam out of the five. In a new session, "Draft a note to my boss about launch
slipping", which shares no word with the memory, brings Sam back into the prompt
by meaning.

`earlier_test.go` checks `formatEarlier` (dates, "The user said" and "You
said", the cap), which routes add the section, that the asking session is left
out, and that a failed recall still answers. `TestRecallsLastWeekWithoutAReminder`
is the v0.4 "Done when" line: it writes a garden-beds session dated seven
days ago and one about the library, lets the real summarizer summarize both over a
fake model, and asks "what did we decide about the garden beds?" in a new
session. The prompt's first recalled line must read "7 days ago" and hold the
garden summary. It uses a real store and `retrieve.SearchSessions`, with a
bag-of-words fake embedding.

`skills_test.go` checks the skills step with `pickEngine`, a fake whose
`Generate` plays the fast model. `TestPickWritingForEmail` asks "write a short
email to my landlord" over the real built-in skills and checks the pick call's
options, that the `route` event names `writing`, and that the list, the header
and the writing body land in the system prompt in that order. Other tests
cover a pick of "none", no skills or an empty folder (no pick call at all), a
failed pick that still answers, a failed route, `parsePick` and the cap in
`formatBodies`.

`skills_integration_test.go` runs the pick against the real local Ollama:

```sh
go test -tags integration -v -run Integration ./internal/agent/
make pick-eval   # TestPickEval: 21 labelled questions, three asks each
```

`pickeval_test.go` holds `loadPicks`, `pickRight` and two plain tests, so a
broken fixture or scoring rule fails `make test` without Ollama.

`usage_test.go` checks what a turn keeps for `meru usage`: the assistant
line's route (after the override rules), duration and full source paths, each
file once; the row the TurnRecorder gets, with its time equal to the user
line's; a failed insert that leaves the answer alone; and no row for a turn
that failed.

`observe_test.go` runs turns through the socket server, the agent and the real
router over a fake engine. It records spans with `tracetest.SpanRecorder` and
checks the tree (`rpc.request` → `meru.turn` → one span per stage), the key
attributes and the `first_token` event. It logs into a buffer and checks each
debug line, the trace ID on every line, and that neither the spans nor the log
hold the question or answer until `capture_content` is on.

## Why it's built this way

- **No agent framework.** The loop is short, and the rules Meru enforces
  (audit, budgets, approvals) must live in code we can read.
- **The agent returns errors; the server sends them.** The agent never writes
  to the socket itself, so the same code can later serve scheduled jobs.
- **Route recorded from day one.** Route metrics collected since v0.1 give
  search (v0.2) and tools (v0.3) real numbers to test against.
- **A failed search doesn't fail the turn.** The excerpts help the answer, but
  the model can still answer without them, and the note in the prompt stops it
  from pretending it looked. A failed recall works the same way.
- **Recall on every route.** It costs one embedding of the question and three
  small queries, about 10 ms against the local Ollama, well under a model call.
- **Sources before the answer.** The client learns what the model read while
  the answer streams, and picks which to show once it has the whole text. A
  tool's excerpts come in a later `sources` event that repeats the whole list,
  so a client needs no new logic: it keeps the last list it saw.
- **Both retrieval modes, one default.** `auto` searches before the answer and
  offers `search_files` too; `agentic` leaves the search to the model. The
  owner's check set favoured `auto` on the 2B model (see ARCHITECTURE.md,
  "Retrieval"), so `auto` stays the default and `agentic` is a setting.
- **The agent never runs a tool itself.** It hands every call to the
  ToolRunner, so dispatch stays the one path that checks allowlists, asks
  you, and logs each call.
- **Calls in a round run at the same time.** The model asked for them
  together, so none needs another's result, and a slow MCP server doesn't
  hold up a quick built-in.
- **A separate pick call, not a longer router.** The router reads one token's
  probabilities and can't name skills. A second short call keeps the router's
  prompt and its calibration as they are, and running both at once costs no
  extra time.
