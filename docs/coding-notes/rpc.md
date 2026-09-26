# rpc

**Code:** `internal/rpc/` (`protocol.go`, `sessions.go`, `citation.go`, `args.go`, `usage.go`, `client.go`, `server.go`)
**Milestone:** v0.1; sources and the index ops in v0.2
**Architecture:** [The shape: daemon + thin client](../../ARCHITECTURE.md#the-shape-daemon--thin-client)

## What it does

`rpc` carries messages between `meru` (the client) and `merud` (the daemon)
over a Unix socket at `~/.meru/merud.sock`. A Unix socket works like a network
connection, but it is a file on disk, and only processes on this machine can
open it.

The protocol is newline-delimited JSON. The client opens a connection and
writes one `Request`. The server writes `Event`s back, one JSON object per
line, and the last one is `done` or `error`. One connection carries one
request, and closing the connection cancels the turn.

## The picture

```mermaid
sequenceDiagram
    participant C as meru (Do)
    participant S as merud (Serve)
    participant H as Handler (agent)
    C->>S: {"op":"ask","text":"hello"}
    S->>H: h(ctx, req, emit)
    H->>S: emit(session)
    S-->>C: {"type":"session","session":"2026-…"}
    H->>S: emit(route)
    S-->>C: {"type":"route","route":"search"}
    H->>S: emit(sources), when the turn searched
    S-->>C: {"type":"sources","sources":[{"n":1,"path":"~/notes/garden.md",…}]}
    H->>S: emit(token) …
    S-->>C: {"type":"token","text":"Hi"}
    H->>S: emit(done + stats), held back
    H-->>S: return nil
    S-->>C: {"type":"done","ttft_ms":420,"tokens_out":12,…}
    Note over C,S: if the client hangs up early,<br/>the server cancels ctx and H stops
```

## Walk through the code

### protocol.go

The message types. `Request` has an `Op` (`ask` or `ping`), an optional
`Session` to continue, the question `Text`, and a `Source` (`cli`, `tui`, `desktop` or
`job`). `Event` has a `Type` and only the fields that type needs. A `route`
event sets `Fallback` when the router wasn't sure and used its fallback route.
The `done` that ends an ask carries the turn's stats: time to first token
(`ttft_ms`), total time (`duration_ms`), the main model's token counts
(`tokens_in`, `tokens_out`), and the time the model spent writing
(`eval_ms`). Every field has `omitempty`, so a `ping`'s `done` stays
`{"type":"done"}`, and a client built before these fields existed skips
them.

v0.2 added, without changing anything older clients read:

| Addition | What it carries |
| --- | --- |
| `sources` event | `Sources`, a list of `Citation`s: the excerpts from your files that the prompt held, numbered as the answer cites them (`n`), with the shortened `path`, `heading`, `start_line`/`end_line` or `page`, and `score`. It comes after `route` and before the first `token`, and only when the search found something. |
| `index` op | `Path` in the request: a folder or file to index, or empty for every `[index]` folder. The reply is `progress` events (one line of news in `Text`), one `report` event, then `done`. |
| `index_status` op | The reply is one `status` event, then `done`. |
| `report` event | `Report`, an `IndexReport`: files seen, indexed, unchanged, removed, failed and skipped, chunks written, and the time taken. |
| `status` event | `Status`, an `IndexStatus`: the configured folders, the counts of files, chunks and vectors, whether a scan runs, and the last full scan's report, time and error. |

v0.4 added the skill ops. `skills` and `skill_show` answer with one `skills`
event whose `Skills` lists `SkillInfo`s: name, description, and the `builtin`
and `edited` marks; `skill_show` adds the whole `SKILL.md` in `Body`, and
`skills` puts the reasons `merud` skipped any folders in `Text`. `skill_reset`
answers with `done` alone. A `route` event reuses the same `Skills` field for
the skills the turn loaded, with only `name` set, such as
`{"type":"route","route":"direct","confidence":0.91,"skills":[{"name":"writing"}]}`.
Reusing the field keeps one name for one idea on the wire, and `description`
has `omitempty`, so each entry stays that small.

The `search_files` tool added `Sources` to `ToolEvent`, set on a `tool_result`
event: the excerpts that call returned, numbered as the model reads them. After
a round whose calls returned excerpts, `merud` sends another `sources` event
that holds every source of the turn so far, the prompt's and the tools'. A
client keeps the last `sources` event it saw, so both clients' `Sources:` lists
and `Cited` cover what the model found through the tool, with no client change.
`meru check` adds up the paths of every `sources` event, which gives the same
set.

Two MCP ops back `meru mcp add`. `mcp_probe` carries the server to try in
`Server`, a `ProbeServer` with the fields of an `[[mcp.servers]]` entry minus the
allow lists, and answers with one `probe` event. Its `Probe`, a `ProbeResult`,
holds the name and version the server reports and every tool it offers, each
with `read_only` and `destructive`, the server's MCP hints. Both are `*bool`, so
a hint the server left out stays out of the JSON instead of reading as `false`.
`mcp_reload` answers with one `tools` event, the same as `tools`, showing the
servers after the reload. `ProbeServer.Remote` goes on the wire as `remote`; it
was `network` before the rename.

A third MCP op, `OpMCPStatus` (`mcp_status`), backs `meru mcp` and the chat's
`/mcp` box. `merud` answers from config and what its client pool already holds,
and sends nothing to any server, so the reply comes at once while a server is
down. The reply is one `mcp_status` event (`EventMCPStatus`) whose `MCP` field
holds one `MCPStatus` per `[[mcp.servers]]` entry, in config order:

```go
type MCPStatus struct {
    Name      string `json:"name"`
    Transport string `json:"transport"` // "stdio" or "http"
    State     string `json:"state"`     // MCPConnected or MCPNotConnected
    URL       string `json:"url,omitempty"`
    Tools     int    `json:"tools"`
    Allowed   int    `json:"allowed"`
    Confirm   int    `json:"confirm"`
    Err       string `json:"err,omitempty"`
}
```

`State` holds one of two constants, `MCPConnected` ("connected") and
`MCPNotConnected` ("not connected"). Config has no key that turns a server off,
so there is no third state. `Tools` is -1 for a server that isn't connected,
since it has no tool list to count; the clients draw it as `—`. `Tools` has no
`omitempty`, so a connected server that offers no tools still sends `0`.
`Allowed` and `Confirm` come from config (`Confirm` counts `confirm` and
`always_confirm` together), so they show either way. `Err` says in one line why a
server isn't connected.

The desktop app added `SourceDesktop` (`desktop`) to the sources, and two ops
for its list of past chats, with their types in `sessions.go`:

| Op | Reply | What it carries |
| --- | --- | --- |
| `sessions` (`OpSessions`) | one `sessions` event | `Sessions`, a `SessionInfo` per session that holds a question, newest change first, at most `Limit` (200 when zero): the ID, the first question as a one-line `Title`, `Started`, `Updated` and the number of `Turns`. |
| `session_turns` (`OpSessionTurns`) | one `turns` event | `Turns`, a `TurnInfo` per question of the session in `Request.Session`: the question, the answer, the route, how it ended, the source files, the tool calls as `ToolStep`s (full name, kind, arguments, outcome, time), the duration and the token counts. |

`merud` answers both from the transcripts, so they need no `meru.db`.
`ToolName(kind, server, tool)` joins a transcript line's three fields back into
the name the model saw, such as `google.search_gmail_messages` or `cmd.backup`.

`Report` and `Status` are pointers. `omitempty` leaves out a nil pointer but
never a struct value, so without the pointer every event would carry an empty
report.

Adding a slice field (`Sources`) made `Event` a type Go can't compare with
`==`, so tests compare events with `reflect.DeepEqual`.

### settings.go

The types of the desktop app's settings ops. `Scope` values name where a question
may look (`auto`, `files`, `mail`, `web`, `talk`), and `Scopes` lists them in the
order the app shows them. `PolicyChange` is what `tool_policy` changes, and the
`Policy` constants (`off`, `ask`, `allow`, and `always`, which only `merud`
reports) map onto config's lists. `Connection` and `ToolPolicy` describe each
tool source tool by tool, `CatalogEntry` and `CatalogNeed` a catalog server and
what it needs, `FolderInfo` one folder with its file count, and `ModelsInfo` the
models. `CustomServer` is a server of the user's own for `mcp_add`, with its
`EnvVar` list; a variable marked `Secret` carries a value `merud` saves to
`secrets.toml` and never sends back.

`Request.Policy` and `Request.Custom` are pointers, as `Request.Server` is. A
struct holding a map or a slice can't be compared with `==`, and tests compare
requests; a pointer keeps `Request` comparable. For the same reason `mcp_add` for
a catalog server takes no keys: the app saves a key with `secret_set` first. A
server of the user's own can't work that way, because `secret_set` accepts only a
name config already uses, so its secrets travel inside `Custom`.

### citation.go

Two helpers both clients use, so `meru` and `meru chat` show sources the same
way:

- **`Citation.String`** writes one line: `[1] ~/notes/garden.md, "Planting",
  lines 3–5`. A method named `String` also makes `fmt.Println(c)` print it
  this way.
- **`Cited(answer, sources)`** finds the `[1]` and `[1, 3]` marks in the
  answer with a regular expression and returns the sources they name. No
  marks means no sources: the model decides when a source matters.
- **`FileURL(path, home)`** turns a source's path into a `file://` URL, putting
  `home` back in place of a leading `~`. `url.URL` escapes spaces as `%20`.
- **`ShortPath(home, path)`** does the reverse: it writes a path under `home` as
  `~/...`. `merud` uses it for the sources of a live turn and of a past one, so
  both read the same.
- **`Hyperlink(url, text)`** wraps text in the OSC 8 escape codes, `ESC ] 8 ; ;
  URL ESC \` before and the same with no URL after. Terminals that know them
  (iTerm2, Ghostty, WezTerm, kitty, VS Code's terminal, Windows Terminal) make
  the text a link; others skip the codes. Both clients call it only when their
  output is a styled terminal.

### args.go

Helpers both clients use to show a tool call's arguments, which arrive as raw
JSON (`json.RawMessage`):

- **`ArgsLines(args, maxLines)`** indents the JSON with `json.Indent`, one field
  per line, for an approval prompt. Past `maxLines`, the last line says how
  many it left out: `… 12 more lines`.
- **`ArgsLine(args, width)`** squeezes the JSON onto one line with
  `json.Compact`, for a tool line or `meru log`, and cuts it to `width`.
- **`ArgvLine(argv)`** writes a local command's program and arguments on one
  line, for `meru tools` and `meru log`. An element that is empty or holds a
  space, a quote, a backslash or a character that doesn't print gets
  `strconv.Quote`'s double quotes, so `rg -- "two words"` still reads as two
  arguments after `--`. It is for people only; `merud` never runs the line.
- **`Cut(s, width)`** does the cutting. It counts characters (runes), not bytes,
  so it never splits a character such as "é" that takes two bytes.

Arguments that aren't valid JSON come back as they are, so a broken tool can't
hide what it sent.

### usage.go

Helpers both clients use to show a `usage` event, so `meru usage` and the `/usage`
box in `meru chat` show the same numbers:

- **`UsageTable(windows)`** returns rows of cells: the window names first, then one
  row per measure (sessions, questions, tokens in and out, active time, docs
  touched, tool calls). Each measure pairs its label with a function that reads its
  value from a window; Go keeps a function in a struct field like any other value.
- **`ShortCount(n)`** writes a count in a few characters: `950`, `1.2k`, `18k`,
  `1.4M`, with one decimal below ten, counting by 1,000.
- **`ShortDuration(ms)`** writes a time as its two largest units: `45s`, `2m 14s`,
  `3h 05m`.
- **`UsageNote`** is the line under the table: today, week and month follow the
  local calendar, while 1h and 30d roll back from now.

Each client lays out the cells its own way: `meru` with a `text/tabwriter`, and
`meru chat` by hand, so it can drop windows that don't fit the terminal.

### client.go

`Do` dials the socket, sends the request and returns the events as an
iterator, which callers read with `for ev, err := range rpc.Do(...)`. Cancelling
the caller's context closes the connection.

### server.go

`serveConn` answers `ping` itself and hands `ask`, `index`, `index_status`,
`tools`, `log`, `usage`, the memory ops (`memory_list`, `memory_add`,
`memory_forget`), the skill ops (`skills`, `skill_show`, `skill_reset`) and the
MCP ops (`mcp_probe`, `mcp_reload`, `mcp_status`) to the handler. Any other op gets an
`unknown op` error.

**Listen** claims the socket. A socket file can outlive a `merud` that crashed,
so `Listen` checks what is there first:

```go
case info.Mode()&fs.ModeSocket == 0:
    return nil, fmt.Errorf("%s exists and isn't a socket; ...", path)
default:
    if pingOK(ctx, path) {
        return nil, fmt.Errorf("another merud is already listening on %s", path)
    }
    if err := os.Remove(path); err != nil { ... }
```

A live `merud` answers the ping, and `Listen` refuses to start a second one. A
dead one doesn't answer, and `Listen` deletes its leftover file. `Listen` never
deletes a regular file it didn't make. After listening it sets the socket's
mode to `0600`, so other users on the machine can't connect.

**Serve** accepts connections until its context is cancelled and runs each one
in its own goroutine:

```go
stop := context.AfterFunc(ctx, func() { ln.Close() })
defer stop()

var wg sync.WaitGroup
defer wg.Wait()

for {
    conn, err := ln.Accept()
    ...
    wg.Go(func() { serveConn(ctx, conn, h, log) })
}
```

- `context.AfterFunc` runs `ln.Close()` when `ctx` is cancelled. That makes the
  blocked `Accept` return an error, and the loop ends.
- `wg.Go` starts a goroutine and counts it. `defer wg.Wait()` makes `Serve`
  wait for every connection to finish before it returns, so no goroutine
  outlives the server.

**serveConn** handles one connection. It reads the request line, answers
`ping` itself, and passes `ask` to the handler. The handler is a function type:

```go
type Handler func(ctx context.Context, req Request, emit func(Event) error) error
```

The handler calls `emit` for each event. When it returns, the server writes the
closing `done` (on `nil`) or `error` (with the error's text). Keeping the last
event in the server means every reply ends the same way.

A handler that wants its `done` to carry stats emits one. `emit` doesn't send
it; the server keeps it in a local variable and sends it in place of the plain
`done` if the handler returns `nil`:

```go
done := Event{Type: EventDone}
emit := func(ev Event) error {
    ...
    if ev.Type == EventDone {
        done = ev // hold it back until the handler returns
        return nil
    }
    ...
}
```

If the handler fails after emitting its `done`, the server sends `error` and
drops the `done`, so the client still sees one closing event, and it comes
last.

The client sends nothing after its request, so any further read returns only
when the client hangs up. A small goroutine waits for that and cancels the
handler's context:

```go
wg.Go(func() { watchHangup(br, cancel) })
```

That is how Ctrl-C in `meru` stops the model in `merud`: the client closes its
end, the read fails, `cancel` runs, and the agent's model call sees `ctx.Done()`.

One more guard: once `ctx` is cancelled, `serveConn` sets the connection's
deadline to now. A client that stopped reading can't then hold a write, and
its goroutine, open forever.

**The trace starts here.** For an `ask`, `serveConn` starts an `rpc.request`
span before it calls the handler. It is the root of the question's trace: the
agent's `meru.turn` and everything under it nest inside it, and every log line
the turn writes gets its `trace_id`. The span carries `meru.rpc.op`,
`meru.source` and `meru.question.chars`, the question's length, never its text.

At debug level `serveConn` writes `rpc ping` for a ping, `rpc request` when a
question arrives, and one closing line: `rpc done sent`, `rpc error sent` with
the error, or `rpc cancelled` with the reason. To tell a hang-up from a
shutdown it keeps the server's own context as `serverCtx`: if that one is
cancelled too, `merud` is stopping; if not, the client hung up. A cancel also
adds a `cancelled` event to the span.

## Go ideas used here

- **Goroutines and `sync.WaitGroup`** — one goroutine per connection, all
  waited for. More in [go-basics/goroutines.md](go-basics/goroutines.md).
- **`context`** — cancels the handler when the client leaves or `merud`
  stops. More in [go-basics/context.md](go-basics/context.md).
- **`defer`** — deferred calls run last in, first out; `serveConn` relies on
  that order. More in [go-basics/defer.md](go-basics/defer.md).
- **Function types** — `Handler` is a type whose values are functions, so a
  method value such as `agent.Handle` fits it.
- **Iterators (`iter.Seq2`)** — `Do` returns a function that `for ... range`
  can loop over.
- **`sync.Mutex`** — `serveConn` locks around writes so two events never mix
  on the wire.

## Try it

```sh
go test -race ./internal/rpc/...
```

`TestRoundTrip` also sends the index ops and a `sources` event through a real
socket, to show their payloads survive the trip through JSON. `TestCited`
covers which sources count as cited. `TestArgsLines` and `TestArgsLine` cover
indenting, cutting, and arguments that aren't JSON.

`TestClientDisconnectCancelsHandler` hangs up mid-answer and checks that the
handler's context ends with `context.Canceled`. `TestDebugLog` runs a question
that succeeds, one that fails and one whose client hangs up, and checks each
debug line and that none holds the question.

## Why it's built this way

- **One request per connection.** No request IDs, no multiplexing, and
  "close the connection" means "stop" without a separate cancel message.
- **Newline-delimited JSON** instead of gRPC or HTTP. You can debug it with
  `nc -U ~/.meru/merud.sock`, and it needs nothing outside the standard library.
- **The server writes `done` and `error`**, so a handler can't forget to end
  the reply or end it twice. The stats ride on `done` instead of a separate
  `stats` event, so clients keep one rule: the last event ends the reply.
