# Meru low-level design, level 100: how the code fits together

This page is a map of Meru's code for someone new to Go. It shows how the code is
organized, the few interfaces that hold it together, and the path one question
takes through the functions, so you know which file to open next.
[ARCHITECTURE.md](../ARCHITECTURE.md) explains *why* Meru is built this way; this
page explains *where* each part lives. It describes v0.2: the v0.1 question path,
plus the store, the indexer and hybrid search, and the groundwork packages for
tools, skills and memory.

You need three Go ideas to follow it:

- A **package** is a folder of `.go` files that share a name, such as
  `internal/engine`. Code in one package uses another by importing it, then writes
  `engine.Message` or `rpc.Do(...)`.
  ([more](coding-notes/go-basics/packages-and-imports.md))
- A **struct** is a type with named fields, such as a `Config` with a `Profile`
  field. A **method** is a function attached to a type: `func (s *Session) Append(...)`
  can be called as `sess.Append(...)`.
- An **interface** lists methods and nothing else. Any type that has those methods
  satisfies it, without saying so. Meru uses interfaces at the seams between
  packages, so each side can be tested with a fake.
  ([more](coding-notes/go-basics/interfaces.md))

## 1. Two programs and their packages

Meru builds two programs from `cmd/`. Everything else is a package under
`internal/`, which Go allows only code inside this repo to import.

| Package | What it does | Start reading at |
| --- | --- | --- |
| `cmd/merud` | the daemon: starts everything, serves questions and the index ops | `main.go`: `main`, `run`, `serve`, then `index.go` |
| `cmd/meru` | the client you type into | `main.go`: `run`, `ask`, `ping`, then `index.go` |
| `internal/config` | reads and checks `~/.meru/config.toml` | `load.go`: `Load` |
| `internal/engine` | the `Engine` interface and the Ollama client | `engine.go`, then `ollama.go` |
| `internal/router` | picks a route from one token's probabilities | `router.go`: `Decide` |
| `internal/store` | `meru.db`: documents, chunks, vectors and the keyword index | `store.go`: `Open`, then `documents.go` and `search.go` |
| `internal/retrieve` | hybrid search: vector and keyword, merged by reciprocal-rank fusion | `search.go`: `Search`, then `rrf.go` and `format.go` |
| `internal/index` | reads `[index] folders` into the store: skip rules, chunking, watching | `indexer.go`: `Scan`, then `skip.go` and `watch.go` |
| `internal/agent` | runs one turn, from question to answer | `agent.go`: `Handle` |
| `internal/transcript` | reads and writes session files (JSONL) | `transcript.go`: `New`, `Append`, `History` |
| `internal/rpc` | the socket protocol between `meru` and `merud` | `protocol.go`, then `client.go` and `server.go` |
| `internal/obs` | OpenTelemetry metrics and traces | `obs.go` |
| `internal/tui` | the `meru chat` screen (Bubble Tea, Lip Gloss, Glamour) | `run.go`: `Run`, then `model.go` and `view.go` |
| `internal/loopback` | the rule "this address is on this machine" | `loopback.go`: `CheckURL` |
| `internal/mcp` | the MCP client pool: starts or connects to servers, keeps allowed tools (v0.3 groundwork) | `pool.go`: `NewPool`, then `call.go` |
| `internal/skills` | loads `SKILL.md` folders and installs the built-in skills (v0.4 groundwork) | `skills.go`: `Load`, then `builtin.go` |
| `internal/memory` | one Markdown file per memory under `memory/<kind>/` (v0.4 groundwork) | `memory.go`: `Open`, `Add`, `List` |

Three more packages exist only for testing: `internal/policy` (tests that enforce
Meru's rules), `internal/testutil/fakeollama` and `cmd/fakeollama` (a fake Ollama
server), and `test/e2e` (tests that run the real programs).

## 2. Who imports whom

An arrow means "imports". Go refuses to compile a loop of imports, so this picture
is also the order to learn the packages in: start at the bottom.

```mermaid
flowchart TD
    merud["cmd/merud"] --> agent & router & rpc & obs & engine & config
    merud --> index & retrieve & store
    meru["cmd/meru"] --> tui & rpc & config
    tui --> rpc
    agent --> transcript & engine & rpc & obs & config & retrieve
    router --> engine & obs & config
    index --> store & engine & obs & config
    retrieve --> store & engine & obs
    store --> engine
    mcp --> engine & obs & loopback
    transcript --> engine
    rpc --> obs
    obs --> config & loopback
    config --> loopback
    engine --> loopback
```

`skills` and `memory` import only the standard library, so they sit off the graph.
Nothing imports `mcp`, `skills` or `memory` yet; `dispatch` and the agent loop pick
them up in v0.3 and v0.4.

Three things to notice:

- **`cmd/meru` stays small.** It reaches `rpc`, `tui` and `config`, and never
  `engine`, `agent`, `transcript`, `store` or `index`. The client only moves messages; the daemon
  does the work. A test in `internal/policy` fails the build if this ever changes.
- **`loopback` imports nothing of Meru's.** It sits at the bottom so every package
  that talks to an address can use the same check.
- **`store` imports only `engine`,** for the `Vector` type. It reads no files and
  calls no model: `index` hands it finished chunks and vectors, and `retrieve`
  merges its two searches.

## 3. The interfaces and types that hold it together

Most of the code is plain functions and structs. A few definitions connect the
packages; learn these and the rest reads easily.

### `engine.Engine`: the only door to a model

```go
// internal/engine/engine.go
type Engine interface {
    Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error)
    Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error)
    Embed(ctx context.Context, texts []string) ([]Vector, error)
    Info(ctx context.Context) (ModelInfo, error)
}
```

`OllamaEngine` in `ollama.go` is the one real implementation; the tests use fakes.
`Generate` waits for the whole answer, and the router uses it. `Stream` hands the
answer back piece by piece, and the agent uses it so text appears as the model
writes it. `iter.Seq2` is Go's type for something you loop over with `for delta,
err := range stream`. ([more](coding-notes/go-basics/iterators.md))

### `rpc.Request` and `rpc.Event`: the socket messages

```go
// internal/rpc/protocol.go
type Request struct {
    Op      Op     // "ask", "ping", "index" or "index_status"
    Session string // empty starts a new conversation
    Text    string // the question
    Source  Source // "cli", "tui" or "job"
    Path    string // for "index": one folder or file; empty means every folder
}

type Event struct {
    Type       EventType    // see the list below
    Session    string
    Text       string       // token text, or one progress line
    Route      string
    Confidence float64
    Fallback   bool         // route event: the router wasn't sure and fell back
    Error      string
    Sources    []Citation   // sources event: the numbered excerpts the answer may cite
    Report     *IndexReport // report event: what one index run did
    Status     *IndexStatus // status event: what the index holds

    // Stats, on the "done" event that ends an ask:
    TTFTMillis     int64 // question received to first token, routing included
    DurationMillis int64 // question received to end of turn
    EvalMillis     int64 // the model's own time spent writing the answer
    TokensIn       int   // prompt tokens
    TokensOut      int   // answer tokens
}
```

`meru` sends one `Request`, as one line of JSON. `merud` answers with a stream of
`Event`s, one per line, and every reply ends with `done` or `error`:

| Op | Events, in order |
| --- | --- |
| `ask` | `session`; `route` (with `Fallback` set when the router wasn't sure); `sources` when the turn searched your files and found something; one `token` per piece of the answer; `done` with the turn's stats |
| `ping` | `done` |
| `index` | zero or more `progress` lines; one `report`; `done` |
| `index_status` | one `status`; `done` |

`meru index` sends `index`, and `meru index -status` sends `index_status`. A
`Citation` holds the number the answer cites, the path (as `~/…` under your home
folder), the heading, the line range or PDF page, and the fused score. The `done`
that ends an ask carries the turn's timings and token counts, which `meru chat`
shows under each answer. Fields are only ever added, so an older client reads a
newer `merud` without trouble.

### `rpc.Handler`: what the server calls for each request

```go
// internal/rpc/server.go
type Handler func(ctx context.Context, req Request, emit func(Event) error) error
```

This is a *function type*: any function with this shape is a `Handler`. The server
reads a request, calls the handler, and passes it `emit`, a function the handler
calls to send each event back. `agent.Agent.Handle` is the handler `merud` uses.
A handler may emit its own `done`, carrying stats: the server holds it back and
sends it last if the handler returns nil, or drops it and sends `error` if the
handler fails, so every reply ends with exactly one closing event.

### `agent.Router`: how the agent asks for a route

```go
// internal/agent/agent.go
type Router interface {
    Decide(ctx context.Context, question string, history []engine.Message) (Decision, error)
}
```

The agent doesn't import `internal/router`; it only knows this small interface.
`cmd/merud` joins the two with a tiny adapter, `routerAdapter`, that calls
`router.Decide`. That keeps the agent testable with a fake router. The adapter
also holds `[index] folders` and passes them in `router.Turn.Folders`, so the
router's prompt can name them.

### `agent.Searcher` and `index.Sink`: how v0.2 reaches the store

```go
// internal/agent/agent.go
func New(cfg config.Config, eng engine.Engine, router Router, search Searcher, log *slog.Logger) *Agent

type Searcher interface {
    Search(ctx context.Context, query string) ([]retrieve.Result, error)
}

// internal/index/indexer.go
type Sink interface {
    Document(ctx context.Context, path string) (doc store.Document, ok bool, err error)
    ReplaceDocument(ctx context.Context, doc store.Document, chunks []store.Chunk, vecs []engine.Vector) error
    DeleteDocument(ctx context.Context, path string) error
    Paths(ctx context.Context, prefix string) ([]string, error)
}
```

The same pattern again. `cmd/merud` passes `searchAdapter`, which calls
`retrieve.Search` on the store with the fixed list sizes (50, 50, 10), as the
agent's `Searcher`; a nil `Searcher` turns search off. It passes the
`*store.Store` itself as the indexer's `Sink`. Each package's tests pass a fake
instead, so neither needs a database file or a model.

`New` also reads `cfg.Index.Folders` twice. It adds `filesNote` to the system
prompt, which names the folders Meru searches, or says that none are set yet.
And it keeps each folder's last path part, such as `meru`, for the rule that
turns a `direct` route into `search`.

### `config.Config`: settings, checked once

```go
// internal/config/config.go
type Config struct {
    Profile       string        // "lite" or "full"
    Models        Models        // the model for each tier: Fast, Main, Embed
    Ollama        Ollama        // BaseURL (loopback only) and KeepAlive
    Agent         Agent         // MaxRounds, HistoryTurns, SystemPrompt
    Router        Router        // the router's four settings
    Observability Observability // OTLPEndpoint and friends
    Log           Log           // Level: "debug", "info", "warn" or "error"
    Index         Index         // Folders, Ignore, MaxFileMB, ChunkTokens, OverlapTokens, Watch
    Dir           string        // Meru's home, usually ~/.meru
}
```

`config.Load` fills in defaults, reads the file over them and checks every value.
After it returns, no other package re-checks config.

## 4. What happens when merud starts

```mermaid
sequenceDiagram
    participant M as merud main.go
    participant C as config
    participant O as obs
    participant E as engine
    participant R as rpc

    M->>C: Load(path)
    C-->>M: Config (defaults + file, all checked)
    M->>M: openLog(merud.log, level) — -v forces debug
    M->>O: Setup(cfg.Observability)
    M->>E: NewOllama(base URL, keep_alive, embed model, logger)
    M->>E: checkRuntime: Info() → Ollama 0.12.11 or later?
    M->>R: Listen(socket) — refuses if another merud answers
    M->>E: warm(): one tiny call per model, so they load now
    M->>E: embedDims: embed one probe text to learn the vector size
    M->>M: openStore: store.Open(meru.db, embed model, vector size)
    M->>M: index.New(cfg.Index, store, engine)
    M->>M: newRouter, then agent.New(cfg, engine, routerAdapter, searchAdapter)
    M->>M: newIndexService(indexer, store, folders)
    par errgroup, until Ctrl-C, SIGTERM or a server error
        M->>R: Serve(listener, handler) — questions to the agent, index ops to the indexer
    and
        M->>M: startupScan: Scan, or Reembed after an embed model change
    and
        M->>M: watch: re-index files as they change
    end
```

All of this is in `cmd/merud/main.go` (`run` and `serve`), `cmd/merud/runtime.go`
(`checkRuntime` and `warm`) and `cmd/merud/index.go` (`startupScan`, `watch` and the
adapters). The probe asks the embedding model rather than config for the vector
size, so a new embedding model can't leave a stale number behind; when the model or
the size changed, `store.Open` drops the old vectors and the startup scan re-embeds.

The three last steps run side by side in an `errgroup` (from `golang.org/x/sync`),
so `merud` answers questions while the first scan is still running. The group
cancels the other two if `Serve` fails; the scan and the watcher log their own
errors and never stop the server. A scan and a `meru index` run take turns, so two
walks never race over the same files. `signal.NotifyContext` turns Ctrl-C into a
cancelled `context.Context`, and every step watches that context, so `merud` stops
cleanly wherever it is. ([more on context](coding-notes/go-basics/context.md))

## 5. One question, function by function

Follow `meru "what is the capital of France?"` through the code. A question
about your notes takes the same path, plus one search step when the route asks
for it:

```mermaid
sequenceDiagram
    participant U as meru (cmd/meru)
    participant S as rpc server
    participant A as agent.Handle
    participant T as transcript
    participant RT as router.Decide
    participant E as OllamaEngine

    U->>S: rpc.Do: Request{Op: "ask", Text: ...}
    S->>A: Handle(ctx, req, emit)
    A->>T: New() or Open(id) → Session
    A-->>U: emit session event
    A->>T: History(n) → earlier turns
    A->>T: Append(user line)
    A->>RT: Decide(question, history)
    RT->>E: Generate(one token, log probabilities)
    E-->>RT: Completion with log probabilities
    RT-->>A: Decision{Route, Confidence, Outcome}
    A-->>U: emit route event
    opt route isn't direct, or a direct question names an indexed folder
        A->>A: searchFiles → retrieve.Search (embed, vector, keyword, rrf)
        A-->>U: emit sources event
    end
    A->>A: buildMessages(system prompt + excerpts, history, question)
    A->>E: Stream(messages)
    loop each piece of the answer
        E-->>A: Delta{Text}
        A-->>U: emit token event
    end
    A->>T: Append(assistant line, token counts)
    A-->>S: emit done event with the turn's stats
    S-->>U: done event (sent last)
```

The same path as a reading list, in order:

1. **`cmd/meru/main.go` → `ask`** builds a `Request` and calls `rpc.Do`, then prints
   each `token` event's text as it arrives. When a `sources` event came, it prints
   `Sources:` after the answer, with the sources `rpc.Cited` finds cited in it, or
   all of them when the answer cites none.
2. **`internal/rpc/client.go` → `Do`** connects to the socket, writes the request
   as one JSON line, and reads events back until `done` or `error`.
3. **`internal/rpc/server.go` → `serveConn`** reads the request and calls the
   handler. If the client hangs up, `watchHangup` cancels the turn's context.
4. **`internal/agent/agent.go` → `Handle`** runs the turn: it opens the session,
   reads the history, saves the question, asks for a route, builds the prompt,
   streams the answer and saves it, then emits a `done` event with the turn's
   stats (`doneEvent`). Each step is a short function below `Handle`, with its own
   span and debug line: `openSession`, `appendLine`, `route`, `searchFiles`,
   `prompt` and `answer`.
5. **`internal/router/router.go` → `Decide`** writes the A-to-D prompt (`prompt.go`),
   asks the fast model for one token, and turns the log probabilities into a route
   (`probs.go`). The prompt names the `[index] folders` on option B's line.
   Back in `Handle`, a `direct` route becomes `search` when the question names an
   indexed folder as a whole word (`namesFolder`).
6. **`internal/agent/agent.go` → `searchFiles`**, on every route but `direct`
   (`searches`; until tools arrive in v0.3, `tools` searches too), calls
   **`internal/retrieve/search.go` → `Search`** with the query from `searchQuery`:
   the question, plus on a follow-up the session's latest earlier question that
   isn't only filler words (`namesSubject`). No model rewrites it. `Search` embeds the query, runs
   `store.SearchVector` and `store.SearchKeyword`, merges the two lists with `rrf`
   (50 hits from each, 10 kept), and loads the top chunks with `store.Chunks`. `retrieve.Format`
   numbers them, and the agent puts them under the system prompt and sends the
   same numbered list to the client as a `sources` event. A failed search is logged
   and the turn answers without your files.
7. **`internal/engine/ollama.go` → `Generate` and `Stream`** turn Meru's types into
   Ollama's JSON (`ollama_wire.go`), send the HTTP request, and turn the reply back.
8. **`internal/transcript/transcript.go` → `Append`** adds one JSON line to the
   session file; `History` reads the file back into messages.

Along the way, `internal/obs` records the timings and token counts, and each stage
opens a span under one trace per question: `rpc.request` at the root, `meru.turn`
under it, and a span for each step (ARCHITECTURE.md, "Observability" → "Traces").
With `merud -v`, each stage also writes a debug line to `merud.log` that carries
the trace's ID. When no metrics endpoint is set, the metric calls do nothing and
the spans record nothing, but each question still gets a trace ID for the log.

`meru chat` takes the same path. The only difference is at the ends:
`internal/tui` sends the `Request`, draws the events on screen with Lip Gloss
instead of printing them, renders the finished answer as Markdown with Glamour,
and passes the session ID back each time so the conversation continues.

### `meru index`, function by function

1. **`cmd/meru/index.go` → `indexCmd`** parses `-status` (or `--status`) and an
   optional path, which it makes absolute, because `merud` runs in another folder.
2. **`cmd/merud/main.go` → `handler`** sends `index` and `index_status` to the
   `indexService` in `cmd/merud/index.go`.
3. **`handleIndex`** refuses when `[index] folders` is empty, or when the path sits
   outside every folder, and names the config file to change. Otherwise **`run`**
   waits for its turn, then calls **`index.Indexer.Scan`** (or `Reembed` after an
   embedding model change) for every folder, or **`IndexPaths`** for one path, and
   emits `progress` lines and one `report`.
4. **`handleStatus`** reads `store.Stats` and the last scan's report, and emits one
   `status`.

The indexer records a `meru.index.scan` span for a full scan and a `meru.index.file`
span for each file, in traces of their own (ARCHITECTURE.md, "Traces").

## 6. Where errors and cancellation go

- **Errors travel up.** A function that can fail returns an `error` as its last
  result, and the caller checks it on the next line: `if err != nil { return
  fmt.Errorf("route: %w", err) }`. Each layer adds a few words of context, so the
  message that reaches you reads like a path: `meru: route: ollama /api/chat: …`.
  ([more](coding-notes/go-basics/errors.md))
- **Cancellation travels down.** Every function that waits on something takes a
  `ctx context.Context` first. Ctrl-C in `meru` closes the connection; the server
  sees the hang-up and cancels `ctx`; the engine's HTTP request stops; the agent
  writes no answer line for the cancelled turn.
- **The server always ends a reply.** If the handler returns an error, the server
  sends an `error` event; otherwise it sends `done`. The client never waits on a
  reply that won't come.

## 7. Where the tests are

Every package has `_test.go` files next to its code; `go test ./internal/agent/`
runs one package's tests. There are three kinds:

| Kind | Where | What it uses |
| --- | --- | --- |
| Unit tests | next to the code in each package | fakes: a fake `Engine`, a fake `Router`, `httptest` servers |
| End-to-end tests | `test/e2e/` (build tag `e2e`) | the real `merud` and `meru` binaries, against `cmd/fakeollama` |
| Integration tests | build tag `integration` | your real Ollama and models, opt-in |

`make check` runs all of them except the opt-in ones, plus every lint and security
check. [coding-notes/testing.md](coding-notes/testing.md) and
[coding-notes/e2e.md](coding-notes/e2e.md) explain how the tests work.

## 8. Where to go next

- **Each package in depth:** [docs/coding-notes/](coding-notes/), one note per
  package, in reading order, with short notes on each Go idea the code uses.
- **Why it is built this way:** [ARCHITECTURE.md](../ARCHITECTURE.md), and the
  gentler [level 100](architecture/100.md) and [level 200](architecture/200.md)
  versions.
- **Rules for changing the code:** [AGENTS.md](../AGENTS.md).
- **Running it:** [docs/running.md](running.md).
