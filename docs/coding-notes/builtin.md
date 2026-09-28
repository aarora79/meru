# builtin

**Code:** `internal/builtin/` (`doc.go`, `builtin.go`, `datetime.go`,
`about.go`, `remember.go`, `writefile.go`, `files.go`, `chats.go`, `search.go`,
`web.go`, `webguard.go`, `webdownload.go`, `upload.go`, `images.go`, and the
tests `builtin_test.go`, `about_test.go`, `writefile_test.go`, `files_test.go`,
`chats_test.go`, `search_test.go`, `web_test.go`, `webfetch_test.go`, `upload_test.go` and
`images_test.go`, with the PDF in `testdata/`)
**Milestone:** v0.3 (`configure`, `web_search`, `web_fetch`), v0.4
(`remember`, `write_file`, `read_file`, `list_folder`, `grep`,
`search_files`)
**Architecture:** [First run and setup](../../ARCHITECTURE.md#first-run-and-setup),
[Approving a tool call](../../ARCHITECTURE.md#approving-a-tool-call),
[Memory](../../ARCHITECTURE.md#memory),
[Built-in skills](../../ARCHITECTURE.md#built-in-skills),
[Web search](../../ARCHITECTURE.md#web-search),
[Retrieval](../../ARCHITECTURE.md#retrieval),
[Facts and episodes](../../ARCHITECTURE.md#facts-and-episodes)

## What it does

Some tools live inside `merud` instead of an MCP server. This package holds them,
behind the same `dispatch.Backend` interface the MCP pool and the A2A client use,
so every call still goes through `dispatch` (AGENTS.md, non-negotiable 4).

There are eleven built-ins, and `[builtin] tools` in `config.toml` lists the ones
the model may use: all eleven by default. When you say "connect my Gmail" in chat, the model calls
`configure` with `{"action": "add_mcp_server", "catalog": "google"}`, and
`configure` adds the `google` entry to `config.toml`. You still start that server
yourself; `merud` connects to it on the next turn on a tools route. It can also add a server outside
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
the `[index] folders`, the output folder and your past chats, with the
indexer's own skip rules. Asked "what were my previous conversations with you
about?", the model lists `~/.meru/sessions` with `list_folder`, greps it for a
topic and opens a chat with `read_file`.

`search_files` is the fourth file tool. It runs the same search a turn runs
before the answer, by meaning and by keyword, for whatever query the model
writes, and hands back numbered excerpts. With `[index] retrieval = "agentic"`
no search runs before the answer, so this is how the model finds text by
meaning: `{"query": "why do firms exist"}` finds a note on Coase that never uses
those words. With `"auto"` it lets the model search again in other words.

When you ask "search the web for the latest Go release", the model calls
`web_search` with `{"query": "latest Go release"}`. The tool sends one GET to the
SearXNG you run on `127.0.0.1:8888` and hands back numbered results with their
URLs. Then it calls `web_fetch` with one of those URLs and a prompt, such as
`{"url": "https://go.dev/doc/devel/release", "prompt": "List the newest releases
with their dates."}`. `web_fetch` reads the page and asks the fast model the
question, and the model gets back one line that starts `From
https://go.dev/doc/devel/release (fetched 2026-09-24):`. Without a prompt it gets
the page's text; with `save` the file lands in `~/meru-output/downloads/`. A URL
that neither a search result nor your own question showed asks you first.

When you ask "which model are you using?", the model calls `about_meru` with
no arguments and gets back what `merud` knows about itself, such as `Answer
model (main): qwen3.6:35b` and `Ollama version: 0.34.0`. Before this tool, a
model asked that question answered from its training and named another
company's model.

Two functions here are no tools. When you drop `~/Downloads/garden-plan.pdf` on
the desktop app, `merud` calls `Upload`, which copies the file to
`~/meru-output/uploads/garden-plan.pdf`, and the question then asks the model to
read that copy with `read_file`. Drop `garden bed.png` instead, and `Upload`
copies it to `~/meru-output/uploads/garden_bed.png` as an image; when you ask
about it, `Image` reads the copy back so the agent can send its bytes to the
model with the question.

## The picture

```mermaid
sequenceDiagram
    participant M as model
    participant D as dispatch
    participant U as you
    participant T as builtin.Tools
    participant C as catalog
    M->>D: configure {"catalog": "obsidian"}
    D->>T: Confirm("configure")
    T-->>D: ConfirmAlways
    D->>U: approve once / deny
    U-->>D: once
    D->>T: Call(ctx, "configure", args)
    T->>T: obsidian_api_key in secrets.toml?
    alt key missing
        T-->>D: IsError: run `meru mcp add obsidian` in a terminal
    else key present
        T->>C: AppendServer(config.toml, Block(entry))
        T->>T: onChange(ctx), merud reloads MCP
        T-->>D: added; allowed tools; tools that ask first
    end
    D-->>M: result text
```

## Walk through the code

### builtin.go

`New` takes the config path, the `[builtin]` and `[web]` sections, the memory
store, the output folder for `write_file`, `merud`'s indexer for the file tools,
and two hooks:

```go
func New(configPath string, cfg config.Builtin, web config.Web, mem *memory.Store, outputDir string, files *index.Indexer, onChange func(context.Context) error, onRemember func(context.Context)) *Tools
```

For `onChange`, `merud` passes a hook that rebuilds its MCP pool, so a new server
works without a restart. For `onRemember`, it passes one that syncs the memory
folder into the store, so the next turn can recall the new fact. The package
doesn't know how the pool or the store work; it only calls the hooks, and a `nil`
hook does nothing. A `nil` memory store leaves `remember` out, and an empty
`outputDir` leaves `write_file` out, and a `nil` indexer leaves the three file
tools out; the tests of `configure` use all three. `merud` expands the `~` in
`[skills] output_dir` before it calls `New`, so this package gets an absolute
path, and it passes a `nil` indexer when `[index] folders` is empty. An empty
`searxng_url` leaves `web_search` out. After `New`, `merud` calls `UseModel(eng,
cfg.Models.Fast)`, so `web_fetch` can answer a prompt with the fast model, and
`UseSearch(search)`, so `search_files` can search. Both are methods, not more
parameters of `New`, so the many tests that build `Tools` without a model or a
store stay as they are.

`[builtin] tools` decides which built-ins exist at all. `New` keeps the list in
`t.on`, and `enabled(name)` asks `slices.Contains(t.on, name)`. Three places use
it. `Tools` builds its specs as before, then drops the ones the list leaves out:

```go
// DeleteFunc drops, in place, each spec the function returns true for.
return slices.DeleteFunc(specs, func(s engine.ToolSpec) bool { return !t.enabled(s.Name) })
```

`Status` does the same to what `meru tools` shows, and `Call` refuses a name
the list leaves out before it looks at the arguments. `dispatch` never offers a
tool `Tools` didn't return, so the model can't reach one that is off; the check
in `Call` is a second lock on the same door. `ConfirmCall` asks `enabled` too, so
the URL guard stays quiet when `web_fetch` is off.

A listed tool can still lack what it works on: `remember` needs the memory store,
`write_file` the output folder, the file tools the indexer, `search_files` the
indexer and a searcher, and `web_search` a SearXNG URL. `missing(name)` returns the reason, or `""`, and `Off()` returns one
`Off{Tool, Reason}` per listed tool with a reason, in list order. `merud` logs
each as "built-in tool off" at startup, so a user who listed `grep` with no
`[index] folders` can read why the model doesn't get it. `about_meru` needs
the facts function `merud` hands `UseAbout`; without it, `Off` says "merud gave
it no facts to report". `web_fetch`, `configure` and `datetime` need nothing, so
they never show up in `Off`.

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
`command` (and `args`) or `url`. A catalog name must be one of the two entries,
`google` or `obsidian`; any other name fails with the list. No catalog
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

**Changing while `merud` runs.** The desktop app sets a built-in tool's policy,
and `merud` hands the new lists to `SetLists`. `on` and `confirm` sit behind
`lists`, a `sync.RWMutex`, and `enabled`, `asks` and `Off` read them under it.
`hasFiles` asks the indexer whether any `[index]` folder is set, so the first
folder the app adds turns the file tools on; `merud` now always passes its
indexer. `EditConfig` runs a change to `config.toml` under `mu`, the lock
`configure` holds, so every writer in `merud` takes turns. `Summary` gives each
tool one line for the app's list, off tools included.

### datetime.go

`datetime` reads the clock and nothing else: the date and time with weekday and
zone, the time in another zone (`timezone`, an IANA name), and a date's weekday
with how many days it is from today (`date`, `YYYY-MM-DD`). The system prompt
already carries today's date; the time of day goes through this tool, because
putting it in the prompt would change the prompt's opening every minute and stop
Ollama reusing its work. The agent offers `datetime` on every route, `direct`
included, since "what time is it?" routes direct.

Two details:

- The day count rounds the hours between midnights to whole days, so a
  daylight-saving change, when one day is 23 or 25 hours long, doesn't cut a day.
- The blank import of `time/tzdata` puts the time-zone database in the binary,
  about 450 KB, so zone names resolve on a machine without zone files.

`Tools.now` holds the clock, `time.Now` outside tests, so `datetime_test.go` can
fix the time.

`EveryRoute(name)` reports whether a built-in goes with every route and every
scope but "talk": `datetime` and `about_meru`. The agent asks it, so the rule
lives in one place. `IsWebTool(name)` names `web_search` and `web_fetch`,
which the agent also offers on every route while `web_search` is on, so a
question the router sends `direct` can still look a fact up; the "My files",
"Mail and calendar" and "Just talk" scopes leave them out.

### about.go

`about_meru` answers questions about Meru itself: which models it runs, on
what, and what it can reach. It takes no arguments and only reads, so it never
asks.

The facts come from `merud`, which knows them; this package only writes them
out. `About` is a plain struct of names, counts and paths:

```go
type About struct {
    Version string
    Profile string
    Fast, Main, Embed string
    MainDetails engine.ModelDetails
    RuntimeVersion string
    Machine string
    Folders []string
    Files, Chunks int
    DBBytes int64
    Sources []AboutSource
    Commands []string
    Skills, Disabled []string
    Memories map[string]int
    // plus builtins and outputDir, which the tools fill themselves
}
```

`merud` calls `UseAbout(facts)` once at startup, with a function that fills an
`About` on each call (see [merud](merud.md)). Until then the tool stays off.
`aboutMeru` calls that function, adds the built-in tools the model may use and
the output folder, which `Tools` knows better than `merud`, and hands the lot to
`aboutText`.

`aboutText` writes one short line per fact. A fact `merud` couldn't read, such
as Ollama's version while Ollama is down, drops its part of the line instead of
guessing. The version sits on a labelled line of its own, `Ollama version:
0.34.0`: written inside a sentence, as `Ollama 0.34.0 runs every model`, it came
back from one model as "Ollama 0.44". For the same reason the tool's description
ends "Quote model names, versions and numbers exactly as this tool gives them." Each list stops at 12 names and says how many more there are, and the
whole text stops at 2,000 characters, about 500 tokens, because it lands in the
model's context on every call.

Nothing here can leak a secret, because `About` has no field for one: no env,
no header, no server error, no memory text. `dispatch` also runs the result
through its secret redaction, as it does for every tool.

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

### remember in an incognito chat

`remember` checks the call's session first, with `transcript.IsIncognito`, and
refuses: an incognito chat keeps nothing. The agent doesn't offer `remember` in
such a chat, but a model can still call a tool it wasn't offered, so the tool
checks too.

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

`read_file`, `list_folder` and `grep` read the `[index] folders` and the
output folder, `[skills] output_dir`. `New` adds the output folder through
`index.Indexer.ReadAlso`, so the tools read there under the indexer's rules,
and search never indexes it. It holds what `write_file` wrote, what `web_fetch`
downloaded and, in `attachments/`, the mail attachments the `google` server
saves (see [catalog](catalog.md)).

The three tools share one path check, `resolve`. It accepts four shapes of
path:

- absolute, such as `/Users/you/notes/a.md`;
- starting with `~/`, which it expands with `os.UserHomeDir`;
- relative, such as `reviews/plan.md`, when exactly one of those folders holds
  it. When two do, it refuses and names them;
- a name that no folder holds but `<output_dir>/attachments/` does, such as
  `folio_3f2a9c1e-7b4d-4e8a-9c0f-1a2b3c4d5e6f.pdf`.

The last shape exists for mail attachments. `get_gmail_attachment_content`
reports only the saved filename, and the model passes it to `read_file` as it
stands. `absPath` tries the attachments folder only when no other folder holds
the name, so a file in an `[index]` folder never turns ambiguous. The other
choice was a line in the prompt telling the model to prefix the name with the
folder; the fallback needs no prompt text and works whatever the model passes.
`read_file`'s description adds one sentence: pass the saved filename as path.

The model still writes full paths, and guesses the folder: in testing it asked
for `~/meru-output/downloads/<name>` when the server had saved the file in
`attachments/`. So for a full path that doesn't exist, `savedAttachment` looks
for a file with the same name in `attachments/`, but only when the path points
inside the output folder. A guessed path anywhere else stays a missing file,
and `Check` applies every rule to what the lookup finds.

Then `resolve` asks the indexer, through `index.Indexer.Check`, whether the
indexer would read that path (see [index](index.md)). A path found in the
attachments folder goes through the same check, so a symbolic link there is
refused. A path outside every folder fails with a message that lists them. A skipped path fails with the
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
tool lists the folders it reads: the indexed ones, then the output folder.
`search_files` names only the indexed folders, which it gets from
`index.Indexer.Folders`, since search never reaches the output folder.

**`grep`** compiles the pattern with Go's `regexp` package, which uses RE2
syntax. A plain-text pattern goes through `regexp.QuoteMeta` first, so a `.` or
a `(` in it matches only itself, and `(?i)` in front makes the match ignore
case. A bad regular expression comes back with the parse error, so the model
can fix it.

A `grepRun` holds one search's state. `search` walks each start folder with
`Walk`, and `file` reads each kept file with `ReadText` and tests it line by
line. A PDF line reports its page, `path (page N): text`; any other file reports
its line number, `path:12: text`. `around` keeps each line to 200
characters: a longer line shows the 200 around its first match, with `…` at
each end it cut. A chat's JSONL line holds a whole answer, thousands of
characters, and a cut from the start would show its `ts` and `type` fields and
drop the match. The window opens a third of the way before the match, so the
match reads with what led up to it. It counts runes, not bytes, so it never
splits an "é". With no path, `grep` searches every folder but the past chats
(see chats.go below). Three
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

`IsFileTool` names the four file tools, these three and `search_files`. The
agent offers them, with `datetime` and the commands that don't ask, on the
`search` route. It also uses `IsFileTool` to decide when the prompt gets the
note on using them: only on a turn about the user's files that offers one.

### chats.go

`read_file`, `list_folder` and `grep` also read the past chats: the session
transcripts in `~/.meru/sessions`, one JSON Lines file per chat, named by the
time it started, such as `2026/09/2026-09-17T141502-7f3a.jsonl`. A model asked
"check my previous conversations with you" saw only the three sessions that
recall put in its prompt, out of more than two hundred, and told the user it
had no access to past conversations.

`merud` calls `ReadSessions` once at startup with the folder. It hands the
folder to `index.Indexer.ReadChats`, which adds it as `ReadAlso` adds the
output folder, so the tools read it under every indexer rule and `Scan` and
the watcher never see it. Nothing in it reaches the store or search. It is a method rather than
a parameter of `New`, like `UseSearch`, so the tests that build `Tools`
without a sessions folder don't change.

The folder sits under the hidden `~/.meru`, and the hidden rule refuses a
name that starts with a dot. The indexer applies its rules from the folder a
path sits in downwards, though, never to the folders above it. So the
sessions folder is readable as a folder of its own, a hidden file inside it,
such as `.draft.jsonl`, is still skipped, and `~/.meru/config.toml`, outside
every folder the tools read, stays refused. The chats are `.jsonl` files,
which the indexer skips as unsupported; `ReadChats` makes that one folder the
exception, so a `.jsonl` file in an `[index]` folder stays skipped by search
and the file tools alike (see [index](index.md)).

`isSessions` tells the sessions folder apart among `index.Indexer.Roots`. The
roots come with symbolic links resolved, so it resolves the folder too before
it compares them. `listRoots` puts a label after it, "past chats with the
user, one JSONL file per chat", and each file tool's description names it.
`fileRoots` is every root but the sessions folder: `grep` with no path
searches those. A chat holds the first 4,000 characters of each tool result,
the text of the files its tools read included, so a grep of the user's files
would match many lines twice and the copies would use up `max_results`. The model greps the chats by passing their
folder as path.

`read_file` needs no change for a chat: it pages through the file 12,000
characters at a time, as it does any file. It refuses a chat over `[index]
max_file_mb` (5 MB by default) as too large. `dispatch` writes at most 4,000
characters of each tool result to a transcript, so a chat grows that large
only after more than a thousand tool calls.

### attachments.go

`get_gmail_attachment_content` saves a mail's attachment and reports its name.
In a real turn the model then failed to open it: it downloaded the same PDF four
times, looked in `~/meru-output/downloads`, made up a tool and an attachment ID,
found the file with `list_folder` in its last round and ran out of rounds.

`AttachmentText` takes that step off the model. `dispatch` calls it, through
`Options.Attachments`, for every MCP or A2A call that ends `ok`, with the
result's text and the time the call began. It lists `<output_dir>/attachments/`
with `os.ReadDir` and keeps a file when two things hold:

- its name appears in the result word for word (`strings.Index`). The rule knows
  nothing of Google's wording, so another server that saves there works too;
- its modified time is no earlier than the call's start, less two seconds.
  Only a file the call wrote counts, so an old attachment that some later
  result names stays out. The two seconds cover file systems that keep
  modified times in whole seconds. A window of "the last few minutes" would
  need a number to tune and would let a second, unrelated call pick up the
  same file.

It keeps at most two files, in the order the result names them. For each,
`attachment` writes a line with the path in the `~` form `read_file` takes,
such as `Saved at ~/meru-output/attachments/folio_3f2a.pdf.`, then a
`--- Meru read <name> (N of M characters) ---` line and the text. A longer
file ends with the offset to pass `read_file`. The path line comes first and
comes always: for a file Meru won't read, such as a symbolic link or a zip, the
block is that line and the reason, so the model can still name or open the file.

The reading goes through `index.Indexer.Check` and `ReadText`, the calls
`read_file` makes, so every rule holds: no symlinks, the size cap, PDFs page by
page. `joinPages` numbers the pages as `read_file` does. The text is at most
12,000 characters, `read_file`'s page, and less when the result is long: the
files share what the result leaves under `dispatch.MaxModelResult`, less 600
characters each for their lines, so `dispatch`'s cut never drops the line that
says how to read on. `dispatch` swaps any long base64 run for a short note
before it calls `AttachmentText`, so a result that carried the file as base64
leaves room for its text.

With no `[index] folders`, `merud` passes no indexer, the file tools are off,
and `AttachmentText` returns "".

### search.go

`search_files` needs the store and the embedding model, which live in `merud`.
The package doesn't import them. It defines the one method it calls:

```go
type FileSearcher interface {
    SearchFiles(ctx context.Context, query string, limit int) ([]retrieve.Result, error)
}
```

`merud` passes its `searchAdapter`, whose `SearchFiles` calls `retrieve.Search`
with `TopN` set to `limit`: the same search, list sizes and merge a turn runs
before the answer (see [retrieve](retrieve.md)). Tests pass a fake that returns
fixed results.

The model sends `query` and, if it likes, `limit`: 8 by default, 1 to 20. The
tool refuses a blank query, a limit out of range and an unknown key, with text
the model can read. A search that finds nothing says so and suggests other
words or `grep`.

The results come back best first. The tool keeps as many as fit in 14,000
characters, counting 200 for each citation line, and drops the rest from the
bottom. `dispatch` cuts every result at 16,000 characters; staying under that
means no excerpt arrives with its number and half its text.

Each excerpt needs a number the model can cite, and the number must not clash
with the excerpts already in the prompt or those another call returned in the
same turn. `dispatch.CiteNumbers(ctx, n)` reserves `n` numbers and returns the
first. The agent put the counter on `ctx` (see [dispatch](dispatch.md) and
[agent](agent.md)); outside a turn, as in most tests, it returns 1. The tool
then writes each excerpt under `retrieve.Cite`'s line, such as `[11]
~/notes/coase.md, "Theory of the firm", lines 3–9`, the same line the prompt's
excerpts carry, with the path shortened to `~/…`. The same numbers go into
`dispatch.Result.Sources` as `rpc.Citation` values, which the agent adds to the
turn's sources.

`search_files` only reads the index and runs without asking unless
`[builtin] confirm` lists it.

### web.go

`newWebClients` builds two `http.Client` values from `[web]`, one per tool, and
`New` keeps them in a `webClients` struct. Neither uses a proxy
(`Transport{Proxy: nil}`) or a cookie jar.

The tool's description tells the model to search a product, company or
person under the name the user wrote, in double quotes, and never to swap in a
name it knows. A model once searched for an older product with a similar name
to the one the user asked about.

**`web_search`** checks its arguments first: `query` must hold a word,
`max_results` must be 1 to 20 (0 means `[web] max_results`), and `time_range`
must be `day`, `week`, `month` or `year` when given. `searxng` builds the URL
with `url.Values`, which escapes the query, and sends one GET with a 15-second
timeout. The client's `CheckRedirect` returns an error, so it follows no
redirect: SearXNG is on loopback, and a redirect could only lead somewhere config
never approved. `readCapped` reads at most 2 MiB and fails past that instead of
returning half a JSON document.

Each way the request can fail gets its own message, because the model reads it
and passes it on:

| What happened | How the code tells | What the model reads |
| --- | --- | --- |
| nothing listens on the port | `errors.As` finds a `*net.OpError` whose `Op` is `"dial"` | `SearXNG isn't answering on <url>. See "Web search" in docs/running.md.` |
| JSON is off | the body starts with `<`; a fresh SearXNG answers `403` with an HTML page | `catalog.SearXNGFormatsHint`, which names `search: formats:` and `settings.yml` |
| SearXNG found nothing | `results` is empty | `SearXNG found no results for "…"` |
| the search ran long | `isTimeout` | try again, or search with fewer words |

`formatResults` writes a header line that tells the model to cite by URL, then
for each result `[n] title — url`, the snippet with its white space collapsed and
cut to 300 characters, and `Published 2026-02-10.` when SearXNG sends a date.
`PublishedDate` is a `*string`, a pointer, because SearXNG sends `null` for a
result with no date, and a pointer can hold "no value" where a plain string
can't.

**`web_fetch`** fetches a public page, so it guards where it connects. The
check sits in the dialer, the part of the HTTP client that opens the TCP
connection:

```go
&net.Dialer{
    Control: func(network, address string, _ syscall.RawConn) error {
        ap, err := netip.ParseAddrPort(address) // "93.184.216.34:443"
        ...
        return w.allowAddr(ap)                  // checkPublic in merud
    },
}
```

Go calls `Control` after DNS has turned the host name into an IP address and just
before the socket connects, with that address. So the check sees where the
connection goes. A URL such as `http://localhost:8080/` resolves to
`127.0.0.1` and fails, as does a public name whose DNS record points at
`192.168.1.1`. A redirect opens a new connection, so each hop passes the same
check; `CheckRedirect` also stops after 5 hops and refuses a scheme other than
http or https. There is no check before the dial, so a DNS answer that changes
between a check and the connection has no gap to use.

`checkPublic` does the work with `net/netip`'s own tests: `IsLoopback`,
`IsPrivate` (10/8, 172.16/12, 192.168/16, fc00::/7), `IsLinkLocalUnicast`
(169.254/16, where cloud metadata answers, and fe80::/10), `IsUnspecified` and
the multicast ones. Two blocks netip doesn't flag sit in `privateRanges`:
0.0.0.0/8, which reaches this machine on Linux, and 100.64.0.0/10, which carrier
NAT and VPNs such as Tailscale use. `Unmap` first turns `::ffff:127.0.0.1` back
into `127.0.0.1`. A refusal is a `*notPublicError`; `get` finds it inside the
errors the HTTP client wraps around it with `errors.As`, and shows the model its
reason alone.

The page client has no `Timeout` of its own. Each call puts a deadline on its
`ctx` with `context.WithTimeout`: 20 seconds for a page, 2 minutes for a
download. The deadline covers the body as well as the headers, and one client
serves both modes with the same dialer and redirect rule.

`webFetch` checks the arguments, then picks one of three modes:

| Arguments | What happens |
| --- | --- |
| `url` alone, or with `offset` | `fetchPage`, then 12,000 characters of text from `offset`, as `read_file` pages a file |
| `url` and `prompt` | `fetchPage`, then `answerFromPage` asks the fast model |
| `url` and `save: true` | `download` saves the body in the downloads folder |

`fetchPage` checks the `Content-Type` header before it reads the body, so a
refused type costs no download, then `pageText` picks the reader:

| Content type | Reader |
| --- | --- |
| `text/html`, `application/xhtml+xml` | `index.HTMLText`, the indexer's reader, which also returns the `<title>` |
| `application/pdf` | `index.PDFText`, from the bytes in memory; no temporary file |
| `text/plain` | as it is |
| anything else | refused, naming the type and pointing at `save` |

The body stops at 5 MiB. Each call fetches the page again, because a cache would
be one more thing to keep fresh and to size.

**The prompt.** `answerFromPage` takes up to 48,000 characters from `offset`,
about 12,000 tokens, and sends two messages to the fast model: a four-rule
system message (`fetchInstructions`: answer only from the page, quote numbers,
versions and dates exactly, compare dates for "latest", say when the page doesn't
say) and a user message with the question, the page and the question again. The
options are the ones the skill pick uses:

```go
engine.Options{Model: w.fastModel, MaxTokens: answerTokens, Temperature: &zero, NoThink: true}
```

It wraps the call in `obs.StartChat`, the `gen_ai.chat` span the agent's own
model calls use. `ctx` already carries `dispatch`'s `meru.dispatch` span, so the
new span nests under it with no extra wiring. The result reads
`From <final URL> (fetched <date>): <answer>`, and when the page has text past
what the model read, it says which characters the answer covers and gives the
offset for the rest. The tokens don't join the turn's usage: the router and the
skill pick, the other fast-model calls in a turn, don't either, and carrying
counts back would widen `dispatch.Result` for one tool. The span records them.

The live test on the `lite` model showed the limit of a 2B model here. On
go.dev's release history, which lists go1.27.1 under a go1.27.0 heading, it
answered "go1.27.0" to "what is the latest release?" at temperature 0 with
thinking off, whatever the instructions said. Asked to "list the newest entries
with their dates", it listed both, and the main model could pick. The
`web-research` skill tells the model to ask that way.

### webguard.go

A URL can carry data out: `https://attacker.example/?notes=<your notes>`. A page
the model reads can tell it to fetch one. So `web_fetch` runs without asking only
for a URL the session has seen before, from a trusted place:

- `webSearch` adds each result URL it shows to the session's set, with the
  session from `dispatch.SessionFrom(ctx)`, as `remember` gets it;
- `ConfirmCall` adds each URL in `Call.Question`, which the agent fills with the
  user's words: this turn's question and the earlier questions in the history.

`dispatch` asks a backend's `ConfirmCall` before `Confirm` when the backend has
one (see [dispatch](dispatch.md)). For `web_fetch`, it answers:

| The call | Answer | Choices the user sees |
| --- | --- | --- |
| a URL the session doesn't know | `ConfirmAlways` | once, deny |
| `save` on a known URL | `ConfirmAsk` | once, session, deny |
| a known URL, no `save` | `ok = false`: `Confirm` decides | none, unless `[builtin] confirm` lists `web_fetch` |

An unknown URL never gets a session choice: after one yes, every later made-up
URL would pass. A scheduled job has nobody to ask, so `dispatch` declines the
first two rows there.

`normalizeURL` makes the comparison fair: scheme and host in lower case, the
fragment gone, an empty path written as `/`. The query stays, because the query
is where data would travel. `questionURLs` finds URLs in the question with one
regular expression and trims the punctuation that ends a sentence, keeping a
`)` that has a partner inside the URL, as in Wikipedia's
`Go_(programming_language)`.

The sets live in `knownURLs`, a map from session to set with a `sync.Mutex`,
because one round's calls run side by side. `merud` runs for weeks and every
chat leaves a set, so the map has a cap: at most 256 sessions, and when a new
one would pass that, `dropOldest` removes the session used longest ago. A walk
over 256 entries costs less than keeping a second structure in order. One
session holds at most 2,000 URLs. The sets end with `merud`, as session
approvals do; after a restart the user's questions come back through the
history, and a search-result URL asks once.

### webdownload.go

`download` uses the same `get` as a fetch, so the dial check, the redirect cap
and the no-cookie rule hold, with a 50 MiB cap and a 2-minute deadline instead
of 5 MiB and 20 seconds:

1. `downloadName` picks the name: the `filename` in `Content-Disposition`, parsed
   with `mime.ParseMediaType`, or the URL's last path part. `safeName` then keeps
   only the part after the last `/` or `\`, so `../../.ssh/authorized_keys`
   becomes `authorized_keys`; turns anything but ASCII letters, digits, `.`, `-`
   and `_` into `_`; drops leading dots, so no download is hidden; and cuts the
   name to 100 characters. An empty result becomes `download`.
2. `openFolder` creates the output folder and `downloads` inside it with mode
   `0700`, refuses a `downloads` that is a symbolic link or a file, and returns an
   `os.Root` on it.
3. `saveNew` opens `report.pdf`, then `report-2.pdf`, `report-3.pdf` and so on,
   each with `O_CREATE|O_EXCL`. That flag makes the open fail when anything has
   the name, a symbolic link included, so the write never goes through a link or
   over a file. It copies at most the limit it gets, 50 MiB here, plus one byte;
   one byte over means the file is too large, and a deferred function removes
   it.
4. `previewText` reads back an HTML, PDF or text file of 5 MiB or less and returns
   its first 2,000 characters for the result.

`New` calls `index.ReadAlso` on the output folder, which holds the downloads
folder, so `read_file` and `grep` reach downloads under the indexer's own rules
(see [index](index.md)). The indexer never scans it, so a downloaded page can't
reach a later turn through search.

### upload.go

`Upload` copies a file the user attached in the desktop app into
`<output_dir>/uploads/`. `merud` calls it for the `attach_file` op; the model
can't, because no tool spec names it. The user picked the file, so it may sit
anywhere, and `Upload` checks it in this order:

1. The output folder is set, `[index] folders` isn't empty and `[builtin] tools`
   lists `read_file`. Without those the model couldn't read the copy.
2. `os.Lstat` looks at the path itself without following a link. A missing file,
   a symbolic link, a folder or anything but a regular file stops here.
3. `index.IsSecret`, the indexer's own test, refuses `.env`, `server.pem`,
   `id_ed25519` and the rest by name, before any copy exists.
4. A file over 50 MiB, `web_fetch`'s download cap, stops here too.
5. After `os.Open`, `os.SameFile` compares the file it opened with the one
   `Lstat` saw, so a path swapped for a link in between fails.
6. `openFolder` and `saveNew` copy the file, as a download is saved, under
   `safeName` of its name, with `-2` and up for a name in use.
7. `files.Check`, the test `read_file` runs on every path, checks the copy. A
   copy it refuses, such as a `.zip`, a file with a NUL byte or one over
   `[index] max_file_mb`, gets deleted, and the error gives the indexer's reason.

The copies go in `uploads`, not `attachments`: the `google` server deletes every
file in its attachments folder an hour after it was written, by modified time,
whoever wrote it.

An image takes a different path through the same function. Before step 1,
`IsImageName` looks at the name's ending: `.png`, `.jpg`, `.jpeg`, `.gif` or
`.webp`, in any case. For an image, step 1 checks only the output folder, since
the model looks at the image and never calls `read_file` on it; step 4 uses
`imageCap`, 20 MiB; and in place of steps 6 and 7, `uploadImage` reads the
first 512 bytes, asks `http.DetectContentType` what they are, and refuses
anything but a PNG, JPEG, GIF or WebP before a copy exists. So a text file
renamed `notes.png` stays out. Then it seeks back to the start and copies the
file under `imageName`, which is `safeName` with the ending kept, in lower
case, even when a long name is cut. `Upload` returns an `Uploaded`, the copy's
path and its kind: `rpc.AttachImage` or `rpc.AttachFile`.

### images.go

Beside the image half of `Upload`, this file holds `Image`, which the agent
calls when a question names an image. A client names the path, so `Image`
trusts none of it:

1. The path must be absolute and, once `filepath.Clean` has removed any `..`,
   sit right inside `<output_dir>/uploads/`. `/tmp/x.png` and
   `uploads/../outside.png` fail here.
2. The name must end like an image.
3. `openFolder` opens an `os.Root` on the uploads folder, and every step after
   goes through it, so no link inside can lead out.
4. `root.Lstat` refuses a missing file, a symbolic link, anything but a regular
   file and a file over `imageCap`.
5. After `root.Open`, `os.SameFile` checks that the file opened is the one
   `Lstat` saw, as in `Upload`.
6. `io.ReadAll` behind an `io.LimitReader` of `imageCap` plus one byte reads the
   file; one byte past the cap means it grew after `Lstat`.
7. The first bytes must pass `isImageData`, the same `DetectContentType` test
   `Upload` runs.

It returns the bytes, and the agent puts them on the question's message.

## Go ideas used here

- **`net/http` clients** — an `http.Client` holds the timeout, the redirect rule
  and the `Transport`, which holds the proxy setting and the dialer. Each web tool
  builds its own, so one tool's rules can't leak into the other's. More in
  [go-basics/http-clients.md](go-basics/http-clients.md).
- **`context.WithTimeout`** — returns a `ctx` that ends after a time, and a
  `cancel` function to call when done (`defer cancel()`). `web_fetch` uses it to
  give a page 20 seconds and a download 2 minutes on one client. More in
  [go-basics/context.md](go-basics/context.md).
- **The dialer's `Control` hook** — a function `net.Dialer` calls with the
  resolved address before it connects; returning an error stops the connection.
- **`net/netip`** — small value types for IP addresses and prefixes, with tests
  such as `IsPrivate` built in.
- **`errors.As`** — walks the chain of wrapped errors and finds one of a given
  type, here `*net.OpError` and `*notPublicError`. More in
  [go-basics/errors.md](go-basics/errors.md).

- **Interfaces** — `Tools` has the six methods of `dispatch.Backend`, so
  `dispatch` can hold it next to the MCP pool, and the one method of
  `dispatch.CallConfirmer`. The test lines
  `var _ dispatch.Backend = (*Tools)(nil)` and its `CallConfirmer` twin fail to
  compile if a method goes missing. `Generator` is an interface with the one
  engine method `web_fetch` calls, and `FileSearcher` one with the one search
  `search_files` runs, each defined here, where it is used. More in
  [go-basics/interfaces.md](go-basics/interfaces.md).
- **Struct tags and `encoding/json`** — `configureArgs` maps the JSON keys to
  fields. More in [go-basics/json.md](go-basics/json.md) and
  [go-basics/struct-tags.md](go-basics/struct-tags.md).
- **`sync.Mutex`** — `mu.Lock()` then `defer mu.Unlock()` lets one caller at a
  time into the write. More in [go-basics/goroutines.md](go-basics/goroutines.md).
- **Functions as values** — `onChange` is a function passed in by `merud`.
- **Named results and deferred cleanup** — `writeAtomic` names its `err` result,
  and a deferred function removes the temporary file only when `err` is set. More
  in [go-basics/defer.md](go-basics/defer.md).
- **`os.SameFile`** — reports whether two `os.FileInfo` values describe the same
  file on disk. `Upload` uses it to check that the file it opened is the one
  `os.Lstat` looked at a moment before, and `Image` does the same.
- **`http.DetectContentType`** — reads up to 512 bytes and names the type from
  the bytes each format opens with, such as `\x89PNG` for a PNG. It never looks
  at the name, which is why `Upload` and `Image` check both.
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

`web_test.go` never leaves the machine. `TestWebSearch` runs `web_search`
against an `httptest` server that plays SearXNG: JSON results, the `403` HTML page
SearXNG sends with JSON off, a `200` HTML page, no results, a `500`, and each bad
argument. `TestWebSearchQuery` checks the query string, `time_range` included,
and `TestWebSearchRefused` points the tool at a closed port.

`TestWebFetch` serves pages from an `httptest` server on `127.0.0.1` and swaps
`allowAddr` so that this one address and port count as public; every other
address still goes through the real `checkPublic`. It covers HTML with its title,
a PDF, plain text, paging, a redirect, a cookie that mustn't come back, a refused
content type, the 5 MiB cap, a 404, a redirect loop, and refusals of a second
server on `127.0.0.1`, of a redirect to it, of `::1`, `192.168.1.1` and
`169.254.169.254`. `TestWebFetchNameToLoopback` keeps the real check and asks
for `http://localhost:<port>/`, a DNS name that resolves to `127.0.0.1`, and
checks the server never saw a request. `TestCheckPublic` pins the address table.

`webfetch_test.go` covers what `web_fetch` adds. `TestWebFetchPrompt` answers
with a fake model and checks the result line, the options (fast model,
`NoThink`, temperature 0), the two messages, the 48,000-character cap on a
longer page and the offset that reads on. `TestWebFetchPromptFails` checks a
missing model, a failing one, and that the raw path doesn't call the model.
`TestWebFetchGuard` runs calls through a real `dispatch.Dispatcher` with a fake
SearXNG whose result links to the page server: a search-result URL and a
question URL run without asking; a made-up URL and one with notes in its query
ask, offering once and deny; `dispatch` declines a job's call; and a URL known in one session
asks in another. `TestNormalizeURL`, `TestQuestionURLs` and
`TestKnownURLsBound` pin the helpers and the cap. `TestWebFetchSaveAsks`,
`TestDownloadName`, `TestWebFetchSave` and `TestWebFetchSaveSymlinks` cover
downloads: the choices, the job, the names, `-2` and `-3`, the 50 MiB cap with
nothing left behind, and both kinds of planted link. `TestReadDownloads` reads
and greps a downloaded file through `index.ReadAlso`, and refuses a link in the
folder. `TestOutputFolder` builds tools with an output folder and one `[index]`
folder, and runs a table of calls: `read_file` of a PDF in `attachments/` by
full path, by bare saved filename, by `attachments/<name>` and under a guessed
`downloads/` folder, page by page;
`read_file` of a file `write_file` wrote; `list_folder` and `grep` in the
attachments folder and with no path; and refusals for a path outside both
folders (a guessed folder there included), a symbolic link in the attachments folder by path and by name, a
missing name, and `..` out of the folder. It also checks that `write_file`
still refuses an absolute path and `..` and still asks first, that
`search_files` doesn't name the output folder, and that `read_file`'s
description mentions the saved filename.

`chats_test.go` builds an `[index]` folder and a sessions folder under a
hidden `.meru` in a temp folder, with made-up chats about raised garden beds
and a `.jsonl` file in the `[index]` folder, which `grep` and `read_file` must
skip.
`TestChatsListed` checks `list_folder` names the sessions folder with its label
and lists the chats but not a hidden file. `TestChatsGrepAndRead` greps the
chats, finds a word deep in a long answer line, checks that `grep` with no path
leaves the chats out, reads a chat with `read_file`, and checks
`~/.meru/config.toml` stays refused. `TestChatsInDescriptions` checks the
descriptions, `TestReadSessionsWithoutFiles` checks `ReadSessions` does nothing
without an indexer, and `TestAround` pins the window: a short line, a match in
the middle, at either end, and characters of more than one byte.

`TestAttachmentText` sends a fake MCP tool's call through a real
`dispatch.Dispatcher` with `AttachmentText` wired in. The tool saves files in
the attachments folder during the call and names them in its result. The table
covers a fresh PDF (path line and both pages), an old one (nothing), a name the
folder lacks (nothing), a symbolic link and a zip (the path line and the
reason), a long file cut at 12,000 characters with the offset line, a long
result that leaves less room, and three names of which only two get read. For
the cases with one readable file it also calls `read_file` on the same file
and checks that a built-in's result gets nothing added.

`TestBuiltinToolsSwitch` builds `Tools` three times. With every setting there,
the eleven names from `config.BuiltinTools()` are exactly what `Tools` offers and
`Status` lists, which also proves the config list and this package agree. With
`[builtin] tools` cut to `datetime` and `grep`, only those two show up, and a
call to any other built-in fails. With no settings, `Off` names `about_meru`,
`remember`, `write_file`, the four file tools and `web_search`, each with a
reason.

`about_test.go` feeds `aboutText` a setup like the one behind the tool:
`qwen3.6:35b` as the main model, the lite fast model, one folder and two MCP
servers. `TestAboutText` checks each line, `TestAboutTextWithoutOllama` checks
that missing facts drop out, `TestAboutDescription` checks the line that asks
the model to quote names and numbers as given, and `TestAboutTextCap` gives it 200 commands and
200 agents and checks the lists shrink and the text stays under 2,000
characters. `TestAboutMeruTool` checks the tool is off until `UseAbout`, then
offered, never asks, and lists the built-ins and the output folder.
`TestWebToolsOffered` crosses `[web] searxng_url` with `[builtin] tools` for the
two web tools.

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

`upload_test.go` runs `Upload` on files in a folder outside the `[index]`
folders. `TestUpload` checks a copy lands in `uploads/` with the same bytes and
mode `0600`, a second copy of the same file gets `-2`, a name with spaces gets
underscores, and each refusal: missing, a relative path, a folder, a symbolic
link, three secret names, a sparse file one byte over 50 MiB, a `.zip`, a file
with a NUL byte and one over `max_file_mb`. Afterwards the folder holds only the
three copies, and `read_file` reads one of them. `TestUploadNeedsReadFile`
checks the refusals with no `[index]` folders, with `read_file` off and with no
output folder.

`images_test.go` builds tiny images with `image/png` and `image/jpeg`, so no
image files sit in `testdata/`. `TestUploadImage` runs with no `[index]` folders
and `read_file` off: a PNG and a JPEG named `receipt.JPG` still copy, as
`garden_bed.png` and `receipt.jpg`, with kind `rpc.AttachImage`, and `Image`
reads each copy back byte for byte. A text file named `notes.png` and a sparse
file one byte over 20 MiB are refused, and a Markdown file still needs
`read_file`. `TestImageRefuses` checks that `Image` refuses a file outside
uploads, one in the output folder above it, a path that climbs out with `..`,
a relative path, a symbolic link inside uploads, text named like a PNG, a name
that isn't an image's and a missing file. `TestImageName` checks the ending
survives a cut.

`search_test.go` runs `search_files` over a fake searcher.
`TestSearchFilesReturnsNumberedExcerpts` puts a counter on `ctx` that has
already handed out six numbers, and checks that the excerpts come back as `[7]`
and `[8]`, with `~` paths, heading, lines or page, and matching `Sources`.
`TestSearchFilesArguments` covers the limit range, a blank query and an unknown
key. `TestSearchFilesCap` feeds five 5,000-character excerpts and checks that
two come back and only two numbers were taken. `TestSearchFilesOff` checks the
tool stays off without a searcher or without `[index] folders`.

## Why it's built this way

- **Images by name and by content.** The name alone would let a renamed text
  file through, and the content alone would call a `.dat` file an image. Both
  checks cost one 512-byte read.
- **Images skip `read_file`.** The model looks at an image; it never reads one
  as text, so `files.Check`, which refuses media files, has nothing to say
  about it, and turning `read_file` off shouldn't stop a photo.

- **One switch per built-in.** `[builtin] tools` turns each tool on or off, the
  same way for all eleven. `web_fetch` had its own key under `[web]` before; two
  places to look for one question made the config harder to read.

- **Facts from `merud`, text from here.** `merud` owns the engine, the store,
  the pool and the skills, and this package imports none of them to report on
  them. A function that fills a struct keeps the two apart, and a test can
  hand the tool any setup it likes.

- **One writer for config.** `configure` and `meru mcp add` both call
  `catalog.AppendServer`, so chat and terminal can't write different blocks.
- **A hook, not a pool.** Passing `onChange` keeps this package free of the MCP
  client; `merud` wires the two together.
- **No keys in chat.** The simple rule "the model never sees a key" beats any
  scheme for hiding a key the model has already read.
- **The session on the context.** Only `remember` needs the session, so putting
  it on `ctx` beats adding a parameter to every backend's `Call`.
- **One search, two doors.** `search_files` calls `retrieve.Search`, the
  search a turn runs before the answer, through a one-method interface. A
  turn's up-front excerpts and the tool's excerpts come from the same code, so
  a measurement of one holds for the other.
- **Citation numbers on the context.** Only `search_files` numbers its result,
  so, as with the session, a counter on `ctx` beats a new parameter on every
  backend's `Call`.
- **The indexer's rules, not a copy.** The file tools call `index.Check`,
  `Walk` and `ReadText`, so a new skip rule reaches search and the tools at
  once, and the model can never read a file search would refuse.
- **The chats as files, not a new tool.** Two tools that list and read chats
  would each add a schema to every prompt that offers them, and code to keep in
  step with the transcript format. The file tools already read, list, grep and
  page, under the indexer's rules; one more folder costs a line in their
  descriptions. The chats stay out of the index, so no old chat crowds a
  file's excerpts in search.
- **Web search built in, not an MCP server.** SearXNG answers one GET with JSON.
  The MCP wrapper for it would add Node.js, an npm package and a child process
  per start; `web.go` is one file of Go.
- **Ask about URLs, not pages.** Blocking bad pages would need a list nobody can
  keep. A URL the user or a search showed can't carry anything the model
  learned, so those run freely, and everything else asks.
- **Attachment text through dispatch, not around it.** `AttachmentText` reads
  the file inside the call the model made, so the text lands in that call's
  `tool_result` line and row, with secrets redacted, and no extra call appears
  that the model never asked for. The other choice, a prompt line telling the
  model to call `read_file` next, had already failed in testing.
- **A per-call hook in dispatch, not a second path.** `ConfirmCall` changes only
  the answer to "does this call ask?". The call still goes through `Dispatch`,
  with its transcript lines, row, metrics and span.
- **The address check at dial time.** Checking the URL's host before the request
  would miss a name that resolves inside the network, and a redirect. `Control`
  sees the one address that matters, the one the socket connects to.
- **No bash tool.** Meru runs without a sandbox, so it offers four narrow
  read-only tools instead of a shell. A program you want the model to run goes
  in `[[commands]]`, whole, with the model filling only typed parameters; see
  [commands](commands.md).
- **One folder, checked twice.** `write_file` checks the path itself and then
  works through an `os.Root`. Either guard alone would stop `..` and links; both
  together mean a gap in one doesn't open the disk.
