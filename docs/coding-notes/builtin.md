# builtin

**Code:** `internal/builtin/` (`doc.go`, `builtin.go`, and the test
`builtin_test.go`)
**Milestone:** v0.3
**Architecture:** [First run and setup](../../ARCHITECTURE.md#first-run-and-setup),
[Approving a tool call](../../ARCHITECTURE.md#approving-a-tool-call)

## What it does

Some tools live inside `merud` instead of an MCP server. This package holds them,
behind the same `dispatch.Backend` interface the MCP pool and the A2A client use,
so every call still goes through `dispatch` (AGENTS.md, non-negotiable 4).

v0.3 has one built-in, `configure`. When you say "connect my Gmail" in chat, the
model calls it with `{"action": "add_mcp_server", "catalog": "gmail"}`, and
`configure` adds the Gmail entry to `config.toml`. It can also add a server outside
the catalog, from a name and a command or URL.

## The picture

```mermaid
sequenceDiagram
    participant M as model
    participant D as dispatch
    participant U as you
    participant T as builtin.Tools
    participant C as catalog
    M->>D: configure {"catalog": "brave"}
    D->>T: Confirm("configure")
    T-->>D: ConfirmAlways
    D->>U: approve once / deny
    U-->>D: once
    D->>T: Call(ctx, "configure", args)
    T->>T: brave_api_key in secrets.toml?
    alt key missing
        T-->>D: IsError: run `meru mcp add brave` in a terminal
    else key present
        T->>C: AppendServer(config.toml, Block(entry))
        T->>T: onChange(ctx), merud reloads MCP
        T-->>D: added; allowed tools; tools that ask first
    end
    D-->>M: result text
```

## Walk through the code

### builtin.go

`New` takes the config path, the `[builtin]` section and an `onChange` hook:

```go
func New(configPath string, cfg config.Builtin, onChange func(context.Context) error) *Tools
```

`merud` passes a hook that rebuilds its MCP pool, so a new server works without a
restart. The package doesn't know how the pool works; it only calls the hook.

`Confirm` decides whether a call asks first:

```go
switch {
case name == Configure:
    return dispatch.ConfirmAlways
case slices.Contains(t.confirm, name):
    return dispatch.ConfirmAsk
default:
    return dispatch.ConfirmNever
}
```

`configure` returns `ConfirmAlways` before it looks at `[builtin] confirm`, so no
setting can switch its prompt off, and `dispatch` offers only "approve once" and
"deny". Config grants lasting trust; the model mustn't grant any to itself.

`Call` decodes the arguments with `DisallowUnknownFields`, so a key the tool
doesn't know, such as an `allow` list the model made up, fails instead of being
ignored. `entryFor` accepts exactly one of two shapes: `catalog`, or `name` with
`command` (and `args`) or `url`.

Before it writes, `missingSecrets` loads `secrets.toml` next to `config.toml` and
checks every secret the entry names. When one is missing, `configure` writes
nothing and tells the model to send you to `meru mcp add <name>` in a terminal.
Keys never pass through the model, the approval prompt or the transcript.

Refusals come back as a `Result` with `IsError` set and a `nil` error. The model
reads the text and can fix its call or tell you what to do. Only a tool name that
isn't a built-in returns an error, because that call couldn't run at all.

A `sync.Mutex` wraps the check and the write, so two calls from two sessions can't
both pass the duplicate-name check and add the same server twice.

`Tools` builds the tool's JSON Schema from a Go map with `json.Marshal`. The
`catalog` property lists the catalog names as an `enum`, so the model sees the
valid choices.

## Go ideas used here

- **Interfaces** — `Tools` has the six methods of `dispatch.Backend`, so
  `dispatch` can hold it next to the MCP pool. The test line
  `var _ dispatch.Backend = (*Tools)(nil)` fails to compile if a method goes
  missing. More in [go-basics/interfaces.md](go-basics/interfaces.md).
- **Struct tags and `encoding/json`** — `configureArgs` maps the JSON keys to
  fields. More in [go-basics/json.md](go-basics/json.md) and
  [go-basics/struct-tags.md](go-basics/struct-tags.md).
- **`sync.Mutex`** — `mu.Lock()` then `defer mu.Unlock()` lets one caller at a
  time into the write. More in [go-basics/goroutines.md](go-basics/goroutines.md).
- **Functions as values** — `onChange` is a function passed in by `merud`.

## Try it

```sh
go test ./internal/builtin/
```

`TestConfigure` runs the tool against a temporary config: catalog entries with and
without their keys, custom commands and URLs, and each kind of bad argument. It
checks what landed in `config.toml`, that a refusal wrote nothing, and that no key
shows up in the result.

## Why it's built this way

- **One writer for config.** `configure` and `meru mcp add` both call
  `catalog.AppendServer`, so chat and terminal can't write different blocks.
- **A hook, not a pool.** Passing `onChange` keeps this package free of the MCP
  client; `merud` wires the two together.
- **No keys in chat.** The simple rule "the model never sees a key" beats any
  scheme for hiding a key the model has already read.
