# agent

**Code:** `internal/agent/` (`doc.go`, `agent.go`, `agent_test.go`, `search_test.go`, `observe_test.go`)
**Milestone:** v0.1; search in v0.2
**Architecture:** [Agent loop](../../ARCHITECTURE.md#agent-loop), [A question, end to end](../../ARCHITECTURE.md#a-question-end-to-end), [Retrieval](../../ARCHITECTURE.md#retrieval)

## What it does

`agent` runs one turn: the work between "a question arrived" and "the answer
is saved". `merud` hands `Agent.Handle` to the socket server, and the server
calls it once per question.

A turn makes one model call. The router picks a route, and the route decides
whether the turn looks in your files first:

| Route | Searches your files? |
| --- | --- |
| `direct` | no |
| `search` | yes |
| `tools` | no (tools arrive in v0.3; until then it answers directly) |
| `search+tools` | yes |

## The picture

```mermaid
sequenceDiagram
    participant S as rpc server
    participant A as Agent.Handle
    participant T as transcript
    participant R as Router
    participant E as Engine (main)
    S->>A: request
    A->>T: New or Open session
    A->>T: History(history_turns)
    A-->>S: emit session
    A->>T: Append user line
    A->>R: Decide(question, history)
    A-->>S: emit route
    opt route is search or search+tools
        A->>A: Searcher.Search(question)
        A-->>S: emit sources (when it found some)
    end
    A->>E: Stream(system + excerpts + history + question)
    loop each piece
        E-->>A: delta
        A-->>S: emit token
    end
    A->>T: Append assistant line (tokens in/out)
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
}
```

The same trick as `Router`: the agent names the one method it needs. `merud`
passes `searchAdapter`, which calls `retrieve.Search` over the store; tests pass
`fakeSearcher`, which returns fixed results. A `nil` Searcher turns search off,
which is what most of the older tests pass.

### Handle

`Handle` has the signature of `rpc.Handler`, so `merud` passes `a.Handle`
straight to `rpc.Serve`. Its steps follow the diagram, and each is a short
method that opens its own span under `meru.turn` and writes one debug line:
`openSession` (`meru.session`), `appendLine` (`meru.transcript.append`),
`route` (the router's `meru.route`), `searchFiles` (`meru.search`, with
retrieval's `meru.retrieve` under it), `prompt` (`meru.prompt`) and `answer`
(`gen_ai.chat`). Two details:

**The turn span and metrics are recorded in one deferred function.**

```go
func (a *Agent) Handle(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) (err error) {
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
`ok`, `error` or `cancelled`. `context.WithoutCancel` keeps the trace but drops
the cancel, so a cancelled turn still gets its metric. The same function
writes the turn's one info line, `logTurn`.

**History is read before the question is written**, so the new question doesn't
show up twice in the prompt.

### searchFiles

On the two search routes, `Handle` calls `searchFiles` between routing and the
prompt:

```go
if searches(dec.Route) && a.search != nil {
    var sources []rpc.Citation
    files, sources, err = a.searchFiles(ctx, searchQuery(question, history))
    ...
    if len(sources) > 0 {
        emit(rpc.Event{Type: rpc.EventSources, Sources: sources})
    }
}
msgs := a.prompt(ctx, history, question, files)
```

- **What it searches for.** The router doesn't rewrite queries yet, so
  `searchQuery` uses the question. On a follow-up it adds the session's last
  question after it, because "and the one after that?" finds nothing alone.
  The question goes first: keyword search keeps only a query's first 32 words.
- **What the model sees.** `retrieve.Format` numbers the excerpts `[1]`,
  `[2]` and so on, each under a citation line with its file, heading and
  lines. `searchFiles` puts `citeRule` in front of them, which tells the model
  to cite with `[n]`, to cite only the numbers listed, and never to invent a
  source. The whole section joins the end of the system prompt, because some
  chat templates accept a system message only in first place.
- **When it finds nothing.** An empty index, a search with no match, and a
  search that fails all give the model the `noResults` note ("found nothing
  relevant … don't cite any files") and no `sources` event, and the turn
  answers anyway. A failed search is logged as a warning; only a cancelled
  turn stops here.
- **Paths.** `shortPath` writes a file under your home folder as
  `~/notes/garden.md`, for the model and for the client. Other paths stay
  whole.
- **The sources event** lists every excerpt the model got, numbered as the
  prompt numbers them, with its heading, lines or page, and score. It goes out
  before the first token, so a client can show it however it likes; `meru`
  and `meru chat` show the ones the answer cites (see `rpc.Cited`).
- **The metric.** `meru.context.tokens` with `section = "chunks"` records the
  section's size, estimated as characters divided by four.

### answer

`answer` streams the main model's reply:

```go
stream, err := a.engine.Stream(ctx, msgs, nil, engine.Options{Model: model})
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
    if delta.Done {
        usage = delta.Usage
    }
}
```

- `nil` for tools: v0.1 sends the model no tool schemas.
- `ttft` (time to first token) feeds the v0.1 target "first token in under a
  second".
- The last delta carries Ollama's token counts, which go into the transcript
  line and the metrics. Meru never estimates them.
- If `emit` fails, the client has gone, and the turn stops.
- The first piece of text adds a `first_token` event to the span, with
  `meru.ttft_ms`. At the end, `obs.ChatResult` puts the token counts and
  Ollama's timings on the span.

`answer` returns a small `reply` struct: the text, the usage counters, and
`firstToken`, the moment the first text arrived. Once the assistant line is in
the transcript, `Handle` sends a last event built by `doneEvent`:

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

### Cancellation

When the client hangs up, the rpc server cancels `ctx`. The engine's stream
ends, `answer` returns `ctx.Err()`, and `Handle` returns without writing an
assistant line. The user line stays in the file, and `History` leaves an
unanswered question out of later prompts.

### What it logs

At info level, one `turn` line per turn in `merud.log`: session ID, route,
source, outcome, total milliseconds, `ttft_ms`, token counts, the trace ID, and
the error when there is one. At debug level each stage adds a line: `turn
started`, `session created` or `session opened`, `history loaded`,
`transcript appended` (twice), `search done` (on search routes, with the
result count, the section's size and the time), `prompt built` and `answer
finished`.

Every line goes through `a.log.DebugContext(ctx, ...)` or `InfoContext`, so the
log handler from `obs` adds the turn's `trace_id`. The lines carry lengths
(`question_chars`, `answer_chars`) and never the text. With
`capture_content = true`, `turn started` and `answer finished` add the first
200 characters, and the `meru.turn` span gets the whole question and answer.

## Go ideas used here

- **Interfaces** — `Router`, `Searcher`, and `engine.Engine`.
- **Named results with `defer`** — record the outcome once, whatever path
  returns. More in [go-basics/defer.md](go-basics/defer.md).
- **`context`** — one context runs through the whole turn and stops it. More in
  [go-basics/context.md](go-basics/context.md).
- **Wrapped errors** — `fmt.Errorf("main model %s: %w", model, err)`. More in
  [go-basics/errors.md](go-basics/errors.md).
- **`strings.Builder`** — collects the answer without copying it on every piece.
- **Range over a function** — `for delta, err := range stream`.

## Try it

```sh
go test -race ./internal/agent/...
```

`search_test.go` checks the search step: the two search routes add the
excerpts and send `sources` before the tokens, `direct` and `tools` never
search, an empty or failed search still answers, and a follow-up searches with
the last question too.

`TestEndToEnd` starts the real socket server with this agent over a fake
engine, asks a question with the real client and checks the streamed answer and
the transcript file.

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
  from pretending it looked.
- **Sources before the answer.** The client learns what the model read while
  the answer streams, and picks which to show once it has the whole text.
