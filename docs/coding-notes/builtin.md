# builtin

**Code:** `internal/builtin/` (`doc.go`, `builtin.go`, `remember.go`,
`writefile.go`, `files.go`, and the tests `builtin_test.go`, `writefile_test.go`
and `files_test.go`, with the PDF in `testdata/`)
**Milestone:** v0.3 (`configure`), v0.4 (`remember`, `write_file`, `read_file`,
`list_folder`, `grep`)
**Architecture:** [First run and setup](../../ARCHITECTURE.md#first-run-and-setup),
[Approving a tool call](../../ARCHITECTURE.md#approving-a-tool-call),
[Memory](../../ARCHITECTURE.md#memory),
[Built-in skills](../../ARCHITECTURE.md#built-in-skills)

## What it does

Some tools live inside `merud` instead of an MCP server. This package holds them,
behind the same `dispatch.Backend` interface the MCP pool and the A2A client use,
so every call still goes through `dispatch` (AGENTS.md, non-negotiable 4).

There are six built-ins. When you say "connect my Gmail" in chat, the model calls
`configure` with `{"action": "add_mcp_server", "catalog": "google"}`, and
`configure` adds the `google` entry to `config.toml`. You still start that server
yourself; `merud` connects to it on the next turn that offers tools. It can also add a server outside
the catalog, from a name and a command or URL.

When you say "remember that I work on the registry team", the model calls
`remember` with `{"kind": "me", "text": "Works on the registry team"}`, and
`remember` saves that fact as a file under `~/.meru/memory/me/`. From the next
turn on, the fact sits in every prompt (see [agent](agent.md)).

When a skill asks for a file, such as the explainer's HTML page, the model calls
`write_file` with `{"path": "dns/explainer.html", "content": "..."}`. After you
approve, `write_file` saves it under `~/meru-output/` and hands back the full path.

Search hands the model ten excerpts, which can't cover a folder. When you ask
"write about everything in my work folder", the model can call `list_folder` to
see what the folder holds, `read_file` to read each file whole, and `grep` to
find every file that names a customer. These three only read, and only inside
the `[index] folders`, with the indexer's own skip rules.

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

`New` takes the config path, the `[builtin]` section, the memory store, the
output folder for `write_file`, `merud`'s indexer for the file tools, and two
hooks:

```go
func New(configPath string, cfg config.Builtin, mem *memory.Store, outputDir string, files *index.Indexer, onChange func(context.Context) error, onRemember func(context.Context)) *Tools
```

For `onChange`, `merud` passes a hook that rebuilds its MCP pool, so a new server
works without a restart. For `onRemember`, it passes one that syncs the memory
folder into the store, so the next turn can recall the new fact. The package
doesn't know how the pool or the store work; it only calls the hooks, and a `nil`
hook does nothing. A `nil` memory store leaves `remember` out, and an empty
`outputDir` leaves `write_file` out, and a `nil` indexer leaves the three file
tools out; the tests of `configure` use all three. `merud` expands the `~` in
`[skills] output_dir` before it calls `New`, so this package gets an absolute
path, and it passes a `nil` indexer when `[index] folders` is empty.

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
`command` (and `args`) or `url`. A catalog name must be one of the three entries,
`google`, `brave` or `obsidian`; any other name fails with the list. No catalog
entry now runs on one system only or takes folders on the command line, so
`entryFor` dropped its checks for both.

Before it writes, `missingSecrets` loads `secrets.toml` next to `config.toml` and
checks every secret the entry names. When one is missing, `configure` writes
nothing and tells the model to send you to `meru mcp add <name>` in a terminal.
Keys never pass through the model, the approval prompt or the transcript.

Refusals come back as a `Result` with `IsError` set and a `nil` error. The model
reads the text and can fix its call or tell you what to do. Only a tool name that
isn't a built-in returns an error, because that call couldn't run at all.

A `sync.Mutex` wraps the check and the write, so two calls from two sessions can't
both pass the duplicate-name check and add the same server twice.

`Tools` builds each tool's JSON Schema from a Go map with `json.Marshal`. The
`catalog` property lists the catalog names as an `enum`, so the model sees the
valid choices.

### remember.go

`remember` takes two arguments, `kind` and `text`. The schema's `kind` enum comes
from `memory.Store.Kinds()`, read each time `Tools` runs, so a folder you create
by hand shows up as a choice on the next turn. The model can pick only a folder
that exists; making a new kind is up to you.

The description the model reads asks for one fact per call, written about you in
the third person ("Works on the AI registry team at Example Corp"). The profile
lists these facts in every prompt, and a line such as "I work on..." there would
read as the model talking about itself. It also tells the model to use `me` for
who you are, `preferences` for how you like things done, and never to save a
secret.

Before it saves, `remember` refuses:

- a kind that isn't one of the memory folders;
- an empty text, or one over 4 KiB (`memory.Add` checks the size and says so);
- a text that holds any value from `secrets.toml`.

The secrets check reuses `Secrets.Redact`: when redacting the text changes it,
the text holds a secret. It reads `secrets.toml` on each call, so a key you added
a moment ago counts, and it refuses to save when it can't read the file at all.

The memory's `source` line names the chat it came from, `session <id>`. `Call`
has no session parameter, so `remember` reads it from the context with
`dispatch.SessionFrom(ctx)` (see [dispatch](dispatch.md)).

`remember` answers `ConfirmNever` unless `[builtin] confirm` lists it, so a
memory saves without asking, as ARCHITECTURE.md says. The call still goes through
`dispatch`, so it lands in `tool_calls` and the transcript like any other.

### writefile.go

`write_file` takes `path`, `content` and an optional `overwrite`. Its checks run
in this order, and each refusal ends with "Nothing was written" so the model
can't mistake it for a success:

1. `cleanRelPath` turns down an empty path, an absolute one (`/etc/x`, `\x`,
   `C:\x`) and any path with a `..` part. It splits at both `/` and `\`, so
   `a\..\..\x` can't slip a `..` past a check that only knows one separator.
2. It refuses content over 1 MiB. A model that writes that much is most likely
   stuck in a loop.
3. `os.MkdirAll` creates the output folder, with mode `0700`, the first time.
4. `os.OpenRoot` opens the folder as an `os.Root`, and every later step goes
   through it. An `os.Root` refuses any path that would leave its folder, even
   through a symbolic link, so it backs up the checks above.
5. `makeParents` walks down the folders above the file, creating the missing ones
   and refusing any that is a symbolic link or a plain file.
6. `root.Lstat` looks at the file itself without following a link. It refuses a
   link, a folder or a device, and a plain file unless `overwrite` is true; that
   refusal tells the model to ask you first.
7. `writeAtomic` writes a hidden temporary file with mode `0600`, checks the
   error from `Close`, and renames it over the target. A rename inside one folder
   happens in one step, so nobody sees half a file, and a failed write leaves the
   old file as it was.

The result reads `Wrote 1234 bytes to /Users/you/meru-output/dns/explainer.html.`
The model passes the path on to you, and the client shows it in the tool result.

`write_file` asks before each call, because the shipped `[builtin] confirm` is
`["write_file"]`. A file outlives the chat, so the default is to ask. Take it out
of the list to let it write without asking.

### files.go

`read_file`, `list_folder` and `grep` share one path check, `resolve`. It
accepts three shapes of path:

- absolute, such as `/Users/you/notes/a.md`;
- starting with `~/`, which it expands with `os.UserHomeDir`;
- relative, such as `reviews/plan.md`, when exactly one indexed folder holds
  it. When none does, or two do, it refuses and names the folders.

Then it asks the indexer, through `index.Indexer.Check`, whether the indexer
would read that path (see [index](index.md)). A path outside every indexed
folder fails with a message that lists them. A skipped path fails with the
indexer's reason in words, such as "secret file" or "ignored by .gitignore,
.meruignore or [index] ignore". The package copies none of the skip rules; it
maps each `index.Reason…` constant to a phrase in `reasonText`, and that's all.

**`read_file`** calls `index.Indexer.ReadText`, which returns the text the
chunkers see: Markdown, text and code as written, HTML as its text, and a PDF as
one string per page. `joinPages` puts a `--- page N ---` line before each PDF
page. The tool turns the text into runes (`[]rune(full)`), Go's name for
characters, so `offset` counts characters and a page never ends halfway through
an "é". It returns at most 12,000 characters after a header line with the path,
size and modified date. When text remains, a closing line gives the next
`offset`. `dispatch` cuts every result at 16,000 characters, so a page always
arrives whole.

**`list_folder`** calls `index.Indexer.Walk` and sorts what comes back into
folders and files. A folder at the last level (`depth`, 1 to 3) shows up but
isn't entered: the callback returns `fs.SkipDir`. Walk passes skipped entries
too, each with its reason, and `skippedLine` counts them into one closing line,
such as "Left out 12 that Meru doesn't read: binary file (2), …". The walk
stops at 300 entries, or at 14,000 characters of lines, by returning
`fs.SkipAll`, and the result says so. Folders show no file count: counting a
folder the listing didn't enter would mean walking it too. With no path, the
tool lists the indexed folders themselves.

**`grep`** compiles the pattern with Go's `regexp` package, which uses RE2
syntax. A plain-text pattern goes through `regexp.QuoteMeta` first, so a `.` or
a `(` in it matches only itself, and `(?i)` in front makes the match ignore
case. A bad regular expression comes back with the parse error, so the model
can fix it.

A `grepRun` holds one search's state. `search` walks each start folder with
`Walk`, and `file` reads each kept file with `ReadText` and tests it line by
line. A PDF line reports its page, `path (page N): text`; any other file reports
its line number, `path:12: text`. Each line is cut to 200 characters. Three
limits stop the search: `max_results` lines (default 50, at most 200), 5
seconds, or 20,000 files. The first line of the result gives the counts and
names the limit that stopped it. It comes first so a long result that
`dispatch` cuts still keeps it. The limits live on `grepRun` as fields, so a
test can set them small. When the turn ends, `ctx` ends, `Walk` returns
`ctx.Err()`, and the walk stops.

PDFs are the slow part: pulling text out of a PDF takes far longer than reading
a text file. Over one user's real folders, about 4,000 files of which 150 were
PDFs, a grep that matched nothing took 2.3 seconds, and the PDFs took 0.8
seconds of that. That fits inside the 5-second limit, so `grep` reads PDFs.

All three run without asking unless `[builtin] confirm` lists them. They only
read, and only what search could already put in the prompt.

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
- **Named results and deferred cleanup** — `writeAtomic` names its `err` result,
  and a deferred function removes the temporary file only when `err` is set. More
  in [go-basics/defer.md](go-basics/defer.md).
- **`os.Root`** — a handle on one folder that refuses any path leading out of it.
  `internal/memory` uses the same guard, and so does `index.ReadText`.
- **Runes** — a `string` holds bytes; `[]rune(s)` holds characters. `read_file`
  counts in runes so an offset can't split a character.
- **`regexp`** — Go's regular expressions (RE2). They run in time linear in the
  input, so no pattern the model writes can hang `merud`.

## Try it

```sh
go test ./internal/builtin/
```

`TestConfigure` runs the tool against a temporary config: catalog entries with and
without their keys, custom commands and URLs, and each kind of bad argument. It
checks what landed in `config.toml`, that a refusal wrote nothing, and that no key
shows up in the result.

`TestRemember` sends each call through a real `dispatch.Dispatcher`, the way
`merud` does, so the session reaches the tool on the context. It checks the file
name, the source line, and that each refusal (bad kind, empty or long text, a
secret) saves nothing. `TestRememberSpecAndConfirm` checks the kind choices, the
description and the confirm rule.

`TestWriteFile` writes a file into a folder that doesn't exist yet, checks its
text and its `0600` mode, and checks that a second write needs `overwrite`.
`TestWriteFileRefuses` tries each bad path and size, and checks the output folder
stays empty. `TestWriteFileSymlinks` plants a link to a file and a link to a
folder, both pointing outside, and checks that neither write lands.

`files_test.go` builds a tree with one of everything the indexer skips: a
`.env`, a hidden folder, `node_modules`, a `.meruignore`, a config ignore
pattern, symlinks to a file and a folder outside, a file over the size cap, a
file with a NUL byte, an `.exe`, a `.png`, a two-page PDF and Markdown in
subfolders. `TestFileToolsRefuse` checks every refusal and its reason.
`TestReadFilePages` reads a 30,000-character file in three calls and checks the
pages join back into the file. `TestListFolder`, `TestListFolderEntryCap` and
`TestGrep` cover depth, the 300-entry cap, substring, case, regex, one-file and
one-folder searches, and PDF pages. `TestGrepLimits` sets tiny limits on a
`grepRun`, and `TestGrepCancelled` passes a cancelled `ctx`.

## Why it's built this way

- **One writer for config.** `configure` and `meru mcp add` both call
  `catalog.AppendServer`, so chat and terminal can't write different blocks.
- **A hook, not a pool.** Passing `onChange` keeps this package free of the MCP
  client; `merud` wires the two together.
- **No keys in chat.** The simple rule "the model never sees a key" beats any
  scheme for hiding a key the model has already read.
- **The session on the context.** Only `remember` needs the session, so putting
  it on `ctx` beats adding a parameter to every backend's `Call`.
- **The indexer's rules, not a copy.** The file tools call `index.Check`,
  `Walk` and `ReadText`, so a new skip rule reaches search and the tools at
  once, and the model can never read a file search would refuse.
- **No bash tool.** Meru runs without a sandbox, so it offers three narrow
  read-only tools instead of a shell. A program you want the model to run goes
  in `[[commands]]`, whole, with the model filling only typed parameters; see
  [commands](commands.md).
- **One folder, checked twice.** `write_file` checks the path itself and then
  works through an `os.Root`. Either guard alone would stop `..` and links; both
  together mean a gap in one doesn't open the disk.
