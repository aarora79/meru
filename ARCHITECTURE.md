# Meru — Architecture

*Meru* (मेरु) is a personal AI assistant that runs on a machine you control. This
document describes how it works and why.

This is the level-300 document: the full design, for people building Meru. For a
shorter start, read [level 100](docs/architecture/100.md) (the big picture) and
[level 200](docs/architecture/200.md) (how it works). All three are also
[web pages](https://aarora79.github.io/meru/architecture/100.html).

> We wrote this design before the code. If the code and this file disagree, one of
> them has a bug; say which.

## Principles

1. **Nothing leaves the machine by accident.** Meru connects only to loopback, except
   to A2A agents and MCP servers you mark as remote in config, and to the public web
   pages the model fetches with `web_fetch`, which you turn off by taking it out of
   `[builtin] tools`.
   A page URL that no search result or question of yours gave asks you first, so
   the model can't send your data out in a URL. Meru measures itself, but no telemetry
   leaves the machine, and the codebase has no path that sends a prompt to a cloud
   model.
   Other programs Meru talks to on loopback can reach the network on their own: an
   MCP server you add, such as Gmail, and SearXNG, which web search sends its
   search words through (see [MCP](#mcp) and [Web search](#web-search)).
2. **Files are the source of truth; SQLite is a projection.** Meru can rebuild
   everything in the database from your files and config. Delete `meru.db` and it
   re-indexes.
3. **You can inspect everything.** Memories and skills are Markdown files, and
   answers cite the files they drew on. Meru traces and times every turn, so you can
   find out why it said something and why it took so long.
4. **Few, narrow abstractions.** One engine interface, one store, one agent loop we
   own, and two wire protocols: MCP (Model Context Protocol) for tools and A2A
   (Agent2Agent) for other agents. Broad LLM frameworks change their APIs every few
   releases, and we don't want to chase them.
5. **Fast or unused.** People stop asking an assistant that makes them wait. The
   process model below exists to keep answers quick.
6. **Simple wins every time.** Pick the design with fewer moving parts, even if it is
   slower or less general, until a measurement says otherwise. Write code that a
   developer new to Go can follow.

---

## Language and distribution

Meru is written in **Go**. `meru` and `merud` build as native binaries that you can
copy to another machine and run, with no interpreter or virtualenv.

### Why Go

We chose Go over Python, the usual language for AI tools, for these reasons:

1. **A small footprint.** Each program is one binary, typically tens of megabytes. A
   Python app ships an interpreter, a virtual environment and its packages, often
   hundreds of megabytes. `merud` never stops running, so its idle memory matters,
   and the `meru` client starts in milliseconds instead of waiting on Python imports.
2. **Easy distribution.** One command builds for macOS, Linux or Windows, on Intel or
   ARM. Installing Meru means copying a file: no Python version to match, no
   dependency conflicts, and a container image that holds little more than the
   binary.
3. **Room to scale.** Meru serves one person, but nothing stops one server from
   running many Merus. A company, a school or a home lab could give each person their
   own `merud`, all sharing one Ollama on the same GPU server. At that point the
   overhead of each daemon multiplies, and a lean compiled daemon lets one server hold
   many more people than an interpreted one would. The models dominate the cost on a
   single laptop; across a rack of servers, Meru's own overhead adds up.
4. **Concurrency built in.** A turn streams tokens, runs tool calls in parallel, keeps
   MCP connections open, and shares the process with the scheduler and the indexer.
   Goroutines and `context` cancellation handle that directly, without Python's
   global interpreter lock.
5. **Fewer dependencies.** Go's standard library covers HTTP, JSON, Unix sockets,
   cancellation, structured logging and embedding files in the binary. Fewer
   third-party packages means less supply-chain risk, which matters for a tool that
   reads your mail.
6. **Errors caught before it runs.** Static types and the compiler catch mistakes that
   Python finds only at runtime, which suits a daemon that runs for weeks.
7. **It keeps building.** Go's compatibility promise means code written today still
   compiles years from now, which matches the goal that Meru keeps working without
   anyone's permission.
8. **The ecosystem is already Go.** Ollama is written in Go, and the official MCP SDK,
   the A2A SDK, OpenTelemetry and Bubble Tea all have first-class Go libraries.

The trade-off: Python has the stronger libraries for machine learning and PDF
parsing. That is one reason models run in Ollama rather than inside Meru, and why PDF
extraction is still an open question.

Three things stay outside the binaries:

- **Ollama**, which runs the models (see [Engine layer](#engine-layer)).
- **The observability stack**, which you can skip (see [Observability](#observability)).
- **MCP servers and A2A agents**, which run as their own processes.

The store uses `ncruces/go-sqlite3`, a Go library that runs SQLite compiled to
WebAssembly, with FTS5 and vec1, SQLite's own vector extension, built in. It needs no
cgo (Go's bridge to C code), so a plain `go build` works without a C compiler (see
[Storage](#storage)), and one command builds for another platform:
`GOOS=linux GOARCH=amd64 go build ./...`.

### Platforms

Every part Meru depends on runs on all three major desktop and server systems:
Ollama, SQLite, Go's Unix sockets (Windows 10 and later has them too) and the
OpenTelemetry stack.

| Platform | Status | Restarts `merud` after a reboot |
| --- | --- | --- |
| macOS, Apple silicon | supported and tested; the development machine is a Mac Studio (M4 Max, 64 GB) | `launchd` |
| Linux, x86-64 or arm64 (home servers, cloud virtual machines) | supported | `systemd` |
| Windows 10 and later | should work; not tested at first | a Windows service |

Code must not assume one platform: build paths with `filepath`, find the home
directory with `os.UserHomeDir`, and keep platform-specific code behind Go build tags
in as few files as possible.

**On a cloud server**, Meru still sends no prompt to a model provider,
but your notes, email and transcripts live on that server. Meru's promise is "a
machine you control"; where that machine sits is your call. You'd run `meru` over SSH,
because it reaches `merud` through a local socket.

---

## The shape: daemon + thin client

A command-line tool that starts, loads a model, answers and exits would be too slow:
the `full` profile's main model is ~18 GB at 4-bit and takes many seconds to load
from disk. So Meru has two programs:

- **`merud`** runs all the time. It keeps models loaded in Ollama, owns the store,
  holds the MCP and A2A connections, runs the scheduler and the agent loop, and emits
  metrics and traces.
- **`meru`** is a thin client. It connects to `merud` over a Unix socket at
  `~/.meru/merud.sock`, starts in ~50 ms, streams the answer back and exits.

The desktop app is a second thin client. One more program, the Mac installer, runs
once, before `merud` exists, to set the Mac up (see [Installer](#installer)).

A daemon that keeps models warm can also run scheduled jobs for almost no extra
work. That is why we build `merud` first and add the scheduler to it later.

![System diagram. Three short-lived clients, meru for one question, meru chat and meru-desktop, talk to merud over a Unix socket. Inside merud, the socket server and the scheduler feed the agent loop, which uses the skill registry, dispatch and the Engine. The Engine reaches Ollama and its fast, main and embed models over HTTP on 127.0.0.1:11434. Dispatch reaches MCP servers such as google and obsidian through the MCP client pool, other agents through the A2A client, and a SearXNG you run for web_search. The loop reads and writes the files under ~/.meru: session transcripts, memory files, skills, config.toml and meru.db, the index Meru can rebuild. The OTel SDK sends metrics and traces to an optional OTLP endpoint on 127.0.0.1:4318, which feeds Grafana, Prometheus and Tempo.](docs/architecture/img/300-overview.png)

*Figure 1. Arrows leave `merud` for programs it doesn't own from two places: the engine, to Ollama, and `dispatch`, to MCP servers, A2A agents and SearXNG. Each is one guarded entry point, which is where the privacy rules live.*

### Terminal UI

`meru chat` uses [Bubble Tea](https://github.com/charmbracelet/bubbletea), a Go
library for interactive terminal apps. A Bubble Tea program has three parts: a model
struct that holds the screen's state, an `Update` function that turns each event (a
key press, a window resize, a streamed token) into a new state, and a `View` function
that draws the state as text. The client reads tokens from the socket in a goroutine
and passes each one to the program with `program.Send`, so the answer grows on screen
as it arrives.

`meru chat` also uses Bubbles, from the same authors, for the text input and the
scrolling answer pane, and two more Charm libraries for its look:

- **Lip Gloss** styles the screen: a header with the name and the short
  version (`v0.4.3`, or `dev` and the commit for a build between tags, from
  `internal/about`, as the desktop app's rail shows it), the profile, model, what the
  index holds (`2637 docs (11698 vectors, 84 MB)`, with `· indexing` while a
  scan runs), the session, the last hour's use (`1h: 4 questions · 18k in ·
  2.1k out`) and whether `merud` is reachable; "You" and "Meru" labels; a route badge on each
  answer, amber when the router fell back; and a stats line with time to first
  token, tokens per second and total time. Colors adapt to light and dark
  terminals, and `NO_COLOR` turns them off. The chat asks `merud` for the
  header's numbers every 5 seconds while a scan runs and every 30 seconds
  otherwise. Typing `/usage` opens a table of sessions, questions, tokens in
  and out, active time, files touched and tool calls for the last hour, today,
  this week, this month, the last 30 days and all time; `meru usage` prints
  the same table, and `/usage by model` shows one row per answer model (see
  [Model sets](#model-sets)). Typing `/model` opens a table of the model sets,
  `/model <name>` switches to one and `/model save` makes it the default; the
  header names the set in use beside the usage. Typing `/new` starts a new
  session, so a conversation that went wrong stops shaping the answers after it. In both clients, each source
  line is a link (OSC 8) to its `file://` path, which terminals that support
  links open on a click.
- **Glamour** renders each finished answer as Markdown: headings, lists, and code
  blocks with syntax highlighting. While the answer streams, the screen shows the
  raw text with a cursor, because half-written Markdown renders wrong.

A finished answer puts a dim `⧉ copy N` label under each code block. The numbers
run on through the session, so `/copy N` names one block on the whole screen, and
`/copy` or Ctrl-Y copies the newest answer's last block. The chat finds the blocks
in the answer's Markdown, not in what Glamour drew, and hands the text to the
system's clipboard program (`pbcopy`, `wl-copy`, `xclip`, `xsel` or `clip.exe`).
With none installed, it sends OSC 52, an escape code that asks the terminal to set
its clipboard. A click on a label copies too. That needs the chat to capture the
mouse, which takes plain click-and-drag selection away from the terminal, so
selecting text needs Option (iTerm2) or Shift (most others);
`[chat] mouse_copy = false` gives plain selection back.

A web or file link in a finished answer shows as its URL without the scheme, cut
with `…` to fit its line, and a click opens the full URL in terminals that support
OSC 8 links. A Markdown link keeps its text, with the URL after it, so you see
where a click goes before you click. Glamour alone breaks a long URL at its dots
and dashes, and the terminal's own URL detection then finds only half of it. With
`NO_COLOR` nothing on screen can open a link, so the chat shows the full URL as
plain text: whole where it fits, and in line-wide pieces where it doesn't. Only
`http`, `https` and `file` links change; a URL in code stays as the model wrote it.

With `[chat] mouse_copy` on, the default, the terminal never sees a click, so the
chat opens links itself. It finds the OSC 8 link under the pointer, in an answer or
in the Sources list, and hands the URL to `open` on macOS, `xdg-open` on Linux or
`rundll32` on Windows. It starts that program with no shell and the URL as one
argument. It opens only `http`, `https` and `file` URLs, refuses one that starts
with `-`, which the program would read as an option, and says on the bottom line
what it opened. With `mouse_copy = false` the terminal gets the click, and its own
link click, often Cmd-click, opens the link.

While a turn runs, Enter puts the next question in a queue, drawn under the
running turn with a dim `queued` mark. When the turn ends, the chat sends the
oldest queued question in the same session, so `merud` still gets one turn at a
time; the queue lives in the client. It holds five questions at most, because
each one costs a whole turn and a longer line of them is more often a slip than
a plan. Ctrl-C stops the running turn and drops the queue: a user who stops an
answer wants the screen back. `/new` drops it too, since those questions
belonged to the old conversation. Commands that only open a box or copy text
run at once. A model switch waits for the running turn to end, since `merud`
would unload the model that writes its answer.

**The same features as the desktop app.** The chat does what the app does
wherever a terminal can, with the ops the app already sends; it adds no op of its
own. The commands, in the order `/help` lists them:

| Command | What it does | Op |
| --- | --- | --- |
| `/new` | start a new session | — |
| `/chats [words]` | a box of past chats, filtered by the words; Enter reopens one, and the next question continues it | `sessions`, `session_turns` |
| `/retry` | ask the newest question again, with its scope and images (the app's Try again) | `ask` |
| `/scope [auto\|files\|mail\|web\|talk]` | set where the next questions look, as the app's switch does; the header names a scope other than auto | `ask` with `scope` |
| `/attach [path]` | attach a file or an image to the next question; `/attach` alone takes them all off | `attach_file` |
| `/save [chat]` | save the newest answer as a note, or the whole chat as a file | `save_file` |
| `/used` | what the newest answer used: every source, each tool call, the memories recall brought, and who the calls reached | `memory_forget` |
| `/copy [N\|answer]` | copy code block N, the newest answer's last block, or the whole answer | — |
| `/usage [by model]` | the usage box | `usage` |
| `/me [add\|prefer <text>]` | the profile memories; `add` saves one to `me` and `prefer` one to `preferences` | `memory_list`, `memory_add`, `memory_forget` |
| `/mcp` | every tool source with each tool's Off, Ask or Allow, and the catalog servers not added yet | `connections`, `tool_policy`, `mcp_add`, `mcp_remove`, `secret_set` |
| `/folders [add <path>]` | the `[index]` folders and the usual ones not indexed yet | `folders`, `folder_add`, `folder_remove` |
| `/skills` | every skill, on or off | `skills`, `skill_enable`, `skill_disable` |
| `/model [name\|save]` | the model sets, a switch, and a save | `models`, `model_use`, `model_save` |
| `/log` | the latest tool calls, the app's Activity | `log` |
| `/about` | the tagline, the version, the license, Meru's folder and the project's links | — |
| `/help` | every key and command | — |
| `/exit` | quit | — |

Each box opens over the conversation and keeps the keys until Esc or q closes it.
In a box with rows, ↑ and ↓ move a marker and the box scrolls to keep it in view.
The keys that change something are the same everywhere: ← and → step a tool's
policy through Off, Ask and Allow (a tool that always asks steps between Off and
Always asks), Enter adds or toggles the marked row, and `d` removes a folder or an
MCP server or forgets a memory, after a second `d`, since none of those can be
undone from the chat. Adding a catalog server that needs an API key opens a field
that shows `•` for each character; Enter sends the key to `merud` with
`secret_set`, then `mcp_add` adds the server. The key never comes back.

`/attach` takes a path the user types, with `~` for the home folder. The app takes
only a real pick or drop, since its page could otherwise choose a path; in the
terminal the user types the path, so typing it is the consent. `merud` applies the
same rules to the file either way, and the chat applies the app's: five files at
most, one "Read this file" line per file, the images in the request's `images`,
and a scope other than auto or files switches to files when a file comes in. The
attachments show on one line above the input box.

A save asks first, as `write_file` does. The approval box opens where the
conversation was, since the save belongs to no turn, and a save waits while a turn
runs, so one approval box shows at a time. The approval box for a turn also offers
`e`, Edit first: it answers deny and puts the call in the input as a draft, "Run
mail.send with these arguments instead:" and the arguments, for the user to change
and send as a new question, as the app does.

The chat leaves out what needs a window: drag and drop, the file and folder
dialogs, the SVG preview, image previews, and custom MCP servers, which take a form
of their own. `meru mcp add stdio` and `meru mcp add http` add those in the
terminal.

The stats come from the `done` event that ends each reply, which carries the
turn's timings and token counts.

Answers always stream: `meru chat` and one-shot `meru` both show text as the model
writes it. When `merud` warns that an answer claims an action no tool took, `meru
chat` shows the warning in amber under the answer and one-shot `meru` prints it
on stderr as a `note:` line (see [Claims no tool backs](#claims-no-tool-backs)). Each tool call shows as a dim line inside the answer, `→ notes.search`
while it runs and `✓` or `✗` with its time or outcome when it ends. When `dispatch`
needs your approval, `meru chat` opens a box under that line with the tool's name
and arguments and three choices: approve once, approve for this session, or deny.
The box opens with deny selected, so a stray Enter runs nothing (see
[Approving a tool call](#approving-a-tool-call)).

The UI code holds no model or store logic; it draws what `merud` sends. One-shot
`meru "..."` doesn't use Bubble Tea at all: it prints the stream as plain text, which
also keeps it usable in scripts and pipes.

### Desktop app

`meru-desktop` is a third client: a native window for people who don't live in a
terminal. It is as thin as `meru chat`. It talks to `merud` over the same socket
with the same protocol, holds no model, store or tool logic, and never runs a tool
itself; `merud` runs every call through `dispatch`, and the app only answers its
approval questions.

**Wails v3 draws the window.** [Wails](https://wails.io) pairs a Go program with
the system's own WebView (WebKit on macOS, WebKitGTK on Linux, WebView2 on
Windows), so the app is one native binary with no browser engine inside it, and
the page is plain HTML, CSS and JavaScript. We pin a v3 beta: v3 has no final
release yet, and its beta notes call the API stable. Wails needs cgo and the
platform's WebView headers, so the app sits apart from the cgo-free build:

- `cmd/meru-desktop` holds only the window code, behind the build tag `desktop`.
  Without the tag the go command skips it, so `go build ./...`, the five-platform
  `make build` and CI's cross-compiles never touch cgo. `make desktop` builds it
  on the machine that runs it, with the tags `desktop production`; `production`
  turns off Wails' web inspector and its probe for a development server.
- `internal/desktop` holds everything else: the Bridge the page calls, the views
  it sends, and the page itself, embedded with `go:embed`. It doesn't import
  Wails, so its tests run everywhere.
- CI builds the app on macOS. Linux and Windows builds come later.

**The Bridge.** The window binds one Go value, the Bridge, whose methods the page
calls by name. `Send` opens an `ask` request with source `desktop` and the scope
the composer's switch sets, and a goroutine reads the events and hands each one to
the page as an `Update`, through one Wails event, `meru:update`. The Bridge keeps
the running turn and the queue, with the chat's rules: one turn at a time, five
questions waiting at most, and Stop drops the queue with a notice. For each tool
call it adds a friendly label ("Searched mail", "Read lisbon.md"), and when a turn
ends it lists who the turn's tool calls reached: each MCP server and A2A agent by
name, "web search" for `web_search` and the site for `web_fetch`. The other
methods each send one request for the Settings and Setup screens, save a chat or
an answer, show the system's file and folder dialogs, and close the app.

**One line says what Meru is**: "A personal AI assistant that runs entirely on
your own computer". The title bar shows "Meru · " and that line, and never
changes; the chat's own title sits in the page's header. The rail's logo carries
the line as its tooltip.

**The chat screen** has three columns:

- **The rail** holds the logo and wordmark, one button that opens About
  in Settings, then New chat, a search box that filters the list, past chats grouped
  Today, Yesterday and Earlier, a status block, and a Settings button at its
  foot. The status block shows the answer
  model, the file count and the connected MCP servers, or, when `merud` doesn't
  answer, that it isn't running and the command that starts it. A button folds
  the rail to a column of icons: the logo, New chat, chats, Settings and a status
  dot.
- **The conversation** shows each question as a bubble and each answer as a card.
  The header holds the chat's title, **Share as file** and a button that shows
  or hides the side panel. A one-line work strip says what the route did and lists
  each tool call by its label; "Show steps" opens the raw tool names, outcomes and
  times, and an amber "Waiting for you" chip marks an open approval. The answer
  streams as plain text and renders as Markdown when it ends, as in `meru chat`;
  each code block gets its number in the chat and a Copy button, and each answer
  **Copy**, **Save to a note**, **Try again** (the same question, in the same
  session and scope, with the same images) and a dim stats line. Under the
  answer, one closed line, "3 sources", opens to a chip for each source the
  answer cites, and a chip opens its file. The Bridge picks those sources with
  `rpc.Cited`, as `meru` and `meru chat` do; an answer that cites none shows no
  line. When `merud` sends a `notice`, because the answer claims an action no
  tool performed, an amber note with a warning icon sits under the answer, and
  again when the chat reopens from history. A new chat shows the logo beside "Ask Meru". The composer sends on Enter and adds a line on
  Shift+Enter; while a turn
  runs, Enter queues, and the queue shows above the composer with a remove button
  on each question. Under the text box sit the **Where Meru looks** switch and
  the attach button.
- **The side panel** starts closed; the header's button opens it, and the choice
  lasts until the window closes. "What this answer used" lists the selected
  answer's sources, every file the prompt held, its tool calls, and under
  **Remembered** the memories recall put in
  its prompt, each with a **Forget** button that sends `memory_forget`. It ends
  with a privacy line: "The model ran on this Mac. Only google was contacted."
  While an approval card is open, the panel turns into **Why Meru is asking**:
  why this call waits, the server's tools that are on with their policies, a
  link, "Change what google may do", that opens Settings at that connection,
  and a note that every call goes in the tool log under Settings, Activity. The
  panel doesn't open for a card: the card says why Meru asks, with a link to the
  panel. In Settings the panel is **On this Mac**: the answer and router
  models, what
  the search index holds, and a line that says there is no account and no cloud,
  with the path of `config.toml`. Below 1180 pixels the panel slides over the
  conversation on demand.

**Where Meru looks.** The composer's switch sets the request's `scope`: Auto, My
files, Mail and calendar, Web or Just talk. Auto runs the router as every other
client does. Any other scope skips the router, the route rules that widen a route
and a picked skill's tools, because a scope is a promise about what the turn may
touch:

| Scope | Route | Searches first | Tools offered |
| --- | --- | --- | --- |
| `files` | `search` | yes | what the search route offers but the web tools: the file tools, `datetime`, `about_meru`, commands that don't ask |
| `mail` | `tools` | no | every tool of each mail and calendar server, with `datetime` and `about_meru` |
| `web` | `tools` | no | `web_search`, `web_fetch`, `datetime` and `about_meru` |
| `talk` | `direct` | no | none |

A mail and calendar server is an MCP server or A2A agent whose tool names hold a
noun that ends in "mail" (gmail, email), or "calendar" or "event": the nouns the
router's prompt already reads (see [Routing](#routing)). So Meru needs no list of
mail providers, and the google server brings Drive and Docs along with Gmail,
while obsidian stays out. Deny-by-default still holds: a scope only narrows what
config allows. In Auto every route offers `web_search` and `web_fetch` while
`web_search` is on (see [Who decides what](#who-decides-what)); of the other
scopes only Web does, since My files, Mail and calendar and Just talk each
promise where the turn looks. `merud` logs the scope in the turn's info line and on its span;
the value is one of five.

**Attaching files.** The attach button opens the system's file dialog, which
sets no starting folder and takes one or more files from anywhere. Dropping files
on the chat screen does the same; while they hover, a "Drop to attach" overlay
covers the screen. Picking or dropping a file is the user's consent to share that
one file with Meru. The app writes nothing: the Bridge sends each path to `merud`
in an `attach_file` request, and `merud` copies the file into
`<output_dir>/uploads/` under its own name made safe, adding "-2", "-3" when the
name is taken. The file tools already read the output folder, so they read the
copy under their usual rules and reach nothing new.

`merud` refuses a folder, a symbolic link, anything but a regular file, a file
over 50 MiB (the cap `web_fetch` puts on a download), a name the indexer treats
as a secret (`.env`, `*.pem`, `id_ed25519` and the rest; see
[What stays out](#what-stays-out)), and a copy `read_file` couldn't read, such as
an archive, which it deletes. A user who drops a folder of files may
not know a key file sits among them, so the notice under the text box names each
file that stayed out, and why. `merud` also refuses while `read_file` is off,
since the model couldn't open the copy. A question takes five files at most:
each one costs a `read_file` call, and a small model loses track of more. Each
file shows as a chip above the text box with its name, its size and a remove
button. Send adds one line per file, "Read this file:
~/meru-output/uploads/garden-plan.pdf", and a scope other than Auto or My files
switches to My files, the scope that offers `read_file`.

**Attaching images.** An image takes the same button, the same drop and the same
`attach_file` request, and lands in the same uploads folder, but the model looks
at it rather than reading it with `read_file`. `merud` treats a file as an image
when its name ends in `.png`, `.jpg`, `.jpeg`, `.gif` or `.webp` and its first
bytes, read with Go's `http.DetectContentType`, are a PNG, JPEG, GIF or WebP
file's; a text file renamed `garden-bed.png` fails the second test and stays out.
The secret-name, symbolic-link and regular-file rules still apply. The cap is
20 MiB, which covers a phone photo of 2 to 12 MB with room to spare and keeps a
question of five images to about 133 MB of base64 on its way to Ollama. An image
skips `read_file`'s test for readable text and needs no `read_file` at all.
`attach_file` answers with the copy's `kind`, `image` or `file`.

The chip shows a small preview in place of the file icon. The Bridge builds it in
Go from the copy, and only from a file inside the uploads folder: an image up to
64 KiB goes to the page as it stands, and the Bridge shrinks a larger PNG, JPEG
or GIF to 160 pixels on its longest side and sends it as a JPEG, both as
`data:` URLs, which the page's policy allows for images. A large WebP gets an
image icon, since Go's standard library has no WebP decoder and a preview
doesn't justify a new module.
Send puts the images in the `ask` request's `images` field, by the copies' full
paths, and adds no "Read this file" line for them; the question's bubble shows
the previews above its text. An image switches no scope: it goes with the
question in each one. Images and files share the cap of five per question.

`merud` trusts none of the paths an `ask` request names. It takes five at most,
and each must be a full path right inside `<output_dir>/uploads/`, a regular
file and no symbolic link, opened through an `os.Root` on that folder, under the
cap, with an image's first bytes. It refuses anything else with a message the
user reads, before the turn starts. [Agent loop](#agent-loop) says what the
turn does with them.

- **Always a copy.** A file inside an `[index]` folder could be read where it is,
  but one rule for every file is simpler, and the question's line keeps working
  after the user moves or deletes the original.
- **`uploads`, not `attachments`.** The `google` server deletes every file in its
  attachments folder an hour after it was written, and a chat can go on longer.
- **Only a real pick or drop.** A WebView hides a dropped file's path from the
  page, so Wails reports the drop, with full paths, to Go. The window hands them
  to `desktop.Drop`, a plain function rather than a Bridge method, so the page
  can't call it with a path of its own choosing.
- **No tool call.** The copy is the user's act, so it skips `dispatch`. The
  `read_file` call that reads it goes through `dispatch` and lands in
  `tool_calls` as any other.
- **Removing a chip keeps the copy.** The app deletes nothing, and the uploads
  folder is the user's to clear.
- **Images by path.** The request names the copies, and `merud` reads the bytes
  itself, so an image never passes through the socket, and the transcript can
  name what the question carried.

**Slash commands.** The composer understands the commands `meru chat` has, each
mapped to the app's own screen: `/new` (as New chat: it stops a running turn and
drops the queue, with the chat's notice), `/chats` (the rail, with the search box
focused and filled with any words after the command), `/retry` (Try again on the
newest answer), `/scope <name>` (the Where Meru looks switch), `/attach` (the file
dialog), `/save` (Save to a note on the newest answer; `/save chat` is Share as
file), `/used` (the side panel for the newest answer), `/copy N` (code block N of
this chat, numbered as the chat numbers them; `/copy` alone the newest answer's
last block, and `/copy answer` the whole answer), `/usage` (Settings, Usage), `/me`
(Settings, About you), `/mcp` (Settings, Connections), `/folders` (Settings,
Folders), `/skills` (Settings, Skills), `/model` (Settings, Models; `/model <name>`
and `/model save` switch and save as in the chat), `/log` (Settings, Activity),
`/about` (Settings, About), `/help` (the command menu) and `/exit` (close the app,
asking first while a turn runs). Typing "/" at the start of the box opens a menu
of them, a listbox the arrow keys move through, filtered as you type; Enter or
Tab picks one and Esc closes it. A line that starts with "/" runs in the page and
never reaches the model; an unknown one leaves the text in the box and lists the
commands, as the chat does. The Bridge holds the list, and a test fails when it
differs from `meru chat`'s.

**Settings** has a back link and eight sections:

- **Connections** has a card per tool source: the built-in web tools, merud's
  other built-in tools, each MCP server and A2A agent, and the local commands.
  Each card shows whether the source is connected, the last error when it isn't,
  "N of M tools on", and a switch per tool with three positions that map onto
  config: **Off**, the tool isn't in `allow`; **Ask**, it is in `allow` and in
  `confirm`; **Allow**, it is in `allow` and not in `confirm`. A tool in
  `always_confirm`, and `configure`, shows **Always asks** and has no Allow: the
  one runs commands, and the other changes config, so the model could grant
  itself a tool. The local commands show their policy without a switch: each is a
  `[[commands]]` entry you edit in `config.toml`. **Add a connection** lists the
  catalog servers not added yet, with what each needs, its start command, and the
  tools it turns on; an API key goes into a password field. Under them, **Add
  your own MCP server** opens a form for a server outside the catalog, with the
  rules of `meru mcp add stdio` and `meru mcp add http`: a name of letters,
  digits, `-` and `_`; either a program with its arguments, one field for each,
  and environment variables, or a URL. A URL off this machine needs the tick
  "This server is on another computer", which writes `remote = true`. A variable
  ticked Secret goes to `secrets.toml` and `config.toml` holds `secret:<name>`.
  The server's tools all start Off; once it connects, its card lists them.
- **Folders** lists the `[index]` folders with how many files the index holds
  from each, a button that opens the folder dialog, the usual folders not indexed
  yet, and the skip rules.
- **About you** lists the profile memories, `me` and `preferences`, with Edit
  (a forget and an add) and Forget, and a form to add one, as `meru setup user`
  does.
- **Skills** lists every skill with an on and off switch, which edits `[skills]
  disabled`.
- **Models** shows the profile, the three models, the Ollama version and which
  models it holds in memory. Under them, **Models you can use for answers** has a
  card for each model in [Models we tried](#models-we-tried): its size, what it
  can do, a line on what it did well and one on what it did badly, whether
  Ollama has it, and its `ollama pull` and `ollama run` commands, each with a Copy
  button. **Use for answers** switches the answer model with no restart and saves
  it; it stays off until Ollama has the model, and a model without tools carries
  a warning. Above those cards, **Model sets** shows a card per
  [model set](#model-sets), with what its main model can do when Ollama or the
  list of models we tried says; **Use** switches to the set until `merud` stops,
  and **Make default** saves the set in use. The router, the embedding model and
  the profile change in `config.toml`, then a restart.
- **Activity** lists the `tool_calls` log, the data `meru log` prints: time,
  tool, outcome and duration, with the arguments and result behind a button.
- **Usage** shows the usage windows the chat's `/usage` box shows.
- **About** says what Meru is and where its name comes from, what it does and
  why it runs on your computer, the app's version, where `config.toml` and
  Meru's folder are, and three links: the source code, the design and a new
  issue on GitHub. The Bridge hands the page the links, so the page's own files
  name no host, and each opens in the browser through `OpenURL`. Under those
  paths, "Run setup again" opens Setup.

**Setup** has four steps, and opens on its own when `merud` reports no folders
and no profile memory; after that, "Run setup again" in Settings, About opens
it. **The models**
lists the three models; with `merud` down it shows the `ollama pull` each one
needs and the command that starts `merud`. Meru pulls nothing on its own: a model
is gigabytes, and `merud` doesn't start until its models are there. **Your
folders** offers checkboxes for `~/Documents`, `~/Notes` and `~/Desktop` where
they exist, each with a file count from `merud`, and "Choose another folder".
**Connections** shows the catalog cards. **About you** asks your name, your email
and how you like answers, and saves each as a memory, as `meru setup user` does.

**Settings change in `merud`.** The app writes no file of Meru's. Each change goes
to `merud` as an op, and `merud` checks it, writes it with the same code `meru mcp
add` and `configure` use, and applies it at once:

| Op | What `merud` does |
| --- | --- |
| `connections` | lists every source with each tool's policy, and the catalog |
| `tool_policy` | sets one tool's `allow`, `confirm` or `[builtin]` lists, then reloads that kind of source |
| `secret_set` | saves one key to `secrets.toml`, for a name config or the catalog uses, then reloads the MCP servers |
| `mcp_add` | appends a catalog server's block, once its key is saved, or a server of the user's own with no tools allowed and its secrets saved first, then reloads the MCP servers |
| `mcp_remove` | takes a server's block out, then reloads |
| `folders` | lists the `[index]` folders with file counts, and the usual folders not indexed yet |
| `folder_add`, `folder_remove` | edits `[index] folders`, hands the indexer the new list, restarts the watcher and scans |
| `skill_enable`, `skill_disable` | edits `[skills] disabled` and loads the skills again |
| `save_file` | saves the chat or an answer as Markdown through `write_file` |
| `models` | names the models, asks Ollama which it holds, and lists the models we tried with whether Ollama has each, and the model sets |
| `model_use` | switches to a model set until `merud` stops, unloading the old answer model first (see [Model sets](#model-sets)) |
| `model_save` | writes the models in use to `[models]` |
| `model_set` | switches to a model on that list that Ollama has, the same way, then writes `[models] main` |

A change to a list edits only that list's lines in `config.toml`: every comment
and every other key stays. `merud` writes a temporary file, loads it, checks that
the list came out as asked, and renames it over the old one, so a change that
would break config leaves the file as it was. One lock covers every write to
`config.toml` in `merud`, the `configure` tool's included. A tool policy checks
the tool against what the source offers or config already names, and refuses
anything else; a new tool a server starts to offer shows Off. The key a user
pastes goes to `merud` over the socket, which only that user can open, and never
comes back in any reply.

An answer model change takes effect without a restart too; see [Models we
tried](#models-we-tried).

A folder change takes effect without a restart. The indexer takes the new list,
the watcher starts again over it, and a scan adds the new folder's files or drops
the removed folder's. The file tools, the prompt's note on the user's folders and
the router's prompt read the list on every turn, so the first folder added turns
the file tools on. A folder op refuses the whole disk, the home folder itself,
Meru's own home, and a folder inside or around one already indexed.

**Saving a file.** Share as file saves the whole chat, and Save to a note one
answer, as Markdown under `[skills] output_dir`, in `chats/` or `notes/`, named
by the date and the first words of the title. `merud` makes the file with one
`write_file` call through `dispatch`, in the chat's session, so the save lands in
`tool_calls` and the transcript, and asks first as `write_file` does. The card
shows above the composer; after a yes, the notice names the file and offers
"Show in folder", which opens the folder in the file manager. A name already
taken gets `-2`, since `write_file` never replaces a file.

**Approvals sit in the answer.** An `approval` event becomes a card inside the
answer, amber like everything that asks. It shows the tool and its arguments, with
`to`, `cc`, `bcc`, `subject` and `body` laid out as a mail when the arguments hold
`to` or `subject`, and the rest as indented JSON. Focus lands on the answer that
runs nothing, so a stray Enter runs nothing. The answer goes back on the socket as
a `Reply`, as in `meru chat`. The Bridge gives each card an ID of its own, since
`merud` numbers approvals per connection and a save runs on a connection of its
own.

**Edit first.** The approval card has Send, Edit first and Don't send (Allow
once and Don't allow for a call that isn't mail, and Allow for this chat when
`merud` offers it). Edit first answers Don't send, then puts the call as a draft
in the composer: "Send this mail instead:" with its To, Subject and Body, for the
user to change and send as a new question. The model makes the call again with
the new text, and `merud` asks again. We chose this over approving with edited
arguments: `dispatch` would then run a call the model never made, and the audit
line, the approval and the call would need to agree on which arguments ran. The
second card shows exactly what will run, so nothing runs that the user didn't
see.

**Past chats come from the transcripts.** Two ops serve the rail: `sessions` lists
the sessions that hold a question, newest change first, with the first question as
the title; `session_turns` returns one session's turns, each with its question,
answer, route, sources, tool calls, time and token count. `merud` reads both from
the JSONL files, never from `meru.db`, so they stay right after the database is
deleted. Opening a past chat and asking again sends its session ID, so the
conversation continues.

**Model output is untrusted.** A bad answer, or a page the model fetched, could
hold HTML meant to run in the window. Three layers stop it:

1. `marked` renders the Markdown with raw HTML escaped, and DOMPurify keeps a short
   list of tags, no `data-*` or `style` attributes, no class but a code block's
   language, and only `http`, `https` and `file` links. It returns DOM nodes; no
   code puts a string into `innerHTML`. Everything else reaches the page as
   `textContent`. A code block that holds an SVG drawing also shows as a
   picture, with a Preview / Code switch: the page draws it as an `<img>` with a
   `data:` URL, where the browser runs no script and fetches nothing, so a
   drawing can only draw.
2. Every file goes out with a strict Content-Security-Policy: `default-src 'none'`,
   and scripts, styles, fonts and connections from the app only. No inline
   script runs, and no image or font loads from the network, so an answer can't
   report that it was read.
3. The page never navigates. A click on a link calls `OpenURL`, which hands only
   an `http`, `https` or `file` URL to the system opener, with no shell and the URL
   as one argument, the same code (`internal/opener`) that `meru chat` uses.

**Everything ships inside.** The page loads nothing from the network: the fonts
(Newsreader, IBM Plex Sans and IBM Plex Mono, under the SIL Open Font License) and
the two libraries (marked and DOMPurify) sit in `internal/desktop/web/` with their
licenses. There is no Node, npm or bundler: the page is ES modules the WebView
loads as they are, and the Bridge's methods are called by name through Wails'
runtime, so no generated bindings are needed.

**Not yet.** An "Earlier" line for turns scrolled out of view, pulling a model
from the app, and Linux and Windows builds come in later versions.

---

## merud as an agent harness

An *agent harness* is the code that runs a model in a loop: it builds the prompt,
runs the tools the model asks for, hands back the results and keeps a record.
`merud` holds a whole one: a model loop with tool rounds, one gate for every tool
call, an MCP client, skills with progressive disclosure, local commands and A2A
agents as tools, a session record, a context budget and tracing. `meru`,
`meru chat` and `meru-desktop` are clients of that harness. Meru the assistant sits
on top of it and decides where to look, what to remember and how to catch a small
model's mistakes.

| Layer | Packages and files |
| --- | --- |
| Harness core | `engine`; the tool rounds in `agent/tools.go`; `dispatch`; `mcp`; `skills`, with the skill pick in `agent/skills.go`; `commands`; `a2a`; `transcript`; the budget in `agent/budget.go`; `obs` |
| Meru the assistant | `router`, with the route rules in `agent/toolnouns.go`; `retrieve`; `memory` and the profile (`agent/profile.go`, `agent/recall.go`); recall of earlier chats (`agent/earlier.go`, `summarize`); web first (`agent/webfirst.go`, `agent/webnotes.go`); the fixes for small-model failures: `agent/honest.go`, the retries after bad output and empty replies in `agent/tools.go` and `agent/agent.go`, and `agent/notools.go` |

Four places tie the loop to Meru today:

- `agent.New` takes the whole `config.Config`. It reads the assistant's settings,
  such as `cfg.Index.Folders`, next to the loop's own `cfg.Agent.MaxRounds`.
- `Agent.Handle` takes an `rpc.Request` and emits `rpc.Event`, the socket
  protocol's types, so the loop speaks Meru's wire format.
- Both layers live in one package, `agent`, and run inside one `Handle` call:
  routing, search, recall and web first share the function that runs the tool
  rounds.
- `dispatch` asks for approval through `rpc.ApproveFunc` and writes each call to
  the store's `tool_calls` table as a `store.ToolCall`.

A script can drive the harness with no one at the keyboard.
`meru run --json "question"` writes each socket event as one JSON line on stdout
and exits non-zero when the turn fails. The command denies a tool call that asks
first, as any client with no one to ask does, and `dispatch` logs it as
`declined`. The command also writes the approval event to stdout, so the script
sees which call it refused. Every tool call still goes through `dispatch`; the
mode adds no second path.

---

## A question, end to end

The diagram below follows two turns of one conversation. The first turn searches
your files and calls a read-only tool. The second turn builds on the first and calls
a tool that changes something, so Meru asks you before it runs. Later sections
explain each step in detail.

![Sequence diagram of two turns in one session, across you, the meru client, the merud agent loop, the store, the fast model, the main model, dispatch and an MCP server. In turn 1 you ask what you agreed on the launch date in email this week. The loop appends your line to the transcript, the fast model picks the search+tools route, hybrid search returns chunks and memories, and the client gets the numbered sources. In a loop of at most 8 rounds, the main model asks for google.search_gmail_messages, dispatch runs it without asking and logs it, and the main model answers from the messages. In turn 2 you ask Meru to reply to the thread and confirm. The router picks the tools route and the main model asks for google.send_gmail_message, which sits in the confirm list, so dispatch asks you through the client. You approve once, dispatch sends the message and logs the call with your approval, and the main model answers.](docs/architecture/img/300-end-to-end.png)

*Figure 2. Each dashed frame is one run of the agent loop, and both turns go round it twice: `main` asks for a tool, gets the result, then answers. Turn 2 searches too: the `tools` route searches your files, because the router sends some questions about them there, and recall runs on every route. Only turn 2 stops for you, because `send_gmail_message` sits in the server's `confirm` list.*

### Who decides what

| Decision | Made by | How |
| --- | --- | --- |
| Answer directly, search, call tools, or search and call tools | the `fast` model (the router) | It reads the probability of each route letter from one decoded token, and falls back to search and tools when unsure. `merud` searches anyway when a `direct` question names an indexed folder, and adds tools when a question names a connected tool server. From v0.4, a separate short call picks the skills to load |
| What to search for | `merud`, with no model | The question as you typed it. A question with three or more words that aren't filler names its own subject and is searched alone. A shorter one is a follow-up: `merud` appends the session's latest earlier question that names a subject, because "and the one after that?" finds nothing on its own. It skips a question made only of filler words, such as "try the last question again". With `[index] retrieval = "agentic"` no search runs first, and the `main` model writes its own `search_files` queries |
| Which tools the model may use | you, in `config.toml` | Only tools in each server's or agent's `allow` list reach the model; the rest don't exist to it. Each `[[commands]]` entry is one tool. The built-in tools need no entry |
| Which tool to call, with what arguments | the `main` model | It reads each allowed tool's name, description and argument schema, as the MCP server, agent card or `[[commands]]` entry wrote them, and picks. For a local command it picks only the parameter values; the program and its flags come from config |
| Whether a call runs without asking | you, in `config.toml` and at the prompt | `dispatch` stops and asks when the tool is in its entry's `confirm` list, or its `[[commands]]` entry says `confirm = true`, unless you already approved that tool for this session. `configure` asks every time. `web_fetch` asks for a URL that no search result or question of yours gave, and before a download |
| When the turn ends | the `main` model, with caps | The turn ends when the model answers without calling a tool, or at the round cap (`[agent] max_rounds`, default 8). The last round offers no tools, so the model has to answer. A model that repeats a call twice loses its tools early. Each model call writes at most `[agent] max_output_tokens` (default 8,192, thinking included), and the whole turn has `[agent] turn_timeout` (default 5 minutes). A turn that hits a limit with no answer says sorry instead of going quiet |

The `tools` and `search+tools` routes offer every allowed tool. `search` offers
`datetime`, `about_meru`, the four read-only file tools, `read_file`, `list_folder`, `grep` and
`search_files`, and the local commands that don't ask first. A question such as "write about everything
in my work folder" lands on `search`, and ten excerpts can't cover a folder. So
does "what changed in the meru repo this week?" when `meru` is an indexed folder,
and a declared `git log` answers it. A command with `confirm = true` changes
something, so it waits for a tools route. `direct` offers `datetime` and
`about_meru`: both only read, and "what day is Christmas?" and "which model are
you?" route there. The model can't call a tool it hasn't seen, and the prompt
stays shorter.

Every route, `direct` included, also offers `web_search` and `web_fetch` while
`web_search` is on, which takes both `[builtin] tools` and `[web]
searxng_url`. A real session drove this, here with an invented song: asked
"what does the song maname maname sung by r. devi acvtually mean", typo and all, the router
sent the question `direct` at 0.966, where the model had no web tool. The model knew the song only in
part, called the `web-research` skill as if it were a tool, and told the user
it couldn't search the web. A question the router takes for general knowledge
is often one a model half knows. With the web tools on offer and one line in
the tools note (see [Agent loop](#agent-loop), step 2), the model can check a
fact before it answers. The two schemas are short. `web_fetch` without
`web_search` stays on the tools routes, since the model would have to guess a
URL.

### Approving a tool call

When `dispatch` reaches a tool in the `confirm` list, the client shows the tool's name
and arguments and offers the choices `merud` sends, at most these three:

| Choice | What happens |
| --- | --- |
| Approve once | This call runs. The next call to the same tool asks again. |
| Approve for this session | This call runs, and `dispatch` skips the prompt for that tool until the session ends. The approval covers the tool, whatever its arguments. |
| Deny | The call doesn't run. Its outcome is `declined`, and the model hears that you said no, so it can answer without the tool or ask you what to do. |

- **The question travels on the same socket.** `merud` sends the client an
  `approval` event with an ID, the call and the choices it offers, and holds the
  call. The client writes back one `Reply` line with that ID and a choice. A choice
  the approval didn't offer counts as deny.
- **Each choice is recorded.** `merud` appends an `approval` line to the transcript,
  between the call's `tool_call` and `tool_result` lines, and the `tool_calls` row
  keeps the choice:

  ```json
  {"ts":"2026-09-23T10:17:21Z","type":"approval","call_id":"call-1","server":"google","tool":"send_gmail_message","choice":"once","trace_id":"9c2e…"}
  ```

- **Built-in tools have their own section.** Built-ins belong to no server
  entry, so `config.toml` gives them one section with two lists:

  ```toml
  [builtin]
  tools   = ["configure", "datetime", "about_meru", "remember", "write_file",
             "read_file", "list_folder", "grep", "search_files",
             "web_search", "web_fetch"]   # all eleven, the default
  confirm = ["write_file"]   # the shipped default; add "remember" to approve each memory
  ```

  `tools` lists the built-ins the model may use. Take a name out and `merud`
  doesn't register that tool with `dispatch`: the model never sees it, and
  `meru tools` doesn't list it. `configure` can go too; `meru mcp add` still
  works without it. An unknown name stops `merud` at load, with the valid names
  in the message, and so does a `confirm` entry that `tools` doesn't list. A
  listed tool still needs what it works on: the file tools need `[index]
  folders`, `write_file` needs `[skills] output_dir`, and `web_search` needs
  `[web] searxng_url`. Without it the tool stays off, and `merud` logs one info
  line at startup that names the tool and the missing setting.

  The built-in tools are `configure`, which always asks, whatever `confirm` says
  (see [First run and setup](#first-run-and-setup)); `remember`, which saves a
  memory without asking unless you list it in `confirm`; `write_file`, which asks
  before each file it saves because the shipped list names it; the two web
  tools, `web_search` and `web_fetch` (see [Web search](#web-search)); the
  clock tool, `datetime`; `about_meru`, which reports this setup; and four
  read-only file tools. `web_search`, `datetime`, `about_meru` and the file
  tools run without asking unless you list them. `web_fetch` runs without asking for a URL a
  search result or your question gave, and asks otherwise:

  | Tool | What it returns |
  | --- | --- |
  | `read_file` | A file's whole text, 12,000 characters per call, with the offset for the next call. PDFs come page by page. A bare filename that no folder holds is looked up in `<output_dir>/attachments/`, so the model can pass the name a mail tool reports. |
  | `list_folder` | A folder's folders, then its files with size and modified date, 1 to 3 levels deep, at most 300 entries. |
  | `grep` | Every line that holds a word or an RE2 regular expression, as `path:line: text`, until 200 lines, 5 seconds or 20,000 files. |
  | `search_files` | The hybrid search of [Retrieval](#retrieval) for the model's own query: 8 excerpts by default, at most 20 and 14,000 characters, each numbered after the turn's other excerpts, with its path, heading and lines or page. Its excerpts join the turn's `sources` event. Offered when `[index] folders` is set. |
  | `datetime` | The current date and time with weekday and zone; the time in another zone; a date's weekday and how many days it is from today. Offered on every route, `direct` included, because "what day is Christmas?" routes direct. |
  | `about_meru` | `merud`'s own facts, in under 2,000 characters: the profile; the `main`, `fast` and `embed` models and what each does; the main model's capabilities, size, quantization and context length from Ollama's `/api/show`; the Ollama version, on a labelled line of its own; the computer; the `[index]` folders with file and chunk counts and the size of `meru.db`; each MCP server and A2A agent with its state and tool count; the local commands, built-in tools, skills and memory counts by kind; the output folder; and `merud`'s build. It copies names, counts and paths only; secrets, env and header values, a server's last error, a memory's text and transcript lines stay out. Offered on every route, as `datetime` is: asked "which model are you using?" on a `direct` turn, a model answered from its training with another company's name and no version. Its description asks the model to quote names, versions and numbers as the tool gives them: with the version inside a sentence, a model read `Ollama 0.34.0` and wrote "Ollama 0.44". |
  | `web_search` | Numbered web results from SearXNG: title, URL, a snippet and the date when known. Offered when `[web] searxng_url` is set, on every route, `direct` included. |
  | `web_fetch` | One public web page's text, 12,000 characters per call, like `read_file`; with a `prompt`, the `fast` model's answer from the page; with `save`, a file saved in `~/meru-output/downloads/`. Offered while `[builtin] tools` lists it: on every route while `web_search` is on too, and on the tools routes alone otherwise. |

  The file tools reach only the `[index] folders` and `[skills] output_dir`
  (`~/meru-output`), and they skip what the indexer skips (see
  [What stays out](#what-stays-out)): they never follow a symlink, and they
  refuse secret, hidden, ignored, binary and oversized files with the
  indexer's reason. The output folder holds what `write_file` wrote, what
  `web_fetch` downloaded, the mail attachments the `google` server saves
  (see [Adding an MCP server](#adding-an-mcp-server)) and the copies of files
  the user attaches in the desktop app (see [Desktop app](#desktop-app)). The file tools only
  read there; `write_file` keeps its own rules. Outside the output folder, they
  read nothing that search couldn't already put in the prompt. The indexer
  never indexes the output folder, so a web page or an attachment can't reach
  a later turn through search. `merud` leaves the file tools out when no
  `[index]` folder is listed.

  In `tool_calls` and the metrics, a built-in call has `kind = "builtin"` and
  `server = "meru"`.
- **Local commands ask per entry.** A `[[commands]]` entry with `confirm = true`
  asks before each run and offers all three choices; one without runs without
  asking. Read-only commands run freely, and anything that changes something
  should ask. A command call has `kind = "command"`, `server = "meru"` and
  `tool = "cmd.<name>"`, and its `tool_call` line, prompt and row show the argv
  it runs (see [Local commands](#local-commands)).
- **Session approvals stay inside the running `merud`.** `dispatch` keeps them in
  memory, per session and tool. They end with the session or with `merud`, and
  never reach `config.toml`. To stop Meru asking about a tool for good, remove it
  from the `confirm` list yourself; config stays the one place that grants lasting
  trust.
- **One-shot `meru "..."`** asks on standard error with the same choices, as
  `[o]nce [s]ession [d]eny`, so the answer on standard output stays clean. Its
  session ends with the answer, so "for this session" covers only this question.
  When standard input isn't a terminal (a script or a pipe), nobody can answer, so
  the client denies without asking and says so.
- **The desktop app** shows the choices as a card inside the answer, with the
  arguments laid out to read, focus on the answer that runs nothing, and Edit
  first, which says no and puts the call in the composer as a draft (see
  [Desktop app](#desktop-app)).
- **Nobody to ask means no.** A scheduled job, or a client that passed no way to
  ask, can't say yes, so `dispatch` ends every call that needs a yes as `declined`
  without a prompt. A job's output says which calls it skipped.

### How a conversation continues

- **A session is one transcript file.** `meru chat` keeps one session open until you
  quit. Each `meru "..."` one-shot starts a new session. The desktop app can reopen
  a past session and continue it.
- **Each turn starts with the session's history.** `merud` loads your earlier
  questions and Meru's earlier answers from this session, newest first, until the
  history budget is full. Older turns drop out of the prompt but stay in the
  transcript. From v0.4, search can find them there.
- **Earlier tool results stay out of the history.** The answer that used a result
  already carries what mattered from it, and raw results can run to thousands of
  tokens. The transcript keeps the full results.
- **The router sees the history too**, so it can tell that a follow-up like "reply
  to that thread and confirm I can make it" needs tools. It picks a route and nothing else;
  no model rewrites the follow-up.
- **A follow-up search adds an earlier question.** On the search routes, a short
  question (fewer than three words that aren't filler) gets the session's latest
  earlier question appended, so "and the one after that?" still finds the right
  files. A longer question names its own subject and is searched alone: appending
  a question about a trip to one about a work project filled the results with
  travel papers. It skips earlier questions made only
  of filler words, so "search again" after "try that again" still carries the
  subject from before them.
- **The `main` model resolves the reference.** It reads turn 1's answer in the
  history, which named the thread and the date agreed in it, and works out which
  thread "that thread" means. It reads answers, not the raw tool results behind
  them.

---

## Model tiers

Meru gives models three jobs, called tiers. Code depends on the tiers; `config.toml`
says which model fills each one.

| Tier | Job |
| --- | --- |
| `fast` | routing, classification, trivial answers |
| `main` | reasoning, writing the answer, choosing tools |
| `embed` | embeddings for the index and for queries |

Two profiles ship in `config.toml`. **`lite` is the default.**

| Tier | `lite` (default) | `full` |
| --- | --- | --- |
| `fast` | `hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M` (1.6 GB) | same |
| `main` | same model as `fast` | `qwen3.8:27b` (~18 GB, 4-bit) |
| `embed` | `nomic-embed-text` (~260 MB, 768 dims) | `qwen3-embedding:0.6b` (1024 dims) |
| Hardware | 16 GB of RAM; runs on a CPU, faster with a GPU or Apple silicon | Apple silicon with 32 GB (64 GB comfortable), or a GPU with ~24 GB of memory |

- **`lite`** uses two small models. It downloads in a couple of
  minutes and runs on almost any computer. One model fills both `fast` and `main`, so
  only two models sit in memory.
- **`full`** swaps in Qwen 3.8 27B, a dense model built for agent and tool work, and
  a stronger embedding model. On Apple silicon, Ollama also offers the
  `qwen3.8:27b-mlx` tag, which runs the same model on its MLX backend; we'll measure
  which tag is faster.

`merud` keeps each model loaded by asking Ollama for `keep_alive: -1`, and warms every
tier at startup. Ollama must allow enough models in memory at once
(`OLLAMA_MAX_LOADED_MODELS`, 3 for `full`).

The warm-up runs in two parts. Before the socket takes requests, `merud` sends
the `fast` model a one-token "hi" and the `embed` model one short text: the
router needs the first on every question, the store the second to open, and
both load in a second or two. The answer model, the one a set in use names
(see [Model sets](#model-sets)), loads next, in the background, while `merud`
already answers pings, settings and the router. Its warm-up prompt is the
system prompt a `direct` question gets, with the tools and skills it lists,
and a one-token answer with the set's think setting. A question that reaches
its first answer call before the load ends waits for that load, and a model
switch waits too, so Ollama never loads the model twice or unloads it while
it loads. The info log says `loading the answer model`, then `answer model
warm` with the time it took, or `couldn't load the answer model` with the
`ollama pull` command to run. A model that isn't pulled no longer stops
`merud`; the first question then fails with Ollama's reason.

A real session showed why the warm-up uses a real prompt. `merud` warmed
`qwen3.6:35b-a3b-mxfp8`, a 38 GB mixture-of-experts model, with "hi" before it
took questions; the load took 84 seconds, and Ollama kept the model, as
`keep_alive: -1` asks, with no reload in its log. Yet the first question, 26
minutes later, spent 2 minutes 17 seconds on a prompt of 1,349 tokens, and the
next question spent 6 seconds on 1,460. A mixture-of-experts model runs each
token through a few of its many experts, so a three-token prompt touched a
sliver of the weights, and the first long prompt read the rest from disk. A
warm-up prompt of a thousand tokens or more touches most experts before the
user asks, and Ollama keeps its opening for the first question to reuse.

Loading a 38 GB model at every start costs a minute or two of disk reads, in
the background. `keep_alive: -1` already says the user wants the models in
memory, and the other choice, a load on the first question, is the wait the
real session hit. So the warm-up stays on for every start.

The `fast` tier needs a runtime that reports log probabilities, because
[routing](#routing) reads them. Ollama added them in v0.12.11; `merud` reads
`/api/version` at startup and refuses to start on anything older, naming both
versions. Routing costs no extra model: the `fast` model is already loaded.

A new embedding model makes vectors of a different size, and every stored vector
goes stale. The store records the embedding model's name and vector size. When
either stops matching config, the store drops every vector and keeps the text, so
keyword search keeps working while the indexer re-embeds your files (see
[Storage](#storage)).

A 120B model at 4-bit needs ~60 GB, which leaves even the 64 GB development machine
no room and makes it swap. Meru won't support models that size.

### Models we tried

In September 2026 we tried four models as the `main` model, on the development
machine, an M4 Max with 64 GB, with Ollama 0.34.0 and MiniCPM5-2B as `fast`. The
test question asked how to use `btop`, and made the model search the web and
read pages over several rounds.

| Model | Size | What Ollama lists | What we saw |
| --- | --- | --- | --- |
| `hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M` | 1.6 GB | tools, thinking | about 55 s; made up command flags and misread what tools sent back |
| `qwen3.8:27b` (dense) | 17 GB | vision, tools, thinking | the best-grounded answer, with real flags from the man page, but about 5 minutes: 80,000 input tokens, and slow prompt reading |
| `qwen3.6:35b` (mixture of experts, about 3B of 36B parameters per token) | 23 GB | vision, tools, thinking | about 26 s, about 78 tokens a second; now and then a tool call Ollama can't read, or thinking and no text, which `merud` retries |
| `gemma3:12b` (12.2B parameters, 131,072-token context) | 8.1 GB | vision | answers direct and image questions; Ollama refuses it tools |

MiniCPM5-2B stays the `fast` model: it routes well and fast. `qwen3.6:35b` is the
answer model we use now. We didn't keep `qwen3.8:27b`, since a web question took
five minutes.

`config.KnownModels` lists the three we offer, MiniCPM5-2B, `gemma3:12b` and
`qwen3.6:35b`, with their sizes and a line each on what they did well and badly.
The desktop app's Settings, Models shows each one with its commands, which also
work in a terminal:

```sh
ollama pull hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M
ollama run hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M
ollama pull gemma3:12b
ollama run gemma3:12b
ollama pull qwen3.6:35b
ollama run qwen3.6:35b
```

`ollama run` opens a chat with the model in the terminal, to try it before Meru
uses it.

**Switching the answer model.** "Use for answers" on a model's card sends
`model_set` (see [Desktop app](#desktop-app)). `merud` refuses a model off the
list, and one `GET /api/tags` doesn't show, with the `ollama pull` to run. It
switches the way a [model set](#model-sets) switches, unloading the old answer
model first, then writes `[models] main` with the same safe write and lock as the
other settings. A pick in Settings is a setting, and every setting made there
lands in `config.toml`; the chat's `/model` is for comparing, so it waits for
`/model save`. The `fast` and `embed` models stay as they are. To set any other
model, name it in a model set, or edit `[models] main` in `config.toml` and
restart `merud`.

### Model sets

A model set names the models to switch between, so a switch names one thing
rather than three. Sets sit beside `[models]` in `config.toml`:

```toml
[[models.sets]]
name  = "qwen-moe"
main  = "qwen3.6:35b-a3b-mxfp8"
think = false

[[models.sets]]
name  = "gemma-moe"
main  = "gemma4:26b-mxfp8"
think = false
```

A set names `main`, and may name `fast` and `embed`. `think = false` sends
Ollama `think: false` with every answer call, so a model that reasons before
it answers doesn't lose on time to first token for that reason alone; left
out, each model does what it does by default. `config.Load` checks that each
set has a name of letters, digits, `.`, `-` and `_`, that no name repeats, and
that each set names a model. At startup `merud` logs a warning for each model
a set names that `GET /api/tags` doesn't list, and starts anyway: the model
can be pulled later. The set in use at startup is the first whose models match
the ones `merud` started with.

`/model <name>` in `meru chat`, `meru model use <name>` and **Use** in
Settings send `model_use`. The switch lasts until `merud` stops, which is what a
comparison wants; `/model save`, `meru model save` and **Make default** send
`model_save`, which writes the models in use to `[models]`. `model_use` runs in
this order, because `keep_alive: -1` keeps every model in memory, and three
main models of 38, 28 and 17 GB would push a 64 GB Mac into swap and make every
measurement after it noise:

1. Wait for the startup load of the answer model to end, if it still runs,
   then check that Ollama has the new main model, so a switch that can't
   finish unloads nothing.
2. Ask Ollama to drop the old answer model: `POST /api/generate` with the
   model, no prompt and `keep_alive: 0` (`OllamaEngine.Unload`).
3. Ask `/api/ps` every 100 ms until it no longer lists the old model, for at
   most 10 seconds.
4. Load the new model with a one-token question, so the first real question
   doesn't pay for the whole load.
5. Hand the model and its think setting to the agent, and only then reply.

Steps 2 and 3 are skipped when the old answer model is also the `fast` or
`embed` model, as in `lite`, since the router still needs it. A step that fails
comes back as an error that names it, such as "step 2 of 3: Ollama still holds
qwen3.6:35b in memory after 10s", and the answer model and the set in use stay
as they were. One switch runs at a time.

Only the main model changes while `merud` runs. The router, the skill pick, the
summarizer and the index read the `fast` and `embed` models at startup, so a
set's `fast` and `embed` models take effect once the set is saved and `merud`
restarts, and the reply says so. A set that changes `embed` needs `--rebuild`:
a new embedding model makes vectors of another size, every stored vector goes
stale, and `merud` re-embeds every file in the `[index]` folders at that
restart. A set that changes `fast` carries a warning, because the router needs
log probabilities and one token per route letter (see [Routing](#routing)).

**Which model wrote each answer.** Before an answer whose model differs from
the session's last one, the agent appends a `model_switch` line to the session
(see [Session transcripts](#session-transcripts)); a session's first answer gets
one with an empty `from`. The answer line records the model's time to first
token on the round that first wrote text, the time Ollama spent writing tokens,
its bad calls and whether the turn hit the cap. The `turns` table takes these
from the transcripts, so `/usage by model` rebuilds with the rest of `meru.db`:

```text
MODEL                  TURNS  TTFT p50  TOK/S  CALLS  BAD CALLS  CAPPED
qwen3.6:35b-a3b-mxfp8     41     820ms   24.1     63          2       1
gemma4:26b-mxfp8          38     610ms   31.7     59          7       0
```

TTFT p50 is the median time to first token. TOK/S divides the tokens written by
Ollama's `eval_duration`. CALLS counts tool calls. BAD CALLS counts what
`meru.model.malformed_calls` counts (see [Metrics](#metrics)). CAPPED counts
turns that used every round `agent.max_rounds` allows and wrote no text.

**A model without tools.** Ollama refuses a request that offers tools to a model
whose `/api/show` lacks `tools`, with a 400 that says the model "does not
support tools". Every route offers some tool, `datetime` and `about_meru` at
least, so with such a model every turn would fail. Before it builds the prompt,
the agent asks whether the answer model lists `tools`, through the engine's
cached `Details`. When it doesn't, the turn offers no tools, and when the route
would have offered more than the tools every route offers (those two, and the
web tools while `web_search` is on), or the scope is Web, a `notice` under the
answer names the model and says how to pick another. So `gemma3:12b` answers direct questions,
questions about images, and search questions from the excerpts search puts in
the prompt, and can't read mail, notes, the web or files on its own. The
Settings says so on its card, and `model_set` replies with a warning.

**Context length.** `OLLAMA_CONTEXT_LENGTH` sets how many tokens of context
Ollama loads each model with, and the `CONTEXT` column of `ollama ps` shows what
each loaded model got. For the 27B and 35B models we set it to 32768. On macOS,
where the Ollama app starts the server:

```sh
launchctl setenv OLLAMA_CONTEXT_LENGTH 32768
```

then quit and start Ollama. `launchctl setenv` lasts until the Mac restarts, so
run it again after each restart, or start `ollama serve` from a shell that
exports the variable.

---

## Engine layer

The interface has four methods:

```go
type Engine interface {
    Generate(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (Completion, error)
    Stream(ctx context.Context, msgs []Message, tools []ToolSpec, opts Options) (iter.Seq2[Delta, error], error)
    Embed(ctx context.Context, texts []string) ([]Vector, error)
    Info(ctx context.Context) (ModelInfo, error)
}
```

**`OllamaEngine` is the only implementation.** It talks to Ollama over HTTP on
loopback and refuses to start if the configured URL points anywhere else. It is the
codebase's only HTTP client for a model runtime, and it can't reach a cloud model.

Each model writes tool calls in its own format; Ollama converts them to structured
JSON, and the engine converts that JSON to Meru's own types. The agent loop never
sees a model-specific token. When Ollama's parser rejects what the model wrote,
it sends an `{"error": ...}` line part way through a stream it had answered
200 OK. `Stream` wraps any such line in `ErrModelOutput`, and the agent loop
tests for it with `errors.Is` to retry the round (see [Agent loop](#agent-loop)).
A failure before the stream starts, such as Ollama down or a model not pulled,
stays an `*APIError` or a transport error, and fails the turn as before. Each `Completion` also carries Ollama's counters
(`prompt_eval_count`, `eval_count`, `load_duration`, `prompt_eval_duration`,
`eval_duration`), which feed [Observability](#observability).

For [routing](#routing), `Options` gains two fields, `LogProbs` and `TopLogProbs`,
and `Completion` gains `LogProbs`: for each generated position, the chosen token and
the alternatives the model weighed, each with its log probability. Both default to
off, so other callers see no change, and the interface keeps its four methods.

Two more methods sit outside the interface, for the model ops alone.
`Unload` drops a model from Ollama's memory with a `POST /api/generate` that
sets `keep_alive: 0`, and `Pulled` reads `GET /api/tags` with each model's size
on disk.

For images, `Message` gains `Images`, the raw bytes of each image on a user
message. `OllamaEngine` sends them as the message's `images` array in
`/api/chat`, which Ollama documents as base64 strings; Go's `encoding/json`
writes a `[]byte` that way with no code of ours. Only a model with the `vision`
capability can read them, and Ollama's `POST /api/show` lists a model's
capabilities, such as `["completion", "vision", "tools", "thinking"]`.
`OllamaEngine.Details` asks it for the capabilities, the size in parameters,
the quantization and the context length, and keeps each model's answer for the
life of the engine; a failed call keeps nothing. `Capabilities` reads the list
through `Details`, and `Pulled` reads `GET /api/tags`, the models Ollama has on
disk. All three sit outside the interface, which keeps its four methods: `merud`
hands the agent a check built on `Capabilities` for image turns and another for
whether the answer model can take tools; `about_meru` and the models in Settings
need `Details`, and only the models in Settings need `Pulled`. The "ollama http"
debug line counts a request's images; no log line or span holds their bytes.

Two engines may come later, behind the same interface:

- **`LlamaCppEngine`** would compile llama.cpp into `merud` through cgo, using the
  GPU (Metal on a Mac, CUDA on NVIDIA). Meru would then need no Ollama install, but it would have
  to parse each model's tool-call format itself.
- **`MLXEngine`**, if MLX gets usable C or Go bindings and runs faster enough than
  llama.cpp to justify a second backend.

---

## Agent loop

We write the agent loop ourselves; we looked at Eino and ADK Go and chose not to use
a framework. The loop is small, and every rule Meru enforces lives inside it: the
allowlist, the audit log, the context budget and confirmation prompts. We want that
code where we can read it, not inside another project's callbacks. The only
third-party code in the loop is the official MCP Go SDK and the A2A Go SDK.

A turn has five steps. [A question, end to end](#a-question-end-to-end) shows them in
order.

1. **Route.** The `fast` model picks one of four routes: answer directly, search your
   files first (RAG, retrieval-augmented generation), call tools, or search and call
   tools. It picks by classification, reading one decoded token's probabilities (see
   [Routing](#routing)). On the search routes, `merud` then searches your files for
   the question, with an earlier question appended on a follow-up. No model
   rewrites the query. The `tools` route searches too: the router sends some
   questions about your files there, and an answer from the files beats one from
   the model alone. Four rules then adjust the route (see [Routing](#routing)):
   a `direct` question that names an indexed folder becomes `search`, a
   question that names a connected tool server gets tools, and so does a
   question that says "remember", asks for the web, gives a URL, or names
   what a connected server's tools act on ("email" for Gmail's tools). The
   same signs skip the search on a `tools` turn: a web, mail or notes-app question gets no
   excerpts, which crowd the answer and cost time, and the file tools stay on
   offer. `search` and `search+tools` always search. From v0.4, a separate
   short call picks the skills to load, beside the router. A picked skill
   brings the tools its `allowed-tools` key names (see [Skills](#skills)):
   when the route lacks one that config allows, `direct` becomes `tools` and
   `search` becomes `search+tools`. The router alone can't catch this: "help
   me understand btop with some simple commands" gave `direct` 0.761 and
   `tools` 0.022, while the skill pick chose `web-research`, whose first step
   is `web_search`. Every route now offers the web tools while `web_search` is
   on, so `web-research` widens a route only when `web_search` is off and
   `web_fetch` isn't. A skill that adds only web tools leaves a `direct` turn
   unsearched; one that adds the file tools makes it a file turn. Last,
   some questions go to the web before the model's first round (see
   [Web first](#web-first)): one that asks for the web or gives a URL, and,
   on a turn that searched your files, one that names a thing the files
   don't cover. `merud` runs `web_search` or `web_fetch` itself, through
   `dispatch`, and the route gains the tools when it lacked them.
2. **Build the context.** The system prompt puts the parts that stay the same
   from turn to turn first: the configured prompt, the rule that "I" means the
   user, the rule that the model never claims an action no tool took (see
   [Claims no tool backs](#claims-no-tool-backs)), today's date with a pointer to the `datetime` tool (a model knows only its
   training data, so without the date a trip that ended last week reads as one
   still to come; the time of day goes through the tool, since it changes every
   minute and would cost Ollama's reuse), one line on your computer that `merud`
   reads at startup (the OS and version, the processor, memory, the shell and the
   time zone, and no host or user name; without it the model answered a GPU
   question on an Apple silicon Mac with `nvidia-smi`), your profile, the note on your folders, a line on what
   Meru's own tools can change (with, when config allows `about_meru`, a line
   that sends questions about Meru itself, such as which model answers, to that
   tool instead of the model's training), the tools note, and the list of skills. The parts each question changes come after: recalled memories, the
   picked skills' instructions, file excerpts with earlier conversations, and
   what the web-first step found.
   Ollama reuses its work on a prompt's opening until the first token that
   differs, so this order lets a follow-up reprocess only the changing parts, the
   history and the question. A turn about your files (every turn that searches
   first, or would in `agentic` mode) also gets a note on the file tools, between
   the two groups: other turns share the whole opening with it and keep a shorter
   prompt. Each part has its own cap, in characters (a token is
   about four):

   | Part | Cap | Past the cap |
   | --- | --- | --- |
   | Profile (`me`, `preferences`) | 2,000 | the oldest facts drop |
   | Recalled memories | 2,400 | the lowest-ranked drop |
   | Skill instructions | 12,000 | the first skill stays whole; the second is cut |
   | Earlier conversations | 2,400 | the lowest-ranked drop |
   | From the web | 8,000 | each call's result is cut to an equal share |
   | History | 8,000 | the oldest turns drop, each question with its answer; a turn's web notes take at most 1,500 |

   File excerpts need no cap of their own: a search keeps 10 chunks of about 500
   tokens. The caps keep one part from crowding out the others; a `lite` turn
   uses well under a tenth of the model's 131k-token window. On the `tools` and
   `search+tools` routes the model also gets the allowed tools' schemas; on
   `search` it gets the four file tools' schemas and those of the local
   commands that don't ask, with a note that says it may run the `cmd.` tools.
   Every route gets the schemas of `datetime` and `about_meru`, and of
   `web_search` and `web_fetch` while `web_search` is on.
   The note on the file tools says it may read whole files, list folders, grep
   and search again when the excerpts fall short. A turn that offers
   `web_search`, on any route, adds a line to the tools note: "When you aren't
   sure of a fact, such as a song, a film, a book, a person, a product, a place
   or anything that may have changed, call web_search before you answer, and
   cite the pages you use. Never tell the user you can't search the web." The
   skills list then names only the skills whose tools the turn offers, or that
   name no tools, so it never points the model at a tool it lacks. Both depend
   only on the tools offered, and in Auto every route offers the same web tools,
   so both keep their place among the parts that stay the same. A turn that
   offers both `web_search` and a file tool adds a second line: when the files
   don't answer, call `web_search` before answering from memory, and never make
   up a command's flags or a version number. It too depends only on the tools
   offered. On a turn that offers `web_search`, a search that finds nothing tells the model to use `web_search` for how
   to use a program and for facts that change, where it would otherwise say
   "answer from what you know". A real turn drove both: "help me understand
   btop with some simple commands" grepped the user's folders, found only pages
   that name btop in passing, and answered with flags btop doesn't have.
   `meru.context.tokens` records each part's size per turn, to tune the caps by.
   On the `tools` and `search+tools` routes, the loop first asks each connected MCP server
   for its tools again and gives each server that isn't connected one try, then
   lists the tools (see [MCP](#mcp)).
3. **Call `main`.** A turn that gets here while `merud` still loads the answer
   model at startup waits for that load (see [Model tiers](#model-tiers)).
   Stream text to the client as it arrives. Ollama sends each tool
   call whole, in a chunk of its own, and the loop collects them. It tells the
   client about each call with a `tool_call` event. Each call carries
   `num_predict = [agent] max_output_tokens` (default 8,192). Ollama counts a
   thinking model's hidden reasoning against it, so a model that would think
   for minutes stops with `done_reason = "length"`. Ollama offers no separate
   budget for thinking: `think` only turns it on or off.
4. **Dispatch tools.** Every call goes through one function, `dispatch`, in this
   order:
   1. **Allowlist.** A tool that no backend offers is `denied`. It doesn't run,
      but it still gets a `tool_call` line, a `tool_result` line and a
      `tool_calls` row, so a model that keeps reaching for forbidden tools shows
      up in the log. A call named after a skill, such as `web-research`, gets
      a result that names the tools to call instead (see [Skills](#skills)).
   2. **Transcript.** The `tool_call` line goes in before the call runs. If it
      can't be written, the call doesn't run.
   3. **Confirm.** Each tool asks never, asks unless approved for this session, or
      asks every time (`configure` only). If you say no, or nobody can be asked,
      the outcome is `declined`.
   4. **Call.** The MCP server, A2A agent, built-in tool or local command runs,
      under its own timeout. The outcome is `ok`, `error`, `timeout` or
      `cancelled`. In an MCP or A2A result, `dispatch` replaces each run of
      2,000 or more base64 characters, on one line or wrapped at one width,
      with a note that gives its length. A model can't read base64: in a real
      turn one PDF came back as 109,068 characters of it. A Gmail attachment
      ID, about 400 characters, stays, because the model passes it back. When
      the call ends `ok` and its result names a mail attachment the call
      saved, `dispatch` then adds the file's path and text to the result (see
      [Adding an MCP server](#adding-an-mcp-server)).
   5. **Record.** `dispatch` removes secret values from the arguments, the result
      and any error text, then writes the `tool_result` line, the `tool_calls` row,
      the metrics and the `meru.dispatch` span. The model reads up to 16,000
      characters of the result, with a note when `dispatch` cut it; the transcript
      and the row keep 4,000 of the same text, attachment included.

   No other code path reaches a server, agent, built-in tool or local command. The calls of one
   round run at the same time (`errgroup`), and each sends its `tool_result` event
   to the client as it ends. A denied or declined call still reaches the model as
   a result, so it can answer without the tool or ask you what to do.
5. **Repeat** from step 3 with the tool results, until the model answers without
   calling a tool or the turn reaches `[agent] max_rounds` model calls (default 8).
   The last allowed round offers no tools, so the model has to answer with what it
   has. A call with the same name and arguments (compared as canonical JSON) as
   one the turn already ran doesn't run again: the model gets the earlier result
   with a note that it repeated itself. A repeat never reaches `dispatch`, so it
   is no tool call: no `tool_call` event, transcript line or `tool_calls` row,
   only a debug log line and the turn span's `meru.turn.repeated_calls`. After
   the second repeat, later rounds offer no tools. A 2B model offered only
   `datetime` once called it with no arguments in all eight rounds.

   A round can end with no text and no tool call, when a thinking model spends
   it all on hidden reasoning. When the turn has a round left and the round
   didn't stop at `max_output_tokens`, `merud` asks once more: the same
   messages plus a user message telling the model to answer now in plain text
   from the tool results, with no tools offered. The empty round's thinking
   stays out, and the nudge lives only in that call, never in the transcript. A
   turn retries once; a second empty reply ends it `gave_up`. The turn span
   records the retry as `meru.turn.empty_retry`. In a real turn a thinking
   model got eight good `web_search` results, then wrote 205 tokens of thinking
   and nothing else, and the user read the sorry with seven rounds left.

   A round can also fail part way, when Ollama can't parse what the model
   wrote. Ollama then sends an `{"error": ...}` line in the stream after its
   200 OK, and the engine returns `ErrModelOutput` (see
   [Engine layer](#engine-layer)). When the turn has a round and time left,
   `merud` runs the round once more with the same tools, plus a user message:
   "Your last tool call didn't parse, so it didn't run. Call the tool again
   with valid arguments, or answer in plain text." Text the failed round
   streamed stays on screen, and a blank line separates it from the retry's
   text. The nudge lives only in that call, never in the transcript. This
   retry counts apart from the empty-reply one, since each fixes a different
   slip; `max_rounds` and `turn_timeout` bound both. A second failure, or a
   first one with no round left, ends the turn `bad_output`. Ollama's error
   text goes to `merud.log` at warn with the model's name, never to the chat.
   The turn span records the retry as `meru.turn.output_retry`. In a real
   turn `qwen3.6:35b` on Ollama 0.34 wrote a malformed tool call, and the
   user read `XML syntax error on line 8: element <function> closed by
   </parameter>` as the answer.

A turn has `[agent] turn_timeout` (default `"5m"`) from question to answer,
waits for your approvals included. When the time runs out, `merud` cancels the
turn's context, which stops the Ollama request and any tool calls. A turn that
ends without a full answer still answers, with an outcome of its own:

| Outcome | When | What you read |
| --- | --- | --- |
| `timeout` | `turn_timeout` ran out | the text so far and a note that it stopped, or a sorry |
| `cut_off` | the last call hit `max_output_tokens` | the text so far and a note that it stopped, or a sorry |
| `gave_up` | the rounds ended with no text, such as only tool calls, even after the one retry | "Sorry, I couldn't answer that. Try asking again, or rephrase the question." |
| `bad_output` | Ollama couldn't parse the model's output, even after the one retry | any text so far, then "The model wrote a tool call that Ollama couldn't read, twice. Try asking again, or rephrase the question." (without "twice" when no round was left to retry) |
| `no_vision` | the question carried images and the main model lacks the `vision` capability | "<model> can't look at images, so Meru didn't send it this question. Pick a model with vision for [models] main in config.toml. To check a model, run `ollama show <model>` and look for "vision" under Capabilities." |

The words go out as ordinary `token` events, so both clients show them as the
answer. The outcome goes on the `meru.turn` span, the `turn` log line and
`meru.turn.duration`, and in the assistant line's `outcome` field in the
transcript.

`dispatch` sees the tools through one interface, `Backend`, with four
implementations in a fixed order: the built-in tools, the local commands, the MCP
client pool, then the A2A client. When two backends offer the same name, the
first keeps it, so no MCP server can shadow `configure` or a `cmd.` tool.

A `context.Context` runs through the whole turn. If the client disconnects or you
press Ctrl-C, `merud` cancels the turn, with no sorry: generation stops, in-flight tool calls are
dropped, and their `tool_calls` rows record the cancellation.

### Questions with images

A question from the desktop app can carry up to five images (see
[Desktop app](#desktop-app)). `merud` reads each one from the uploads folder
before the turn starts, then asks `OllamaEngine.Capabilities` whether the `main`
model lists `vision`. When it doesn't, the turn calls no model and answers with
the `no_vision` message above; the message names the model from config and says
how to check another, and names none of its own, since the list of models with
vision changes with each release. When it does:

- **No router.** The `fast` model reads text alone, so its guess would ignore
  what the question is about. The turn takes the route its scope gives, as a
  scoped turn does, and `direct` in Auto, with the tools `direct` offers. A
  scope still holds: My files searches first, and Mail and calendar and Web
  offer their tools, with the images on the question all the same. Skills are
  still picked from the question's text.
- **This turn only.** The images go on the question's user message, and on no
  other. The transcript's user line keeps their full paths in an `images`
  field, never their bytes, and a later turn's history carries a text note per
  image, `[image: receipt.jpg]`, after that question. Each image costs the model
  hundreds of tokens and seconds of work, and the answer that looked at it sits
  in the history already, so sending it again would spend the context for
  little.
- **Counts, not content.** The `meru.turn` span gets `meru.turn.images`, the
  turn's info line an `images` count, and a debug line the count and total
  bytes. No span or log line holds an image.

`meru` and `meru chat` send no images: a terminal has no picker or drop, and
the one-shot client would need a second request to copy the file first.

### Claims no tool backs

A model can say it did something that no tool did. In a real session a user
asked Meru to save a Go program, and `write_file` saved it under
`~/meru-output/hello-go/`. The user then asked Meru to move that folder to
another folder. The router picked `search`, which offers only the file tools; the
model called no tool and answered "Done. It's now at …/repos/hello-go/". Meru has
no tool that moves a file, and nothing moved. The user learned it only on finding
the folder missing.

Two parts guard against this. The first is in the prompt. Every turn carries
this rule after the one on who "I" is:

> Never say you did something, such as saved, moved, sent, deleted, changed or
> scheduled, unless a tool call in this turn did it and succeeded. When none of
> your tools can do what the user asks, say so first, then offer what you can do.

and, after the note on your folders, a line built from the tools config allows:

> You can write files only inside ~/meru-output, with write_file. Meru's own
> tools can't move, rename or delete files, or run programs other than
> cmd.git-log.

The folder comes from `[skills] output_dir`, and the line says "You can't write
files" when `write_file` is off. The `cmd.` names are the `[[commands]]` entries,
sorted. The line lists what config allows, not what one route offers, so it
stays the same from turn to turn and Ollama reuses its work on it. It speaks only
of Meru's own tools: an MCP tool such as a mail server's send tool says what it
changes in its own description.

The second part checks the answer. A turn that ended without a full answer,
`bad_output` among them, gets no check, since it ends in Meru's own message.
When a turn ends with a full answer and no tool call in it ended `ok`, `merud` reads the answer sentence by sentence,
leaving out code blocks, and looks for a claim of a finished action:

| Rule | Matches |
| --- | --- |
| a sentence that opens with "Done" and a stop | "Done.", "**All done!**" |
| it, they, or a file, folder, note, email or event "is now at/in" | "It's now at ~/Projects/garden" |
| "has/have been" and an action | "The file has been saved to ~/Projects" |
| "I", "I've" or "I have", with one word allowed between, and moved, saved, sent, deleted, removed, renamed, copied or scheduled | "I've moved the folder", "I just sent the email" |
| the same with created, updated, wrote or written, when the sentence names a file, folder, note, email, event, reminder, calendar or a path | "I've created the file ~/notes/plan.md" |

A sentence that ends in "?", or that holds "if", "want me", "I can", "I'll", "let
me", "not", "never" or any "n't", counts as a question, an offer or a denial, and
never as a claim. Case doesn't matter, and every pattern matches whole words.

When a claim turns up, `merud` sends a `notice` event after the last `token` and
before `done`, with the text "Meru didn't run any tool for this answer, so
nothing changed on your computer." `meru` prints it on stderr as a dim `note:`
line after the answer; `meru chat` draws it in amber under the answer. The
assistant line keeps it in a `notice` field, and the session's history hands it
to the model in square brackets after that answer, so the next turn doesn't
build on the claim. The turn span gets `meru.turn.unbacked_claim = true` and the
`turn` log line `unbacked_claim=true`; neither carries the text.

The check reads plain English patterns, so it is easy to test and to explain,
and it gets some cases wrong. It misses a claim in words no rule lists, such as
"your folder lives in ~/Projects now", and any claim in another language. It
flags a "Done." at the top of a poem you asked for, where the note is true but
not needed. It stays quiet on a turn where any tool call succeeded, even one that
only read a file, because it can't tell which call backs which claim.

### Routing

A small model asked to write its route as JSON (JavaScript Object Notation) fails in
three ways: it writes broken JSON, invents a fifth route, or answers wrong with no
sign that it was unsure. So the router doesn't ask for text. It treats the route as a
classification and reads the answer from the model's probabilities:

1. The prompt opens with four lettered options (A = answer directly, B = search,
   C = tools, D = search and tools) and two examples per route. Each option's
   description names the questions that belong to it and the ones that don't.
   Option B also names the folders in `[index] folders` and says that questions
   about projects kept there, by name, are B. Without that line the model can't
   tell that "meru" in "what database does meru use" is your own project.
   Option C names what you have connected, and says that questions about these,
   by name, are C, or D when they also need your notes. Each MCP server and A2A
   agent that allows a tool shows as its name with up to five nouns from its
   allowed tool names, such as `google (gmail, message, thread, event, drive)`;
   each `[[commands]]` entry shows by name, such as `git-log`; and web search
   shows as `web search`. `merud` builds the list from config at startup and
   again on each reload, not per turn. The folders come from the list the
   indexer holds, which the desktop app can change while `merud` runs. The
   session history and the question come last, and the prompt ends with
   `Answer: `. The fixed part, folders and connected tools included, stays the
   same from turn to turn, so Ollama reuses its work on it from the previous turn.
2. `merud` asks Ollama's `/api/chat` for one token with log probabilities
   (`num_predict = 1`, `logprobs = true`, `top_logprobs = 20`) and with thinking
   off (`think = false`). A thinking model such as MiniCPM5 otherwise spends its
   one token starting its hidden reasoning, and no letter comes back.
3. The router keeps the alternatives whose text is one of the four letters, turns
   each log probability back into a probability, divides by a fitted temperature,
   and normalizes the four so they sum to 1.
4. The most likely letter is the route, and its probability is the confidence.

![Flow chart of the router. The prompt lists four lettered routes, A answer, B search, C tools and D search+tools, then examples and the turn, and ends with Answer:. The fast model in Ollama returns one token with log probabilities, with num_predict = 1, logprobs = true and top_logprobs = 20. The router keeps the letters A to D, applies the temperature and scales the four to sum to 1. When the top probability reaches min_confidence and at least two letters came back, the route is the most likely letter, with outcome ok. Otherwise the router falls back to search+tools, with outcome low_confidence, or degraded when fewer than two letters came back.](docs/architecture/img/300-routing.png)

*Figure 3. The router reads a route instead of asking for one. It never parses text the model wrote, and when the four probabilities don't pick out one letter, it takes the route that can't leave a turn short of context.*

A question sent with a scope other than `auto`, from the desktop app's "Where
Meru looks" switch, skips the router: the scope sets the route and the tools (see
[Desktop app](#desktop-app)).

The model decodes one token, so routing costs a prompt evaluation and nothing more,
and it can't name a route that doesn't exist. When the confidence falls below
`min_confidence`, or fewer than two letters appear at all, the router takes the
fallback route, `search+tools`. That's the one route that can't fail a turn for
lack of context: an unsure router spends tokens rather than guesses.

```toml
[router]
top_logprobs     = 20             # Ollama's cap
temperature      = 1.25           # fitted by `make router-eval`
min_confidence   = 0.45           # below this, take the fallback
fallback         = "search+tools"
decision         = "top"          # or "marginal"
search_threshold = 0.30           # "marginal" only
tools_threshold  = 0.25           # "marginal" only
```

`decision` picks how the four probabilities become a route. The default, `top`,
takes the most likely letter as described above. `marginal` asks two yes/no
questions instead. B and D both search, so P(B) + P(D) is the chance the turn
needs a search; C and D both call tools, so P(C) + P(D) is the chance it needs
tools. Each sum that reaches its threshold adds its half, and a turn with two
yeses gets `search+tools`. `marginal` needs no confidence floor, so it takes the
fallback only when fewer than two letters appear. Take "what was the last email I
sent?" at search 0.473, tools 0.260, search+tools 0.218: `top` picked search and
offered no mail tools, though the two tool letters held 0.478. `marginal`
picks `search+tools`. With the prompt naming the connected tools, `marginal` at
0.30 and 0.25 missed a needed search or tool on 3 of 46 held-out turns against 7
for `top`, and matched the label on 36 against 34. Both offered unneeded tools on 3
turns; `marginal` added an unneeded search on 6 against 5. The default stays
`top` until labelled turns from real transcripts confirm the gain.
[docs/fast-router.md](docs/fast-router.md#decision-rules) has the numbers.

The router lives in `internal/router` as one function, `Decide`, which returns the
route, its confidence, the full distribution and an outcome (`ok`,
`low_confidence` or `degraded`). A model that answers unclearly isn't an error;
`Decide` returns the fallback and says why.

Five rules override the router, in this order. The first: when it picks `direct` and
the question names an indexed folder as a whole word, such as "meru" for
`~/repos/meru`, the agent loop changes the route to `search`. Even with the folders
in the prompt, the router sent "what database does Meru use to store its index?" to
`direct` at 0.621, and the model made up an answer. A wrong guess costs one search
of about 50 ms, and the model uses only the excerpts that help. The turn's route
event, log line and span show `search`; the `meru.route` span and metric still
record what the router chose. The rule matches the last part of each folder path, in
any case, and skips names under three letters. "hey meru, what's the capital of
France" searches too, because the assistant shares its name with the folder.

The second: when the question names a connected MCP server or A2A agent as a whole
word, and the route is `direct` or `search`, the agent loop adds the rest. `direct` becomes
`tools`, and `search` becomes `search+tools`. The names come from the tools
`dispatch` offers, such as "obsidian" from `obsidian.obsidian_simple_search` and "research"
from `a2a.research.summarize`, and the loop reads them on each turn, because
`configure` can add a server while `merud` runs. Built-in tools don't count: their
owner is `meru`, the assistant's own name, which would match most questions. The
router sent "Search my Obsidian vault for notes mentioning 'AI'" to `search` at
0.65; with no tools offered, the model said it couldn't search the vault. With the
rule it listed the vaults, searched one and answered. A wrong guess costs a prompt
that holds the tool schemas.

The third: when the question holds "remember" as a whole word and the route is
`direct` or `search`, the loop adds the rest, so the model can call `remember`. "Remember that I
work on the registry team" reads like chit-chat to the router, and a `direct` turn
would answer "noted" and save nothing.

The fourth: when the question asks for the web and `web_search` exists, or gives
an http or https URL and `web_fetch` exists, and the route is `direct` or
`search`, the loop adds the rest. A question asks for the web when it holds one
of these phrases as whole words, in any case: "web", "internet", "online", "the
net", "web search", "internet search", "search the web", "search the internet",
"search online", "look up", "look it up" (or "this", "that", "them"), "google
it" (or "this", "that", "for"), "do research", "do some research", "deep
research", and "research about" (or "this", "it", "that"). "research" alone
isn't on the list, since "summarise my research folder" is about your files,
and neither is "google" alone, the name of the server behind Gmail. The router
sent "Search the web: what is SearXNG?" to `direct`, and the model, with no
tools, wrote a tool call as plain text. Such a question also goes to the web
first (see [Web first](#web-first)). Since every route now offers the web tools
while `web_search` is on, a factual question the router sends `direct` without
asking for the web can still look things up; the router itself didn't change.

The fifth: when a word in the question matches something a connected server's
tools act on, the loop adds tools. The nouns come from the tool names, singular
and lower case, minus common verbs and words such as "file": `gmail`, `message`,
`calendar`, `event`, `drive`, `note`. A word matches when it equals a noun or ends
in the same four or more letters, so "email" matches `gmail`. The router sent
"what was the last email I sent?" to `search`, which offers no server's tools, and
the model grepped the user's files.

Naming the connected tools in the prompt took over much of the fifth rule's
work. On the labelled set, with the catalog's `google` and `obsidian` servers, a
`git-log` command and web search connected, the router now sends "what was the
last email I sent?", "how many unread emails do I have" and "what does the
onboarding doc in drive say about laptops" to `tools` by itself. The fifth rule
still rescues one question there, "reply to the last email from my landlord
saying thanks", and the second rule still catches "search my obsidian vault for
notes about sourdough", which the router sends to `search`. All five rules stay.

`make router-eval` scores the router against the local Ollama on a labelled set of
153 questions, 46 of them held out, and fits the temperature. It also compares
the two `decision` rules on the same answers. With the connected list, `top`
got 131 of the 153 right against 126 without it, and missed a needed search or
tool on 15 against 20; it offered unneeded tools on 5 against 2. The list adds
about 48 tokens to the prompt. At 1.25 the
probabilities sit close to calibrated, so `min_confidence = 0.45` means what it
says. Refit after any change to the prompt, the examples or the `fast` model.

The full design, with the prompt contract, tests and calibration results, is in
[docs/fast-router.md](docs/fast-router.md).

---

## Storage

Meru keeps two kinds of data, with one rule between them: **files hold the truth,
and the database indexes them.** Delete `meru.db` and `merud` rebuilds it.

![Diagram with the files that hold the truth on the left and meru.db on the right. The indexer chunks and embeds your folders into documents, chunks, chunk_fts and chunk_vec. Replaying the session transcripts fills tool_calls and, from v0.4, sessions, messages, message_fts and session_vec. The indexer embeds the memory files into memories, memory_fts and memory_vec. Reading config.toml and skills fills meta and, from v0.5, jobs and job_runs. Delete meru.db and merud rebuilds every table from the files.](docs/architecture/img/300-storage.png)

*Figure 4. Every arrow runs left to right, and every table on the right has a file behind it, memories included. v0.2 built `documents`, `chunks`, `chunk_fts`, `chunk_vec` and `meta`, and v0.3 added `tool_calls`; the rest arrive with later milestones, as the table below marks. Edit a memory file by hand and the indexer picks up the change the same way it does for your notes.*

### Session transcripts

Each session is one JSON Lines (JSONL) file: one JSON object per line, one line per
event. `merud` appends a line as each event happens and never rewrites old ones.

```text
~/.meru/sessions/2026/09/2026-09-23T101502-7f3a.jsonl
```

```json
{"ts":"2026-09-23T10:15:02Z","type":"user","text":"what did we agree on the launch date in email this week?","trace_id":"4bf9…"}
{"ts":"2026-09-23T10:15:03Z","type":"tool_call","call_id":"call-1","kind":"mcp","server":"google","tool":"search_gmail_messages","args":{"query":"launch date"},"trace_id":"4bf9…"}
{"ts":"2026-09-23T10:15:04Z","type":"tool_result","call_id":"call-1","outcome":"ok","ok":true,"ms":812,"result":"Re: Launch date, from Sam…","trace_id":"4bf9…"}
{"ts":"2026-09-23T10:15:09Z","type":"assistant","text":"Sam and Priya agreed on 14 October…","tokens_in":2310,"tokens_out":188,"trace_id":"4bf9…"}
{"ts":"2026-09-23T10:31:40Z","type":"summary","text":"Found the launch date agreed in email: 14 October, in the thread with Sam."}
```

```json
{"ts":"2026-09-27T09:14:02Z","type":"model_switch","tier":"main","from":"qwen3.6:35b-a3b-mxfp8","to":"gemma4:26b-mxfp8"}
```

A `model_switch` line comes before the first answer a new main model writes in
a session, so the transcript alone says which model wrote each answer. An
assistant line also carries `ttft_ms`, `eval_ms`, `bad_calls` and `capped` (see
[Model sets](#model-sets)).

A `tool_call` line gets `"caller":"meru"` when `merud` made the call itself
before the model's first round (see [Web first](#web-first)); a call the model
asked for has no `caller`. An assistant line whose turn read the web gets a
`web` field: up to five notes, each a URL, a title and a gist of at most 300
characters, pages before search results, 1,500 characters in all. History hands
them to the model after the answer, one line each:

```text
[from the web: Acme Flow https://acme.example/flow — Acme Flow moves notes between apps.]
```

A user line gets an `images` field, the full paths of the copies in the uploads
folder, only when the question carried images; the bytes stay in the files. An
assistant line gets an `outcome` field only when the turn ended without a
full answer: `timeout`, `cut_off`, `gave_up`, `bad_output` or `no_vision` (see
[Agent loop](#agent-loop)). It gets a `notice` field only when the answer
claimed an action and no tool call in the turn succeeded (see
[Claims no tool backs](#claims-no-tool-backs)).

A tool call's lines share a `call_id`, because the calls of one round run at the
same time and their lines can interleave. An `approval` line sits between the two
when the call asked you first. `result` holds the first 4,000 characters of what
the tool returned, with secrets removed.

You can `grep`, `tail -f` or back up these files with no special tools, and a crash
loses at most the line being written. The database keeps a copy for search and for
`meru log`.

### The database

`~/.meru/meru.db` is one SQLite file, opened through `ncruces/go-sqlite3`. FTS5,
SQLite's built-in full-text search, handles keywords. Vectors sit in plain tables,
one row per vector, and vec1, SQLite's own vector extension, supplies the distance
function that compares them. `merud` creates `meru.db` and its `-wal` and `-shm`
files with mode `0600`, so only you can read them.

| Table | Holds | Rebuilt from |
| --- | --- | --- |
| `documents` | indexed source files: path, mtime, hash, type | your folders |
| `chunks` | pieces of each file's text, plus metadata and the parent document | your folders |
| `chunk_vec` | one vector per chunk, a blob of 32-bit floats scaled to length 1 | chunks, re-embedded |
| `chunk_fts` | keyword index over chunks (FTS5) | chunks |
| `sessions` / `messages` (v0.4) | every session and message, plus each session's summary, for context and `meru log` | `sessions/*.jsonl` |
| `session_vec` (v0.4) | one vector per session summary, for "what did we decide last week" | session summaries |
| `message_fts` (v0.4) | keyword index over messages, for "what did we say about X" | messages |
| `summary_fts` (v0.4) | keyword index over session summaries | session summaries |
| `tool_calls` | audit log: every MCP, A2A, built-in and local command call, with its call ID, session, `kind` (`mcp`, `a2a`, `builtin` or `command`), server, tool, args, result (first 4,000 characters), outcome, approval choice, duration, trace ID and `caller` (`meru` for a call `merud` made itself) | `sessions/*.jsonl` |
| `memories` (v0.4) | one row per memory file: its ID (`<kind>/<name>.md`), kind, text, created, source, mtime and content hash | `memory/*/*.md` |
| `memory_vec` / `memory_fts` (v0.4) | vector and keyword indexes over memories | memories |
| `turns` | one row per answered question: session, start time, source, route, tokens in and out, duration, tool calls, the files its prompt read, trace ID, and the answer model with its time to first token, writing time, bad calls and whether the turn hit the cap. `meru usage` and the chat's usage numbers count it | `sessions/*.jsonl` (the assistant line holds route, duration, files and the model's numbers; the `model_switch` lines name the model) |
| `jobs` / `job_runs` (v0.5) | scheduled jobs and each run's outcome | jobs: `[[jobs]]` in `config.toml`; runs: the job's session transcript |
| `meta` | schema version, embedding model name and vector size | config |

v0.2 built `documents`, `chunks`, `chunk_vec`, `chunk_fts` and `meta`, and v0.3
added `tool_calls`. Each other table arrives with the milestone marked beside it.
v0.4 replays the transcripts into `sessions`, `messages` and their indexes: at
startup, after each turn and after each summary. `sessions` records how many bytes
of each file it has read, so a replay reads only the lines added since the last one.

`tool_calls` is mandatory. An assistant with tools that change things needs a record
you can read afterwards. `dispatch` writes a row as each call ends, and `meru log`
reads the newest rows. When `merud` starts and finds the table empty, it rebuilds
the table from the transcripts, pairing each call's lines by `call_id`; a call with
no `tool_result` line, because `merud` stopped mid-call, gets the outcome
`cancelled`. `messages` and `tool_calls` store the trace ID of their turn, so you
can jump from a slow trace in Grafana to the rows it produced, and back.

When the embedding model's name or vector size in `meta` stops matching config, the
store deletes every row of `chunk_vec`, `memory_vec` and `session_vec` and keeps
`documents`, `chunks`, `memories` and `sessions`. Keyword search keeps working while
the indexer re-embeds. The store reports the gap by counting chunks that lack a
vector (`NeedsReembed`), so the count stays right if `merud` stops halfway through.
The memory syncer re-embeds each memory that lacks a vector, and the summarizer
does the same for each summary.

### Why this driver and this vector store

- **`ncruces/go-sqlite3` with vec1 and FTS5.** No cgo, no C compiler, and vectors
  live in the same file as everything else. One query can join vector hits, keyword
  hits and document metadata. vec1 ships with the driver, so Meru adds no extension
  of its own.
- **A plain table, no vector index.** Vector search reads every row of `chunk_vec`
  and orders by `vec1_l2_distance(vector, ?) / 2`. The store keeps every vector at
  length 1, and for such vectors half the squared straight-line distance equals the
  cosine distance that embedding models are trained for. vec1 also offers an exact
  "flat" index, but writes slowed as it grew: replacing one 100-chunk file took
  186 ms at 10,000 chunks, and indexing 100,000 chunks would take about ten minutes.
  Plain rows index 100,000 chunks in 6.1 s.
- **Measured.** On the development machine (M4 Max), with 768-dimension vectors and
  100 chunks per file:

  | Operation | 10,000 chunks | 100,000 chunks |
  | --- | --- | --- |
  | index every chunk | 0.59 s | 6.1 s |
  | replace one file | 6.4 ms | 6.1 ms |
  | vector search, top 50 | 13 ms | 147 ms |
  | keyword search, top 50 | 9 ms | 90 ms |

  Search time grows with the index, because both searches read every candidate;
  write time doesn't. `meru.retrieval.duration` will show when search needs to get
  faster. The first fixes are fewer dimensions, or computing distances in Go, not
  another database.
- **Rejected:** `sqlite-vec`, because its Go bindings work only with the 2024
  release of the driver (v0.17.1), 18 releases behind and without the security
  fixes since, and the driver already bundles vec1, which supplies the distance
  function the store needs. Also `chromem-go` (pure Go, but a second store we
  can't join with keyword search), LanceDB and DuckDB (both need cgo and do more
  than we need), Qdrant and Chroma (extra server processes).

---

## Getting your content in

The indexer reads the folders you name into the store, keeps them current while
`merud` runs, and reads nothing else on disk.

### What gets indexed

Only the folders under `[index] folders` in `config.toml`. The list ships empty, so a
fresh Meru indexes nothing, and it reads your home folder only if you list `~`.
`merud` reads the list when it starts, so a change takes a restart.

```toml
[index]
folders        = ["~/notes", "~/Documents/papers"]
ignore         = ["*.log", "drafts/"]   # .gitignore-style, on top of the built-in list
max_file_mb    = 5
chunk_tokens   = 500
overlap_tokens = 50
watch          = true
```

Inside those folders the indexer checks each entry against these rules, in order,
and skips it at the first one that matches:

| Order | Skipped as | What it catches |
| --- | --- | --- |
| 1 | `symlink` | any symbolic link; the indexer never follows one |
| 2 | `secret` | `.env*`, `*.pem`, `*.key`, `id_rsa*`, `*.kdbx`, `credentials*`, `.netrc` and similar |
| 3 | `hidden` | a name that starts with `.` |
| 4 | `build-folder` | `node_modules`, `.venv`, `venv`, `vendor`, `target`, `dist`, `build`, `__pycache__` |
| 5 | `ignored` | `[index] ignore`, then each folder's `.gitignore` and `.meruignore`, from the top folder down |
| 6 | `media`, `binary`, `unsupported` | by extension: images, audio, video, archives, programs, databases, and any type with no chunker |
| 7 | `too-large` | over `max_file_mb` |
| 8 | `binary` | a NUL byte in the first 8 KB (PDFs excepted) |

- **Secrets sit beyond any ignore file's reach.** No `.gitignore` or `.meruignore`
  line can bring one back, because a key in the index would end up in a prompt.
- **Config patterns come next**, and no ignore file can undo them either.
- **Each skip has a reason.** A scan counts skips by reason, and `merud -v` logs
  each skipped path with its reason.

A `.meruignore` uses `.gitignore` syntax: one pattern per line, `#` for a comment,
`*` for any run of characters within a name, `**/` for any number of folders, a
trailing `/` for folders only, a `/` at the start to anchor the pattern to the
file's own folder, and `!` to re-include. Git's rule holds across the ignore files:
the last matching line wins, and a deeper folder's file beats a shallower one's.
Meru reads `.meruignore` after `.gitignore` in the same folder, so it can skip more
for Meru alone, or bring back with `!name` a file git ignores.

### Chunking

The indexer cuts each file into chunks along its own structure:

| File | Split by | Heading kept | Citation points to |
| --- | --- | --- | --- |
| Markdown | heading, then paragraphs | the heading path, such as `Garden > Spring` | lines |
| Go | top-level declaration, read with Go's own parser | the declaration's name | lines |
| other code, plain text | blank-line blocks | none | lines |
| HTML | `<h1>` to `<h6>`, then paragraphs; scripts, styles and `<head>` dropped | the heading path | none |
| PDF | page, then paragraphs; plain text, no layout | none | page |

- **Size.** A chunk holds about `chunk_tokens` (500) tokens and repeats the last
  `overlap_tokens` (50) of the chunk before it, so a sentence cut at a boundary
  still appears whole in one chunk. Meru estimates a token as four characters,
  because Ollama doesn't expose the embedding model's tokenizer.
- **No chunk passes the limit.** A paragraph too big for one chunk gets split at
  line breaks, then between words, and last at a fixed width.
- **The heading path travels with the text.** Each embedded chunk starts with its
  heading path, so the third chunk of a long "Garden > Spring" section still embeds
  as being about the garden in spring.

### Keeping it current

- **Changed files only.** At startup `merud` scans every folder and compares each
  file's mtime and SHA-256 hash with the store's copy. It re-chunks and re-embeds a
  file only when either differs. It hashes every file, because some sync tools and
  editors keep the mtime when the content changes.
- **Removals.** After a folder's scan, the indexer deletes the store's entries for
  files the scan didn't keep: deleted files, and files a new ignore rule now covers.
- **Folders you drop leave the index.** Before it walks the folders, a full scan
  deletes every stored file that sits in none of them. Take `~/notes-old` out
  of `[index] folders` and restart `merud`, or remove it in the desktop app's
  Settings, which rescans at once, and search stops finding its files. The
  test works on whole folder names, so `~/notes` keeps nothing from `~/notes-old`.
  An empty list empties the index.
- **Missing folders keep their entries.** A folder that doesn't exist, such as one
  on an unplugged drive, and a folder the scan can't read both keep what the store
  holds for them.
- **Watching.** With `watch = true`, `merud` asks the operating system to report
  changes in every folder the skip rules keep. It indexes a changed path once the
  path has been quiet for 500 ms, so an editor can finish a save that takes several
  writes. When the operating system refuses another watch (Linux's
  `fs.inotify.max_user_watches`, or the open-file limit on macOS), `merud` logs one
  warning and keeps the watches it has; the next startup scan catches the rest.
- **On demand.** `meru index` rescans every configured folder, `meru index <path>`
  rescans one folder or file inside them, and `meru index -status` prints counts.

### What stays out

- **Symlinks are never followed.** A link inside a folder could index files twice
  or loop, and a link out of it would break the promise that Meru reads only the
  folders you list. A link's target inside the folder gets indexed at its real path.
- **Email, calendar and Drive stay live.** Meru reaches them through their MCP
  servers at question time and copies none of them into `meru.db`. Answers see
  today's inbox, and your mail never lands in the index.

---

## Retrieval

Vector search misses exact strings such as project code names and error codes. BM25, the
standard keyword-ranking formula, misses paraphrase. Meru runs both.

[A question, end to end](#a-question-end-to-end) shows where retrieval sits in a turn,
and [Getting your content in](#getting-your-content-in) shows how files become
chunks: Markdown by heading, code by declaration or blank-line block, PDFs by page as
plain text with no layout.

### How hybrid search works

SQLite does both searches; our code merges the results.

With `[index] retrieval = "auto"`, the default, a search runs before the answer
on the `search`, `tools` and `search+tools` routes. `tools` searches
because the router sends some questions about your files there. It also runs on a
`direct` question that
names an indexed folder (see [Routing](#routing)). Its query is the question, with an earlier
question appended on a follow-up (see
[How a conversation continues](#how-a-conversation-continues)).

So far a turn searches file chunks. From v0.4 it also searches memories and the
summaries of past sessions. Each source gets the same keyword and meaning search,
`rrf` merges the results within that source, and each source has its own share of
the context budget, so one busy source can't crowd out the others.

| Piece | Comes from |
| --- | --- |
| Keyword search, ranked by BM25 | FTS5 (`ORDER BY rank`, where `rank` is BM25) |
| Similarity search, ranked by distance | every row of `chunk_vec`, with vec1's distance function (`ORDER BY vec1_l2_distance(vector, ?) / 2 LIMIT ?`) |
| Merging the two lists | our Go code: reciprocal-rank fusion |

![Flow chart of hybrid search. The query goes two ways. The embed model turns it into a vector, and a vector query over every row of chunk_vec keeps the top 50. A keyword query over chunk_fts, ranked by BM25, keeps its own top 50. The two run one after the other inside merud. rrf() in Go merges the lists with k = 60 into the top 10 chunk IDs, and merud loads their text and paths from chunks and documents. Each stage reports its time in meru.retrieval.duration.](docs/architecture/img/300-hybrid.png)

*Figure 5. Only the vector side needs a model call. The keyword side, the merge and the final lookup are all SQLite or plain Go inside `merud`.*

The keyword query quotes every word of the query and joins them with `OR`, so text
from the user can never reach FTS5 as query syntax. It keeps the first 32 distinct
words, which is why the new question comes before the previous one.

`merud` runs the vector query and the keyword query one after the other, then merges the two
ranked lists with reciprocal-rank fusion (RRF). BM25 scores and vector distances use
different scales, so RRF ignores them and uses each chunk's position in each list:

```text
score(chunk) = Σ over lists  1 / (60 + rank of chunk in that list)
```

A chunk near the top of either list scores well, and one near the top of both scores
best. The constant 60 is the usual choice; it keeps the gap between rank 1 and rank 2
small.

```go
// rrf merges ranked lists of chunk IDs into one score per chunk.
func rrf(lists ...[]int64) map[int64]float64 {
    const k = 60
    scores := map[int64]float64{}
    for _, list := range lists {
        for rank, id := range list {
            scores[id] += 1.0 / float64(k+rank+1)
        }
    }
    return scores
}
```

SQLite could do the merge in one query with window functions and a full outer join.
We merge in Go instead:

- Two short queries and one small function are easier to read than one dense query.
- `rrf` is a pure function, so a table-driven test covers it without a database.
- Each stage gets its own timing in `meru.retrieval.duration` (`vector`, `fts`,
  `fusion`).
- Memory retrieval reuses `rrf` with a third list ranked by recency.

The extra query costs microseconds, because SQLite runs inside `merud`.

**Schema detail.** `chunk_fts` is an FTS5 *external content* table
(`content='chunks'`). It indexes the text in `chunks` without storing a second copy,
and its `rowid` equals the chunk ID. `chunk_vec` uses the same chunk ID as its key, so
both lists name chunks the same way and the merge needs no lookup.

The list sizes are fixed constants in `internal/retrieve`: 50 hits from each search
and 10 chunks after the merge. They aren't config keys. We'll tune them once we have
measurements from real questions.

### Search first, or let the model look

A coding agent finds code with `ls`, `grep` and a file reader, and no search
before the answer. Meru can work that way too. The `search_files` built-in tool
runs the same hybrid search for a query the model writes, and returns numbered
excerpts, so search by meaning becomes one tool next to `grep`, `list_folder` and
`read_file`. `[index] retrieval` picks who decides when to search:

```toml
[index]
retrieval = "auto"      # the default; or "agentic"
```

| | `auto` | `agentic` |
| --- | --- | --- |
| Search before the answer | yes, on turns about your files (see below) | no |
| Earlier conversations in the prompt | yes | no |
| Profile and recalled memories | yes | yes |
| File tools offered | the four, on the routes that offer tools | the same |
| Note in the prompt, on turns about your files | the excerpts, with the rule on citing, and "read, list, grep or search again when the excerpts fall short" | "look first with `search_files` or `grep`, then `read_file`, stop after two or three rounds, cite the excerpts" |

`agentic` needs `search_files` in `[builtin] tools`; `merud` refuses to start
without it. A built-in skill, `file-research`, carries the same advice for the
fast model to pick. In both modes a `search_files` excerpt numbers on from the
turn's other excerpts and joins the `sources` event (see [Citations](#citations)).

We measured both modes with `meru check` on the owner's 19 questions and real
folders: 5,600 files, on the `lite` profile (MiniCPM5 2B), three runs of each,
from the same copy of `meru.db`.

| Category (questions) | `auto` passed | `agentic` passed | `auto` median s | `agentic` median s |
| --- | --- | --- | --- | --- |
| direct (3) | 9/9 | 9/9 | 1.5 | 1.8 |
| files: list, grep, read (3) | 9/9 | 9/9 | 8.3 | 5.6 |
| retrieval: by meaning (3) | 9/9 | 9/9 | 8.0 | 8.7 |
| web (2) | 5/6 | 2/6 | 24.0 | 18.1 |
| mcp (2) | 5/6 | 6/6 | 15.0 | 7.4 |
| command (1) | 3/3 | 3/3 | 25.5 | 19.0 |
| profile (1) | 3/3 | 3/3 | 11.5 | 1.3 |
| session: two turns (2) | 5/6 | 0/6 | 30.1 | 42.0 |
| datetime (2) | 6/6 | 6/6 | 4.9 | 5.2 |
| **all (19)** | **54/57** | **47/57** | 10.8 | 7.0 |

A whole run of the 19 questions took 292 s of turn time with `auto` and 271 s
with `agentic`, on average.

What the numbers say:

- **On one-hop questions the two tie.** "What does my knowledge base say about
  Coase?" passed 3 of 3 both ways, at about the same time. The model called
  `search_files` once and answered.
- **`agentic` lost where the answer takes several steps.** "So when did I visit
  Lisbon?" failed all three `agentic` runs. The answer sits in travel PDFs.
  The up-front search put them in the prompt, and `auto` answered in one round.
  With the tools, the 2B model kept searching and reading. Twice it ended its
  turn by writing a tool call as plain text, and once it never called
  `search_files` and grepped instead. The next question in that session read the
  broken turn in its history and failed too.
- **`agentic` was faster on turns that don't need your files.** "Which Obsidian
  vaults do I have?" took 1.2 s against 14 s, and "what is my name?" 1.3 s
  against 11.5 s. With no excerpts, the prompt stays the same from turn to turn,
  and Ollama reuses its work on it. The up-front excerpts change with each
  question, so `auto` reprocesses everything after them.
- **The web questions fell from 5/6 to 2/6.** The file tools' note also joins
  web turns, which offer every tool, and the 2B model read fewer pages or wrote
  a call as text. This loss comes from the note, not from retrieval; the note
  now joins only turns about your files (see below).
- **`agentic` cost more prompt tokens on file questions**, 1.2 to 3 times as many,
  because each tool round sends the prompt again.

So `auto` stays the default. It already holds both approaches: the excerpts in the
prompt, and `search_files` for a second look. We didn't measure the `full`
profile: the test machine was already 16 GB into swap, and a 23 GB model would
have measured the disk. A larger model may follow the explore loop better;
rerun `meru check` with each setting before changing the default for `full`.

Dropping the vector index would take more than this mode. Recalled memories,
earlier conversations and `search_files` itself all search it, and so do the
v0.4 "Done when" tests for recall. The numbers give no reason to drop it: the
one tool that searched by meaning was the one that answered the questions
`agentic` passed.

#### Turns that aren't about your files

Two changes in `auto` mode followed from these numbers. `agentic` won its time on
turns that need no files, and the web questions pointed at the note on the file
tools.

- **No search first on a tool question.** A `tools` turn skips the search when the
  question points at a connected tool. The signs that add tools to a route
  decide it (see [Routing](#routing)): the question names a tool server, says
  "remember", asks for the web, gives a URL, or names what a server's tools
  act on. `search` and
  `search+tools` always search: the router, or the folder rule, saw files in the
  question. The turn still offers the file tools, so the model can look when it
  has to. Before this, "search the web for the latest Go release" put ten
  excerpts from your folders in the prompt, and since `web_search` numbers its
  results from `[1]` too, the Sources list under the answer could name one of
  your files.
- **The file-tools note only on file turns.** The note that the model may read,
  list, grep and search the folders joins only a turn about your files that
  offers the file tools. It sits after the parts every turn shares and before the
  parts each question changes, so a web question's prompt is shorter and still
  shares its whole opening with a file question's. The note on the folders that
  every turn carries names them and nothing more.

We measured both with `meru check` on a made-up setup, so that no personal data
entered the test: eight Markdown notes in one folder, the test MCP server
(`cmd/fakemcp`) connected as `almanac`, and 18 questions shaped like the owner's.
`lite` profile, `auto` mode, three runs before the change and three after, each
from an empty `meru.db`.

| Category (questions) | Before passed | After passed | Before mean s | After mean s | File sources per turn, before → after |
| --- | --- | --- | --- | --- | --- |
| direct (3) | 9/9 | 9/9 | 1.6 | 1.3 | 0 → 0 |
| files: list, grep, read (3) | 9/9 | 9/9 | 3.2 | 3.1 | 8 → 8 |
| retrieval: by meaning (3) | 9/9 | 9/9 | 3.6 | 3.2 | 8 → 8 |
| web (2) | 2/6 | 5/6 | 11.6 | 9.3 | 8 → 0 |
| mcp: `almanac` by name (2) | 6/6 | 6/6 | 4.5 | 1.5 | 8 → 0 |
| memory: "remember that…" (1) | 2/3 | 1/3 | 3.7 | 3.7 | 8 → 8 |
| session: two turns (2) | 6/6 | 5/6 | 6.6 | 4.6 | 8 → 6.7 |
| datetime (2) | 6/6 | 6/6 | 6.7 | 6.2 | 4 → 4 |
| **all (18)** | **49/54** | **50/54** | | | |

A run's turn time fell from 87 s to 70 s. The eight folder files make every
search return all of them, so "file sources" counts eight wherever a search ran.
What the numbers say:

- **Tool questions got faster and lost the stray sources.** The two `almanac`
  questions fell from 4.5 s to 1.5 s, and every web and `almanac` turn went from
  eight file sources to none.
- **The web questions passed more often.** "What is SearXNG, with a source?"
  failed all three runs before: each answer cited a number such as `[1]` and gave
  no link. After, all three gave the link.
- **File questions held.** The nine file and retrieval runs passed before and
  after, at about the same time.
- **The losses don't come from the rule.** "Remember that my favourite tea is
  jasmine" routed to `search+tools` in every run, which the rule leaves alone;
  in the failing runs, after the first run had saved the fact, the model
  answered that it already knew. The failed session turn routed `direct`, which
  the change doesn't touch either.

### Citations

`merud` numbers the chunks it found and puts them in the prompt under "From your
files", with a rule that tells the model to cite each excerpt it uses as `[1]`,
`[2]` and so on, and never to invent one. When a search finds nothing, the prompt
says so instead, and the model answers without citing files.

The system prompt names the folders in `[index] folders` on every turn and says
that Meru searches them before it answers. The note that the model may read, list,
grep and search the folders itself joins only turns about your files. With no folders set, it tells the model that Meru hasn't indexed anything
yet and where you add folders. Without this note a small model answers "I don't
have access to your files" while it reads excerpts from them, and can't say what
Meru indexes.

Before the first token, `merud` sends the client a `sources` event that lists each
excerpt with its number, path (as `~/…`), heading, and line range or PDF page.
When `search_files` returns excerpts later in the turn, they take the next
numbers, ride on the call's `tool_result` event, and `merud` sends a new
`sources` event that holds every source so far. Clients keep the last one. Once
the answer ends, one-shot `meru` prints a `Sources:` list and `meru chat` shows the
same list under the answer. Both list only the sources the answer cites, and an
answer that cites none gets no list: the model decides when a source matters. An
earlier rule listed every excerpt when the answer cited none, in case a small model
forgot to cite; in use it printed ten unrelated files under general answers and
under an answer built from an Obsidian search.

```text
The garden project sows tomatoes on 12 April [1].

Sources:
[1] ~/notes/garden.md, "Planting", lines 3–5
```

With a tiny index, every chunk lands in the top 10. A minimum fused score, or
keeping fewer than 10 chunks, would shorten the prompt; choosing either waits for
measurements (see [Open questions](#open-questions)).

---

## Memory

Each memory is a small Markdown file you can read, edit or delete by hand. The files
hold the truth; the database indexes them so Meru can search them, and rebuilds from
them if you delete it.

### Layout

One file per memory, and one folder per kind:

```text
~/.meru/memory/
  me/            who you are: role, family, where you live
  preferences/   how you like things done
  projects/      ongoing work, goals, deadlines
  people/        people you mention and how they relate to you
  reference/     where things live: accounts, URLs, tools
  other/         anything worth keeping that fits no folder above
```

```markdown
---
created: 2026-09-23
source: session 2026-09-23T101502-7f3a
---
Prefers short replies, with the answer in the first sentence.
```

- **The folder is the kind.** No `kind:` field can disagree with the path, and you can
  browse by kind in a file manager or with `ls`.
- **One fact per file**, so forgetting a memory means deleting one file. You can do
  that by hand, or `meru memory forget` does it.
- **`other/` holds anything that fits no other folder.** If it fills up with one kind
  of thing, create a new folder for that kind; Meru picks it up with no code change.
  A kind's folder name uses letters, digits, `-` and `_`; Meru skips any other
  folder. `merud` creates the six default folders when it opens the memory folder.
- **The frontmatter records when and where** each memory came from, so you can trace
  it back to the session that produced it.
- **Memories stay small.** Meru saves a new memory only up to 4 KiB of text, and
  reads a memory file only up to 64 KiB, which leaves room for hand edits. It
  skips a larger file and says which.
- **Meru never follows a link.** It won't read or delete a memory file, or a kind
  folder, that is a symbolic link, and every file operation stays inside
  `~/.meru/memory/`.

Meru keeps no index file. The database already indexes the files, and
`meru memory list` shows them.

### Facts and episodes

- **Facts** (semantic memory) stay true over time: everything in the folders above.
- **Episodes** (what happened when) already live in the session transcripts, so
  memory files don't copy them. When a session ends, `merud` asks the `fast` model for
  a one- or two-sentence summary and appends it to the transcript as a `summary`
  line. A session ends when it has had no question for `[agent] summary_idle` (30
  minutes by default); a session that goes on later gets a new summary, and the
  newest wins. `merud` embeds each summary into `session_vec`, so "what did we
  decide about the garden beds last week?" finds the right session. On the routes that
  search files, the prompt gets up to three past sessions under "From earlier
  conversations", each with its date, summary and best-matching message.

### How Meru uses them

0. **Your profile, in every prompt.** Every file in `me/` and `preferences/` goes
   into the system prompt of every turn, under "What you know about the user",
   up to 2,000 characters; past that, the newest files win and the rest wait
   for recall. These are the facts Meru should never have to search for: your
   name, your email, your work, where you live, how you like answers. Without them, the
   model can't tell whether "Sam" in a letter is you or someone you know.
   `meru setup user` asks for them one at a time and saves each as a memory,
   and you can add more in chat ("remember that I work on the registry team").
   While both folders are empty, `meru chat` says Meru doesn't know you yet and
   points at `meru setup user`.
1. **Indexing.** The indexer treats `~/.meru/memory/` like any folder you index: each
   file gets a vector and a keyword entry. It picks up hand edits through the usual
   mtime and content-hash check.
2. **Recall.** Each turn searches memories by meaning and by keyword, adds a third
   list ranked by recency, and merges all three with `rrf`. The top results go into
   the context, up to the memory budget. Recall runs on every route, including
   tools-only turns, because a preference such as "always ask before sending mail" matters
   most when tools run. Before the answer, `merud` sends a `memories` event that
   lists what recall put in the prompt, so the desktop app can show each memory
   under Remembered, with a button that forgets it.
3. **Saving.** The model saves a memory by calling the built-in `remember` tool with
   a folder and the text. The call goes through `dispatch` like any other tool, so it
   lands in `tool_calls` and the transcript. Memories save without asking; add
   `remember` to `builtin.confirm` in `config.toml` if you want to approve each one.
4. **Your commands.** `meru memory list | add | forget`, and the desktop app's
   About you, work on the files through `merud`, which owns the memory folder.

If Meru believes something wrong about you, you can find the file and fix or delete
it. A vector blob you can't read would leave you no way to audit or correct it.

---

## Skills

A skill is a markdown file with YAML frontmatter, at `~/.meru/skills/<name>/SKILL.md`:

```markdown
---
name: meeting-notes
description: Turn a meeting transcript into decisions and action items. Use when
  asked to write up a meeting, list what was agreed, or who owns what.
---

<the instructions, loaded only when the skill is chosen>
```

The system prompt carries only each skill's `name` and `description`. From v0.4, a
short call to the `fast` model picks the skills a turn needs, and `merud` loads only
their bodies, so adding skills barely grows the prompt.

A skill may also list the tools its steps use, under `allowed-tools`, the key
Claude's skills use:

```markdown
---
name: web-research
description: Look up current facts on the web...
allowed-tools: web_search, web_fetch
---
```

When the pick chooses a skill, the turn gains the tools it names that config
allows: a built-in tool in `[builtin] tools` (`web_search` also needs a SearXNG
address), or an MCP or A2A tool on its allowlist. A skill never turns on a tool
config keeps off, and every call still goes through `dispatch`. When config
allows none of a skill's tools, the turn leaves that skill's instructions out:
telling a model to search the web with no search tool made it call the one tool
it had over and over. A name Meru doesn't know as a tool, such as Claude's
`Read`, counts for nothing, so a skill copied from Claude doesn't lose its body.
The key takes a comma list, a `[...]` list or a YAML list of `- name` lines.

The list of skills in every prompt says a skill is a set of instructions, not a
tool. It names a skill only when the turn offers at least one of the tools its
`allowed-tools` names, or when it names none, and gives each skill with tools a
line such as "To use it, call web_search or web_fetch." that names the tools the
turn offers. A picked skill whose tools the turn doesn't offer, as on the Just
talk scope, leaves its instructions out too. In a real session the list named
`web-research` on a `direct` turn that offered no web tool; the model called
`web-research` as a tool, and then told the user it couldn't search the web.
Since every route in Auto offers the same web tools, the list still stays the
same from turn to turn there.

A model may still call a skill as a tool. Such a call goes to `dispatch`, which
denies it as it denies any tool no backend offers, and the call counts in
`meru.model.malformed_calls`. In place of the usual refusal the model reads a
hint whenever config allows the skill's tools. When the round offers them, the
hint is "web-research is a skill, not a tool. Call web_search or web_fetch.",
and the model can call the tool in its next round. When config allows them but
the round doesn't offer them, it says the skill works through those tools and
that this answer doesn't offer them, so the model answers without them.

A skill's `name` is lowercase letters and digits in words joined by `-`, such as
`meeting-notes`, and must match its folder's name. A `SKILL.md` may be up to
256 KiB. A skill that breaks a rule is skipped with a warning that says why, and
the other skills still load.

Skills are plain files, so any other agent that reads this format can use the same
directory.

To turn a skill off, name it in `[skills] disabled`:

```toml
[skills]
disabled = ["explainer"]   # the default is [], every skill on
```

`merud` doesn't load a disabled skill, so the model never sees it. For a built-in
skill, `merud` also doesn't install it: delete its folder and disable it, and it
stays gone. A name that matches no skill isn't an error, since you may add that
skill later; `merud` logs it at info and moves on. A skill folder you add loads
with no change to config.

### Built-in skills

Meru ships with four skills. `writing` and `explainer` come from the owner's
`my-ai-assets` repo; `web-research` and `file-research` are Meru's own:

| Skill | What it does |
| --- | --- |
| `writing` | Plain-English rules for any prose Meru writes: emails, summaries, reports |
| `explainer` | Builds a self-contained HTML page that teaches a topic, with diagrams |
| `web-research` | For how to use a program or command, and for questions that need current facts: search the user's own words first, with a name in double quotes; never swap in a product or company the model knows for the one named; search again when the results are about something else; read the one or two best pages with `web_fetch` and a prompt, prefer primary sources, check dates against today, quote versions from the page, cite URLs, and say when sources disagree. Brings `web_search` and `web_fetch` |
| `file-research` | For questions about the user's own files, and not for how to use a program: `search_files` for a topic in any words, `grep` for an exact name or phrase, `list_folder` to see a folder, `read_file` for the whole text; try other words once, stop after two or three rounds, cite the numbered excerpts. Brings those four tools |

The fast model picks from the descriptions alone, so their words decide which
skill loads. `make pick-eval` asks the pick 21 labelled questions three times
each: how to use a program, current facts, the user's files, and plain chat. A
pick counts as right when it names the wanted skill and no research skill the
question doesn't want. On the `lite` fast model (MiniCPM5 2B), the old
descriptions got 50 of 63 right and chose `file-research` three times for the
btop question. Naming programs and commands in `web-research`, and ruling them
out in `file-research`, got 52 of 63, with no stray `file-research` pick. A
line in the pick prompt that said to choose `file-research` only for the user's
own files did worse, 42 of 63: naming the skill drew the model to it.

- **They ship inside the binary** (Go's `embed` package) and live in the repo under
  `internal/skills/builtin/`. On first run, `merud` copies each one to
  `~/.meru/skills/<name>/` unless that folder already exists or `[skills]
  disabled` names it.
- **Your copy wins.** `merud` never overwrites a skill you've edited, or one a
  newer Meru changed. To get the shipped version back, run
  `meru skills reset <name>`; a copy of `web-research` from before
  `allowed-tools` needs it to bring its tools, copies of `web-research`
  and `file-research` from before the sharper descriptions need it to get them,
  and a copy of `web-research` from before the rule on names needs it to get
  that rule.
- **You add more by dropping in a folder.** Any `SKILL.md` under `~/.meru/skills/`
  counts, whether you wrote it or copied it from elsewhere.
- **You turn one off in config.** `[skills] disabled` names the skills `merud`
  skips, built-in or yours (see [Skills](#skills)).
- **Skills that make files need somewhere to put them.** The built-in `write_file`
  tool (v0.4) writes only inside `~/meru-output/` (configurable). It can't touch any other
  path, and it goes through `dispatch` like every tool.
- **These skills want the `full` profile.** The `lite` model can run them, but a 2B
  model writes weaker explainers and reads pages less well.

---

## First run and setup

`meru setup` walks you through setup in the terminal. Nothing in it needs you to
know how MCP works. You can run it again at any time. v0.3 doesn't start it on its
own the first time you run `meru`.

1. **Ollama.** Meru checks that Ollama answers. If it doesn't, Meru prints the
   install step for your platform and waits for you to press Enter.
2. **Models.** With no `config.toml` yet, you pick `lite` (the default) or `full`.
   Meru names the profile's models and, after a yes, downloads each with
   `ollama pull`, which draws its own progress bar.
3. **Your files.** Meru asks which folders to index (for example `~/notes`) and
   writes `config.toml` from the config template, with two lines filled in: the
   profile and `[index] folders`. The template holds every key. What is on by
   default stays uncommented, with its default value, so you see it and can change
   it; what is off, such as the catalog's MCP servers, sits there commented, ready
   to uncomment. Setup writes the file only when none exists: rewriting yours would
   drop your comments, so for an existing file setup says what to edit instead.
   `meru config template` prints the template, to compare with your file or to
   start over. `merud` creates the rest of
   `~/.meru/` when it starts; setup doesn't write `prompt.md` or the built-in
   skills yet.
4. **Web search.** Meru checks that SearXNG answers JSON at `[web] searxng_url`.
   When nothing answers, it prints the commands that start SearXNG in Docker; when
   SearXNG answers HTML, it names the `search.formats` setting to change. Then it
   waits: Enter checks again, `s` skips. Meru works without web search (see
   [Web search](#web-search)).
5. **Tools.** Meru offers the catalog's servers, one at a time, and you pick a path
   for each (see below). You can skip any of them and add them later.
6. **About you.** When `merud` runs, Meru offers `meru setup user`, which asks your
   name, your email, your work, where you live and how you like answers, and saves each as a
   memory (see [Memory](#memory)).
7. **A test question.** When `merud` runs, Meru asks it one question so you see it
   working. Otherwise it tells you how to start `merud`. A new `config.toml`
   takes a restart of `merud`; a new server doesn't (see
   [MCP](#mcp)).

The desktop app has its own Setup screen, which needs `merud` running: its four
steps cover the models, your folders, connections and about you, and every change
goes through `merud` (see [Desktop app](#desktop-app)). Writing the first
`config.toml` and pulling models stay with `meru setup`.

On a Mac, the installer in each release's disk image does all of this in a window,
before `merud` exists, and more: it installs Ollama, starts SearXNG in Docker, sets
up the Google server and starts `merud` at login (see [Installer](#installer)).

### Adding an MCP server

Meru carries a small catalog of known servers in the binary, one for each kind of
example this document uses: mail and calendar, and notes. Web search needs no
server; it is built in (see [Web search](#web-search)).

| Name | Server | Transport | Needs | Asks first |
| --- | --- | --- | --- | --- |
| `google` | `taylorwilsdon/google_workspace_mcp` (`uvx workspace-mcp`) | Streamable HTTP on `127.0.0.1:8000/mcp` | a Google OAuth client, a sign-in, and you running it | sending mail, changing an event |
| `obsidian` | `mcp-obsidian`, through Obsidian's Local REST API plugin | stdio | the plugin's API key | appending to a note |

Each entry lists the install command, what the server needs, and a starting `allow`
and `confirm` list: reading allowed, anything that sends, writes, runs or changes
something in `confirm`.

`google` is the one `url` entry. The server's README marks stdio as legacy, and its
OAuth 2.1 mode needs HTTP, so you start it yourself and `merud` connects to it
(see [MCP](#mcp)). The catalog prints the command:

```sh
USER_GOOGLE_EMAIL=<your Google address> WORKSPACE_ATTACHMENT_DIR=~/meru-output/attachments \
  GOOGLE_OAUTH_CLIENT_ID=<your client ID> GOOGLE_OAUTH_CLIENT_SECRET=<your client secret> \
  uvx workspace-mcp --transport streamable-http --tools gmail calendar drive docs --tool-tier extended
```

The server offers 120-odd tools across twelve Google services. `--tools` limits the
process to Gmail, Calendar, Drive and Docs. `--tool-tier extended` limits it
further, to the 45 core and extended tools of those four: the smallest tier that
holds `get_gmail_thread_content` and `get_gmail_attachment_content`. The flag also
wins over a `WORKSPACE_MCP_TOOL_TIER` in your environment. The catalog's `allow`
limits the model to nine of those tools: search and read mail and threads, save a mail's
attachment, send mail, list and change calendar events, search Drive, and read a
doc. Sending mail and changing an event sit in `confirm`. So two layers apply:
the server decides what exists, and Meru decides what the model sees. The OAuth
client ID and secret go in the server's environment when you start it, never
through Meru. Google's own Workspace MCP servers are remote only, so the catalog
keeps the local one.

`get_gmail_attachment_content` saves the attachment as a file and returns its
name, not its text. `WORKSPACE_ATTACHMENT_DIR` points the server at
`~/meru-output/attachments`, inside the output folder the file tools read. In
testing, a model left to open the file itself failed: it downloaded one PDF four
times, guessed the wrong folder and ran out of rounds. So `dispatch` reads it.
When an MCP or A2A result names, word for word, a file in the attachments folder
that changed after the call began, `dispatch` adds to that result a line with the
file's path, then its text. It reads with `read_file`'s rules, a PDF page by
page, up to `read_file`'s 12,000 characters or the room the result leaves under
16,000. A longer file ends with the offset for `read_file`. A file Meru won't
read, such as a symlink, gets the path line and the reason. One result gets at
most two files. The time rule keeps out an older attachment that a result
happens to name. With no `[index]` folder the file tools are off, and so is
this. The server deletes each saved file after an hour. The model reaches the
file on disk, with no network call: the server's download URL is on loopback,
and `web_fetch` refuses loopback.

The catalog has no shell server; to let the model run a program, declare it in
`[[commands]]` (see [Local commands](#local-commands)).

For each server, you choose one of two paths:

- **"Do it for me."** Meru asks only for what the server needs, one question at a
  time, and reads each key without showing it on screen: "Paste the API key from
  Obsidian's Local REST API plugin". For `google`, Meru prints the command that starts the server and never
  runs it, and notes that the server gives you a sign-in link the first time the
  model uses a Google tool. Then Meru shows you the exact config block it will
  add, and writes it only after you approve.
- **"Show me how."** Meru prints the config block, any install command and the
  `secrets.toml` lines, and names the file to paste them into. Nothing changes
  until you do it yourself.

Meru adds the block to the end of `config.toml` as plain text, so your comments
stay. It writes a temporary copy first and loads it, and replaces the file only
when the copy loads and its servers pass the same checks `merud` runs.

Outside setup, one command adds any server:

```sh
meru mcp add <catalog-name>                        # a catalog entry
meru mcp add stdio <name> -- <command> [args...]   # a server merud starts
meru mcp add http <name> <url> [--remote]          # a Streamable HTTP server you run
```

`meru mcp add google` offers the same two paths as setup. The older forms, `meru
mcp add <name> -- <command>` and `--url <url>`, still work. `meru mcp` shows the
state of each configured server (see [MCP](#mcp)). `meru mcp list` shows the
catalog, then each server in `config.toml` with what `merud` says
about it: connected or not, and how many tools it offers and allows. `meru mcp
remove <name>` takes one `[[mcp.servers]]` block out of `config.toml`, with the
comment lines above it, keeps every other line, and checks the result loads before
it replaces the file, as the append does.

Nobody knows a server's tool names until something connects to it, and a guess would either
miss or allow a tool nobody has read. So `meru` asks `merud` to **probe** the
server first: `merud` starts it for a moment (or connects, for a URL), lists every
tool it offers, and stops it. The probe uses the same code, trimmed environment,
loopback rule and resolved secrets as the pool, calls no tool, and saves nothing.
It gives up after 30 seconds; a first `npx -y` or `uvx` run downloads the server,
so the error says to try again.

For each tool the probe reports two MCP hints, when the server gives them:
`readOnlyHint` (the tool changes nothing) and `destructiveHint` (it may delete or
overwrite). From them `meru` proposes a split: read-only tools go in `allow`, and
tools that may change something go in `allow` and `confirm`, so each call asks
first. A tool with no hints counts as one that may change something. For a catalog
entry the probe runs too, but the catalog's own lists win over the hints, and a tool
the catalog doesn't name starts out of `allow`. You edit the proposal before `meru`
writes the entry: Enter accepts, and `-name`, `+name` and `?name` leave a tool out,
allow it without asking, or make it ask. MCP calls these hints, not promises: a
server can mislabel a tool, so the proposal is a starting point and you make the
call. When the probe fails, `meru` says why and offers to try again, to write the
entry anyway (with the catalog's lists, or an empty `allow`), or to cancel. For a
server you run, such as `google`, `meru` probes only when something answers at the
URL. When nothing does, it writes the catalog's lists and says `merud` connects on
your first question after you start the server. A URL off this machine needs
`--remote`, and `meru` says that each tool call sends your data there; the entry
gets `remote = true`.

After it writes the entry, `meru` asks `merud` to reload its MCP servers, so the new
tools work without a restart.

In chat, "connect my Gmail" works too: the model calls the built-in `configure`
tool, which adds a catalog entry, or a custom one from a name and a command or URL.
`configure` never writes an entry whose secret is missing from `secrets.toml`. It
tells the model to send you to `meru mcp add <name>` in a terminal instead, because
a key typed into chat would pass through the model, the approval prompt and the
transcript. After it writes, `merud` rebuilds the MCP client pool and swaps it in,
so the new server works without a restart.

**Config changes always ask.** `configure` goes through `dispatch` and asks you
every time, and offers only "approve once" and "deny". A session approval isn't
available for it, and `builtin.confirm` can't switch the prompt off. Config grants
lasting trust, so the model can't grant any to itself. (Built-in tools need no
allowlist entry: the model always sees `configure`, and from v0.4 `remember` and
`write_file`.)

**Secrets stay out of config.** API keys go in `~/.meru/secrets.toml`, a flat list of `name = "value"` lines. A config value
refers to one as `secret:<name>`, in an MCP server's `env` or `headers` or an A2A
agent's `headers`:

```toml
env = { OBSIDIAN_API_KEY = "secret:obsidian_api_key" }
```

`env` belongs to a stdio server, which `merud` starts. A `url` entry with `env`
fails at startup: `merud` starts no process for it, so the variables would go
nowhere. The message names the server and points at `headers`, the way to send a
key to an HTTP server.

`merud` swaps in the value when it starts a server or calls an agent, and refuses
to start when group or other users can read `secrets.toml` (it says to run
`chmod 600`). `meru mcp add` writes the file with mode `0600`. Before `dispatch`
writes a transcript line, a `tool_calls` row, a log line or a span, it replaces each
stored value of 8 or more characters with `[secret:<name>]`; shorter values would
match ordinary words. A config file you share or commit holds names only.

---

## Installer

The Mac installer, "Install Meru.app", takes a Mac with Apple silicon from nothing
to a running Meru in nine steps: Ollama and the models, web search, folders in the
index, skills and commands, Google if you want it, your profile, and `merud` started
at login. Each release carries it in a disk image, `Meru-vX.Y.Z-macos-arm64.dmg`,
beside the zip files. `scripts/install.sh` and the `meru-install` skill stay for
people who prefer a terminal.

**Why it is its own program.** The clients never run a program. The desktop app
changes config only by asking `merud`, and `meru setup` writes it only through
`catalog`. The installer has to install Ollama, pull models, start a container and
load launchd jobs before any `merud` exists, so it can't ask `merud`. It is a
fourth program, `cmd/meru-installer`: a Wails window with Meru.app's fonts, colours
and logo, which it borrows from Meru.app's page, and a dark palette that follows the
Mac's setting. Its logic lives in `internal/installer`, which doesn't import Wails,
so CI tests every step with fakes.

What runs where:

| Part | Runs as | Does |
| --- | --- | --- |
| `cmd/meru-installer` | the window | serves the page, binds the Bridge, shows the folder dialog |
| `internal/installer` | the installer's process | the nine steps, the Bridge the page calls, the allowlist of programs |
| programs from the allowlist | child processes, no shell | `brew`, `docker`, `launchctl`, `open`, `ditto`, `xattr`, `codesign`, `sysctl`, `sw_vers`, `df` |
| Ollama | Ollama.app, or a Homebrew service | answers `POST /api/pull`, whose stream gives each download's progress |
| SearXNG | the Docker container `meru-searxng` | web search on `127.0.0.1:8888` |
| `workspace-mcp` | the launchd job `com.meru.workspace-mcp` | the Google server, when you set it up |
| `merud` | the launchd job `com.meru.merud` | everything after the last step |

**The rules it keeps.** `internal/policy` checks each one:

- It starts programs only in `internal/installer/run.go`, which holds a fixed map
  from each program's name to the absolute paths it may live at. No other installer
  file imports `os/exec` or `syscall`, and the map holds no shell, interpreter or
  downloader. The code builds each argument as its own string, so no argument can
  grow into a second command. It never searches `PATH`: an app opened from Finder
  gets a short one, and with fixed paths no file dropped on `PATH` can stand in for
  a program.
- It never imports the engine, the agent loop, the store, `index`, `dispatch`, the
  MCP or A2A clients or `commands`, and never talks to a model. It pulls models
  through Ollama's HTTP API on loopback, which carries no prompt.
- It changes `config.toml` only through `catalog`: `SetTableLists`,
  `SetTableString`, `AppendServer` and `AppendCommand`. Each edits lines of text,
  keeps every comment, and replaces the file only after the result loads. A missing
  `config.toml` starts as the template. The profile goes into memory files through
  `memory`, the same files `merud` reads, and the installer talks to `merud` only
  over the socket.
- It reaches the internet only after you press Continue on a step that says what it
  downloads: through Homebrew for Ollama and uv, through `docker pull` for the
  SearXNG image, and, on a Mac with no Homebrew, straight to
  `https://ollama.com/download/Ollama-darwin.zip`, whose address the screen shows
  first. `codesign --verify` then checks Ollama.app's signature, and the installer
  deletes the app when the check fails. It sends no telemetry and never checks for
  updates.
- Each link on its screens is a name that the Bridge maps to a fixed `https`
  address. The page can't open an address of its own.

**The steps.** Each screen says what the step does, why Meru needs it, and what it
downloads and how long it takes. Continue runs it and shows its progress line by
line, with a bar while a model downloads. A failed step shows the reason with Retry
and Skip. You can skip every step but About you.

| # | Step | What it does | What it writes |
| --- | --- | --- | --- |
| 1 | Check this Mac | reads the chip, macOS, memory and free disk with `sysctl`, `sw_vers` and `df`, and offers the model sets that fit | nothing |
| 2 | Install Meru | copies `meru` and `merud` to `~/.local/bin` and Meru.app to `/Applications`, or `~/Applications` when that isn't writable, with `ditto`; clears the quarantine mark with `xattr` when its box is ticked; adds the `PATH` line to `~/.zshrc` when asked | the three programs; `config.toml` from the template when missing |
| 3 | Ollama and the models | installs Ollama when missing, starts it, and pulls each model of the chosen set that Ollama lacks | `[models] main`, when the set names one |
| 4 | Folders to search | lists the folders config has, then Documents, Desktop and Notes with file counts; the system's folder dialog adds more | `[index] folders` |
| 5 | Web search | keeps a SearXNG that already answers JSON on `127.0.0.1:8888`. Otherwise it checks `docker version`, writes `~/.meru/searxng/settings.yml` (JSON on, a random `secret_key`, the limiter off), pulls `searxng/searxng`, runs `meru-searxng` published on `127.0.0.1:8888` only with `--restart unless-stopped`, and sends one test search | `[web] searxng_url`; `web_search` and `web_fetch` in `[builtin] tools` |
| 6 | Skills and commands | takes the four built-in skills out of `[skills] disabled`, and offers the template's sample `[[commands]]`: the Mac snapshots start ticked, and a sample whose program or folder this Mac lacks is greyed out | the ticked `[[commands]]`, uncommented, at the end of the file |
| 7 | Gmail, Calendar and Drive | walks through Google's console in three screens, takes your address, client ID and secret, installs uv with Homebrew, and waits for the server to answer | `~/.config/workspace-mcp/start.sh` (mode `0700`), its launchd job, the `google` entry |
| 8 | About you | asks your name, your email and how you like answers | `memory/me/` and `memory/preferences/` |
| 9 | Start Meru | writes `com.meru.merud.plist` from `deploy/launchd/` with the paths filled in, loads it, waits for `ping`, and follows `merud`'s own first scan through `index_status` for up to two minutes | the launchd job |

The last screen lists what each step did, or that you skipped it. It names
`~/.meru/config.toml`, with a button that opens it in your text editor, and says
that Meru.app's Settings and `/help` in `meru chat` change most settings. A button
opens Meru.

**About you can't be skipped.** Meru reads files and mail that name many people.
Without your name, the model can't tell who "I" and "my" mean, and mixes you up
with someone in your files. The screen says so, and says the answers stay in
`~/.meru/memory/` as files you can edit, or change in Settings, About you, or with
`/me` in `meru chat`. The name is required. The email is required only after the
Google step, because every Google tool takes the address; the screen fills it in
from that step. How you like answers stays optional. Start Meru refuses to run until
About you is done. On a second run the form shows what Meru already knows.

**The model table.** `config.Recommend` picks from one table in
`internal/config/recommend.go`, next to the profiles, so a change of default models
changes one Go file. Every Mac gets `lite`. With 48 GB or more the installer
suggests `qwen3.6:35b-a3b-mxfp8` as `[models] main` too, and you can pick `lite`
alone. A 32 GB Mac gets `lite` until we find a smaller model that calls tools and
fits.

**Running it again.** Each step first looks for its own work: the same programs in
`~/.local/bin`, Ollama answering with the models, folders in config, SearXNG
answering, commands in config, the start script and the `google` entry, a saved
name, `merud` answering. The screen says what it found and offers Skip. Each write
checks before it changes anything: `settings.yml` keeps its secret, a command or a
server config already has stays as it is, and an unchanged answer keeps its memory
file. Continue on Start Meru restarts `merud`, so it reads what this run wrote.

**Unsigned.** Meru has no Apple developer account, so the disk image, the installer
and the apps carry no Apple signature. "Read me first.txt" on the disk image
explains the right-click, Open step, and the installer's first screen says why
macOS asked. The Install Meru step clears the quarantine mark only from what it
installed, and only when its box is ticked; the box says what the mark does.

**What it doesn't do.** It doesn't uninstall, update itself or look for a newer
Meru. It doesn't install Docker, whose licence terms and size are yours to weigh;
it links the download page and offers Skip. It can't do Google's console steps for
you, and it doesn't sign you in to Google: the first Google question does, with a
link from the server.

---

## MCP

Meru is an MCP **client**; it hosts no servers. `config.toml` lists the servers, and
Meru merges their tools into one set of names, prefixed per server: the model sees
`obsidian_simple_search` from the `obsidian` server as `obsidian.obsidian_simple_search`. It supports the
two transports in the current MCP spec, both provided by the official Go SDK:

- **stdio:** `merud` starts the server as a child process and talks to it over
  stdin and stdout. Most local servers work this way.
- **Streamable HTTP:** `merud` connects to a server that is already running, at a
  URL. It replaced the older HTTP+SSE transport in the 2025 spec, so Meru doesn't
  support SSE.

Every MCP server you already run becomes a Meru capability with no new code.

**`merud` doesn't supervise servers.** The operating system already ships a
process supervisor, and a second one inside the daemon would trade the first
design rule for convenience:

| Transport | What `merud` does | What it never does |
| --- | --- | --- |
| stdio | starts the server as a child, because the transport is the child's stdin and stdout | restart it on its own, watch its health, back off and retry |
| Streamable HTTP | connects to a URL | start it, restart it, health-check it, wait for it |

Starting a stdio child isn't supervision: until the child exists there is no server
to talk to. An HTTP server is somebody else's process with its own lifetime. You
start it with `launchd`, `systemd` or by hand, the way you start Ollama.

**Connecting: once at startup, once more per turn.** `merud` tries each configured
server once when it starts: connect, `initialize`, `tools/list`. A failure is
recorded with its reason, and the server stays listed as not connected. A server
that isn't connected has no tool list, so its tools aren't offered. At the start
of each turn on a tools route, before it lists them, `merud` tries each server
that isn't connected once more, inside that turn: 5 seconds for an HTTP server, 30
seconds for a stdio child, which may still be downloading. If the try fails, the
turn carries on without that server. A stdio child that crashed counts as not
connected and follows the same rule. For stdio, "try" means start the child and
run the handshake; for HTTP, connect to the URL.

**Listing again: once per turn.** The same turn sends `tools/list` to each server
that is connected, with 2 seconds to answer, and keeps the answer, so the tool list
follows the server. An HTTP server needs this: `merud` holds no stream open to it,
so when you restart it nothing tells `merud`, and the old session looks alive until
`merud` sends something. Before this rule, `merud` kept a restarted
`workspace-mcp`'s old list of 16 tools after the server came back with 33, and an
allowed tool stayed missing until `merud` restarted. If the listing fails, because
the server answers "session not found" or nothing listens at its address, `merud`
marks the server not connected and makes the one connect try above, which lists
the tools afresh. A listing that runs out of time keeps the old list and the
session, since a slow answer doesn't prove the session is gone. A stdio server
can't restart behind `merud`'s back, since `merud` owns the process; listing it
again costs one round trip over a pipe, and one rule for both transports keeps the
code short. On an Apple M4 Max, `BenchmarkRefresh` in `internal/mcp` measures the
extra listing at 0.28 ms per turn for an HTTP server on loopback and 0.2 ms for a
stdio child, each offering 8 tools. At that cost `merud` lists every turn and
keeps no timer or cache age. It doesn't act on the
`notifications/tools/list_changed` message: an HTTP server sends it on the stream
`merud` keeps closed, and the listing each turn catches the same change, for
either transport, before the model sees the tools.

| | What happens |
| --- | --- |
| `merud` starts | one try per server |
| a turn on a tools route | `tools/list` to each connected server; one try per server that isn't connected, or whose listing failed, before the tool list |
| a turn that offers no tools, or no turn at all | nothing |
| a call whose session is gone | fails, marks the server not connected, and isn't sent again; the next turn tries the server |

`merud` never sends a failed call again on its own: the server may have run the
tool before the session broke, and a tool such as `send_gmail_message` would then
run twice. The model sees the error and can ask again on the next turn.

There is no loop, timer, goroutine or backoff, and nothing runs while nobody asks: a
server that fails and is never needed again is never touched again. The one
goroutine per session waits for the session to end so that `/mcp` reports a dead
server at once; it marks the server not connected and never reconnects. So you can
start `workspace-mcp` after `merud`, or restart it with new tools, and your next
question sees it with no restart of `merud`.

```toml
[[mcp.servers]]
name    = "obsidian"
command = "uvx"                   # stdio: merud starts this process
args    = ["mcp-obsidian"]
env     = { OBSIDIAN_API_KEY = "secret:obsidian_api_key", OBSIDIAN_PORT = "27124" }  # stdio only
allow   = ["obsidian_simple_search", "obsidian_get_file_contents", "obsidian_append_content"]  # tool-level allowlist
confirm = ["obsidian_append_content"]   # allowed tools that still need a yes per call
always_confirm = []               # ask every call, with no approval for the session
timeout = "60s"                   # longest one call may take; the default

[[mcp.servers]]
name    = "google"
url     = "http://127.0.0.1:8000/mcp"   # Streamable HTTP: you start the server
headers = { Authorization = "secret:google_token" }   # value from secrets.toml
allow   = ["search_gmail_messages", "send_gmail_message"]
confirm = ["send_gmail_message"]
remote  = false                          # true only if the URL isn't loopback
```

A server entry has either `command` or `url`. `env` goes only with `command`: a `url`
entry with `env` fails to load, with a message that points at `headers`. `merud`
refuses a Streamable HTTP URL that isn't loopback unless the entry says
`remote = true`, the same rule A2A agents follow. `remote` covers one thing: whether
`merud` may connect to a URL on another machine. It says nothing about what the
server itself reaches; `google` has `remote = false` and talks to Google all day.
Before the rename it was called `network`, and a config that still says `network`
fails to load with "network was renamed remote". `env` and `headers` values may name
a secret as `secret:<name>` (see [Adding an MCP server](#adding-an-mcp-server)).

`merud` reads the servers when it starts, and again on a **reload**: after
`configure` adds a server, and when a client sends the `mcp_reload` op, as
`meru mcp add` does. A reload reads `config.toml` and `secrets.toml`, builds a new
pool from the servers config lists now, swaps it in, and closes the old one, which
stops its child processes. So a server you add, change or remove takes effect
without a restart. A config that doesn't load, or a bad server entry, fails the
reload with an error that names the problem, and the old pool keeps running.

`merud` can also **probe** a server that isn't in config yet (the `mcp_probe` op):
start it, list every tool with its `readOnlyHint` and `destructiveHint`, and stop
it (see [Adding an MCP server](#adding-an-mcp-server)). A probe needs no allow list,
since it calls no tool, and the model never sees what it finds. It is a user
command, like the memory ops, so it doesn't go through `dispatch`.

**Status.** `meru mcp` (or `meru mcp status`) and `/mcp` in `meru chat` show one
row per configured server:

```text
$ meru mcp
SERVER     TRANSPORT  STATE         TOOLS  ALLOWED  CONFIRM
google     http       connected       124        8        2   127.0.0.1:8000/mcp
obsidian   stdio      not connected     —        5        1   exec: "uvx": executable file not found in $PATH
```

| Column | Meaning |
| --- | --- |
| `SERVER` | the `name` from config |
| `TRANSPORT` | `stdio` or `http` |
| `STATE` | `connected` or `not connected`; config has no key that turns a server off, so there is no third state |
| `TOOLS` | how many tools the server offers, from its `tools/list`; `—` when not connected, since there is no list to count |
| `ALLOWED` | how many tools config allows |
| `CONFIRM` | how many allowed tools ask first, from `confirm` and `always_confirm` |
| last field | the URL of an HTTP server, or why the server isn't connected |

The data comes from the `mcp_status` op, which `merud` answers from config and the
pool's own record of each server. It sends nothing to any server, so the view is
instant and works while a server is down. `TOOLS` and the warnings in `meru tools`
come from the last listing, which each turn on a tools route renews, so after you
restart a server they catch up on your next such question. `ALLOWED` and `CONFIRM` come from config
and show either way, which tells you what you would get. `meru mcp --json` prints
the same rows for scripts. Tool names stay out of this view: `meru tools` lists each
server, whether `merud` reached it, the tools the model may use and which of them
ask first, and warns about each `allow` entry the server doesn't offer, most often a
typo.

Tools are **deny-by-default**. A server that offers 40 tools gives the model none
until you allow specific ones.

- **Commands ask every time.** A tool in `always_confirm` asks before every call and
  offers only "approve once" and "deny", like the built-in `configure`. It is for a
  tool that runs commands, where a session approval would let the model run any
  command unseen for the rest of the session. No catalog entry needs it today.
- **No wildcards.** `allow` and `confirm` name each tool; `merud` refuses `*` or any
  other pattern. A wildcard would admit tools a server adds in a later release,
  which nobody has read.
- **A timeout per call.** Each server entry may set `timeout`, 60 seconds unless
  set. When it passes, or you cancel the turn, Meru tells the server to cancel the
  call.
- **One try per turn, no retry loop.** A server that crashes, restarts or fails to
  start gets one try at the start of the next turn on a tools route (see above). A
  call to a server that isn't connected fails at once. Nothing retries on a timer,
  so a server that crashes on start doesn't spin.
- **A short environment for stdio servers.** A child process gets only `PATH`,
  `HOME` and the few variables Windows programs need, plus the entry's own `env`.
  The rest of `merud`'s environment stays out, because it may hold another tool's
  API key.

Meru doesn't confine MCP servers. Each one runs as an ordinary process with your
user's permissions, and can read files or reach the network by itself. Adding a
server to config is a trust decision: Meru decides which tools the model may call,
and the server decides what each call does.

Meru uses no MCP shell server. It runs the programs you declare in `[[commands]]`
itself (see [Local commands](#local-commands)), because a shell server confines
nothing Meru doesn't, adds a runtime and a process to supervise, and keeps the real
policy, which program with which arguments, where `dispatch` can't see or log it.

---

## Local commands

Meru runs local programs as built-in tools that you declare in `config.toml`. The
model picks a declared command and fills in its parameters. It never writes a
command line.

```toml
[[commands]]
name        = "git-log"
description = "Commits from the past week in one of the user's repositories"
argv        = ["git", "-C", "{repo}", "log", "--since=1.week", "--oneline"]
timeout     = "10s"           # default 30s, at most 300s
confirm     = false           # true asks before each run
cwd         = "~"             # the default
env_allowlist = []            # names passed through besides PATH, HOME and LANG

  [commands.params.repo]
  type        = "path"        # string, int, enum or path
  under       = "~/repos"     # a path must resolve inside this folder
  description = "The repository's folder, such as meru"
```

Each entry becomes one tool, `cmd.<name>`, whose description ends with the argv
template, and whose argument schema has one required property per parameter.

**Why named commands, not a command allowlist.** One `run_command` tool with a list
of allowed programs looks like deny-by-default and isn't. Half the Unix toolbox runs
a shell through its flags:

| Allow this | And you have allowed |
| --- | --- |
| `git` | `git -c core.pager='sh -c …' log` |
| `find` | `find . -exec sh -c … \;` |
| `awk` | `awk 'BEGIN{system("…")}'` |
| `tar` | `tar --checkpoint-action=exec=…` |
| `ssh` | `ssh host <any command>` |
| `rsync` | `rsync -e '…'` |

`sonirico/mcp-shell` carried CVE-2026-55581 (CVSS 8.4) until 0.6.0: its validator
checked only the first token and let `/bin/bash -c` through. Checking arguments
means owning a validation engine that is never finished. A declared command needs
none: the program and its flags come from config, and a parameter fills one argv
element.

**How a call runs.**

1. `exec.CommandContext(argv[0], argv[1:]...)`. Never `sh -c`, never a string.
2. A `{param}` placeholder becomes part of exactly one argv element, even when the
   value holds spaces, quotes or a semicolon. A placeholder inside a longer element,
   such as `"--repo={repo}"`, is fine: the result is still one element. `{{` and
   `}}` write a literal brace; any other brace fails the entry at startup.
3. `merud` refuses at startup an `argv[0]` that runs code given as text: `sh`,
   `bash`, `zsh`, `dash`, `ksh`, `fish`, `python`, `perl`, `ruby`, `node`, `env`,
   `pwsh`, `powershell`, `cmd` and `osascript`, matched on the base name without
   case, `.exe` or a version (`python3.12` counts). You declared the command, so this
   guards against a slip, not an attacker; a script of your own, run by its path, is
   fine.
4. The program gets `PATH`, `HOME` and `LANG`, plus the names in `env_allowlist`,
   and on Windows `SystemRoot` and `USERPROFILE`. The rest of `merud`'s environment
   stays out.
5. It starts in `cwd`, your home folder unless set.
6. Each run has a timeout, 30 seconds unless set, at most 300. On Unix the program
   runs in its own process group, and the timeout kills the whole group, so a child
   it started dies too. On Windows the timeout kills the program alone.
7. `merud` keeps the first 1 MiB of stdout and of stderr and says when it cut.
8. The model reads the exit code, stdout, and stderr when there is any, each
   labelled. A non-zero exit is a result, not an error: the call's outcome is `ok`,
   and the model sees the code and decides what to do.

**Parameter types.** Every parameter is required, and an unknown one fails the call.

| Type | Accepts |
| --- | --- |
| `string` | non-empty text, no null byte, at most `max_len` bytes (default 4096). It may not start with `-` when it fills a whole element, where the program would read it as a flag. With `pattern` set, a Go (RE2) regular expression, the whole value must match it, and the tool's schema carries the same pattern |
| `int` | a whole number, within `min` and `max` when set |
| `enum` | one of `values` |
| `path` | a path that exists and, once `filepath.EvalSymlinks` resolves every link, lies inside `under`. `~` expands, and a relative path starts at `under`. The argv gets the resolved path |

A path resolves before the check, because a prefix check on the path as written lets
a symlink out of the tree. This mirrors `write_file`'s rule for `~/meru-output/`.

**Checked at startup.** `merud` refuses to start, naming the command, on a duplicate
or badly formed `name`; an empty `argv`; a placeholder or an interpreter in
`argv[0]`; a placeholder with no parameter, or a parameter no placeholder uses; an
unknown type, or a key the type doesn't take; a `path` with no `under`; an `under` or
`cwd` that isn't an existing folder; an `enum` with no `values`; `min` above `max`;
a `pattern` that doesn't compile; and a timeout over 300 seconds. A program missing from `PATH` gets a warning in
`merud.log`, not a refusal. `merud` reads the list when it starts; restart it after a
change.

**The audit record.** `dispatch` asks the commands backend, before it writes the
`tool_call` line, which argv the call will run, and records
`{"argv":[...],"params":{...}}` in place of the model's arguments: in the transcript,
the approval prompt and the `tool_calls` row. An audit log without the arguments
isn't one. `meru log` shows the argv as a command line, and `meru tools` shows each
command's template. The `meru.dispatch` span carries `meru.command.exit_code` and
`meru.command.truncated`; the argv goes on the span only with
`capture_content = true`, since a path or search term can say something about the
question. The metrics use `kind = "command"`, `server = "meru"` and
`tool = "cmd.<name>"`, all names from config, never the arguments.

**GitHub through `gh`.** The config template carries six read-only commands
for GitHub's `gh` CLI, all commented out: `gh-prs`, `gh-pr`, `gh-issues`,
`gh-issue`, `gh-runs` and `gh-repos`. Each asks `gh` for `--json` with a fixed
list of fields and a fixed `--limit`. A `repo` parameter must match
`owner/name` and an `owner` one name, so the model can't pass a URL, a host or a
flag. `gh` keeps the token that `gh auth login` stored in the system keychain,
and finds it with `HOME` alone, so Meru never holds a GitHub token. A command
that writes, such as `gh issue comment`, stays out of the template; a user who
adds one sets `confirm = true`.

**Machine facts, snapshots, `sed` and `awk`.** The template also carries 15
read-only commands for macOS, commented out. Seven report facts about the
machine, each with a fixed argv: `kernel` (`uname -a`), `macos-version`
(`sw_vers`), `hardware-summary` (`sysctl` with named keys), `system-report`
(`system_profiler -detailLevel mini` and one section from an enum; the mini
level leaves out the serial number), `battery` (`pmset -g batt`), `disk-list`
(`diskutil list`) and `uptime`. Four take a snapshot of the machine:
`system-load` and `top-processes` run `top -l`, which prints once and exits,
`find-process` runs `pgrep`, and `memory-free` runs `memory_pressure -Q`. `htop`
and a bare `top` redraw the screen until someone quits, so a command can't use
them. The other four run `sed` or `awk` with the script in config: `file-lines`,
`count-lines`, `csv-column` and `csv-sum`. The model fills in line numbers, a
column number and a file, never script text, since a pattern of its own could
end a `sed` address and add a `w` command that writes a file. `perl` stays
refused. These commands don't skip secret files the way `read_file` does, so
their `under` names `~/Documents`, not the home folder.

---

## Web search

Meru searches the web through SearXNG, a metasearch engine you run on your own
machine. SearXNG keeps no index of its own. It passes the search words to engines
such as Google, Bing, DuckDuckGo and Wikipedia, drops the cookies and headers that
identify you, and merges what comes back. It needs no account and no API key. You
start it once, in Docker ([docs/running.md](docs/running.md), "Web search"), and
`merud` reaches it on loopback, as it reaches Ollama.

Two built-in tools use it. Both go through `dispatch`, so every search, page and
download lands in the transcript and in `tool_calls`. While `web_search` is on,
both go with every route, `direct` included (see
[Who decides what](#who-decides-what)); without it, `web_fetch` waits for a
tools route:

| Tool | Offered when | What it does |
| --- | --- | --- |
| `web_search` | `[web] searxng_url` is set and `[builtin] tools` lists it, as both are by default | One GET to `<searxng_url>/search?format=json`, with a 15-second limit. It returns up to `max_results` results, numbered, each with its title, URL, a snippet cut to 300 characters and the date when SearXNG has one. Its description tells the model to cite results by URL. |
| `web_fetch` | `[builtin] tools` lists it, as it does by default | Fetches one public page (HTML, PDF or plain text, at most 5 MiB, 20 seconds). It works in three modes, described below. HTML and PDF go through the indexer's own readers. |

```toml
[web]
searxng_url = "http://127.0.0.1:8888"   # loopback only; "" turns web_search off
max_results = 8                         # 1 to 20
```

`web_fetch` has no key under `[web]`: `[builtin] tools` is its one switch. A
config that still says `fetch` or the older `read_pages` under `[web]` stops
`merud` with a message that says to list `web_fetch` in `[builtin] tools`, or
leave it out to turn page fetching off.

`web_fetch` takes `{"url", "prompt"?, "offset"?, "save"?}`:

- **Without `prompt`** it returns the page's text, 12,000 characters per call,
  with the offset for the next call, as `read_file` does.
- **With `prompt`** it hands the page's text, up to 48,000 characters from
  `offset`, to the `fast` model with thinking off, and returns
  `From <final URL> (fetched <date>): <answer>`. The instruction is short: answer
  only from the page, quote numbers, versions and dates as the page writes them,
  and say when the page doesn't say. A longer page gets an answer from its first
  48,000 characters, and the result says so and gives the offset for the rest.
  The call gets a `gen_ai.chat` span under the call's `meru.dispatch` span; its
  tokens don't join the turn's usage, as the router's and the skill pick's don't.
  The tool description tells the model to pass a prompt for one fact or a
  summary, and to leave it out when it needs the text itself.
- **With `save`** it downloads the file to `<[skills] output_dir>/downloads/`,
  at most 50 MiB and 2 minutes, through the same client and checks as a fetch.
  The name comes from the `Content-Disposition` filename or the URL's last path
  part, cut down to letters, digits, `.`, `-` and `_`; a name in use gets `-2`,
  `-3` and so on, so a download never overwrites. The file gets mode `0600`, and
  the write goes through an `os.Root` that refuses symlinks. The result gives the
  path, the size, the content type and, for HTML, PDF or text, the first 2,000
  characters of its text. `read_file` and `grep` can read the downloads folder;
  the indexer never indexes it.

**The URL guard.** A URL is a way out: `https://attacker.example/?notes=<your notes>`
carries data in its path or query, and a page the model reads can ask it to build
one. So `web_fetch` runs without asking only when the URL appeared, in the same
session, in a `web_search` result or in something the user typed: this turn's
question or an earlier one. The guard compares URLs with the scheme and host in
lower case and the fragment dropped; the query counts, because that is where data
would go. A URL with no query string also runs unasked when its host is one a known
URL came from: the model often knows a site's canonical page, such as
`go.dev/doc/devel/release`, when the results showed only `go.dev/dl/`, and an
attacker's site still has to turn up in real search results first. Any other URL
asks, offering once and deny with no session choice, as
`configure` does; a session choice would let every later made-up URL through.
`save` asks every time, even for a known URL, because a download stays on disk;
there it offers once, session and deny, as `write_file` does. A scheduled job has
nobody to ask, so `dispatch` declines a call that asks there.

`dispatch` decides whether a tool asks per tool, from `Confirm`. For the guard, a
backend may also have `ConfirmCall(call)`, which decides per call and which
`dispatch` asks first. The built-ins keep each session's known URLs in memory:
`web_search` adds each result it shows, and `ConfirmCall` adds the URLs in the
call's `Question`, which the agent fills with the user's words (this turn's
question and the earlier questions in the session's history), never with excerpts
or tool results. The sets end with `merud`; at most 256 sessions keep one, the
session used longest ago goes first, and one session holds at most 2,000 URLs.

**Why built in.** SearXNG's API is one GET that returns JSON. The MCP server that
wraps it, `mcp-searxng`, needs Node.js, an npm package to pin and a child process
per start; the built-in tools need a few hundred lines of Go and nothing to
install. SearXNG needs no key, account or card, so web search works on day one.

**What stays on the machine.** `searxng_url` must be a loopback address, and
`merud` refuses to start otherwise. The search client uses no proxy and follows no
redirect. Your question, your files and the answer stay here.

**What leaves.** SearXNG sends the search words to the engines it asks, with no
account and no cookies, spread across several companies. The model writes those
words, or `merud` does on a web-first turn, so they can hold words from your
question. The named rule (see [Web first](#web-first)) sends a search with no
request from you: a name your files don't cover, in quotes, with a few words of
the question. A question that says "my" or "our" never goes, and neither does
one about mail, a calendar or notes.

**Fetching pages is on by default.** Asked for the latest Go release, the
`lite` model trusted months-old snippets and answered 1.26; the answer sat on
go.dev's release page, which it couldn't read. So `merud` fetches public pages
when the model asks, under the URL guard, and taking `web_fetch` out of
`[builtin] tools` turns that off. It is the one case where `merud` connects off
this machine with no `remote = true` entry. The site sees your IP address and a
User-Agent that names Meru. `web_fetch` keeps no cookies and uses no proxy. It
checks each address as it connects, after DNS, and refuses loopback, private
networks (10/8, 172.16/12, 192.168/16, fc00::/7), link-local addresses
(169.254/16, where cloud metadata services answer, and fe80::/10), unspecified and
multicast addresses, 0.0.0.0/8 and 100.64.0.0/10. The check runs on the connection
itself, so it catches a link, a redirect (the tool follows at most 5) or a DNS
name that points inside your network, and the model can't use the tool to read a
service on your machine or LAN.

**When SearXNG isn't there.** `merud` starts anyway, and logs one line that says
whether SearXNG answers JSON. The check sends an empty query, which SearXNG refuses
without asking any engine, so it sends nothing off the machine. A `web_search` call
then fails with "SearXNG isn't answering on <url>. See "Web search" in
docs/running.md.", and the model tells you. A SearXNG that answers HTML has JSON
turned off, and the error names the `search.formats` setting in `settings.yml`.
`meru setup` runs the same check in its Web search step.

### Web first

A model left to decide when to search can guess wrong. In a real session with
`qwen3.6:35b-a3b-mxfp8`, thinking off, the user asked about a product released
after the model's training. Asked to search for it and compare it with Meru, the
model searched for an older product with a similar name and answered about that
one. After the user pasted the product's page, three follow-ups called no web
tool: their history held only the questions and answers, and the model made up
features. "do a web search about quick and educate yourself" routed `direct` at
0.55, and the model called `web-research`, a skill, as a tool, twice, then
apologised. Only "do some deep research about quick", at `tools` 0.86, got two
searches, four pages and a right answer.

So `merud` itself searches before the model's first round in two cases:

| Case | When | What `merud` runs |
| --- | --- | --- |
| `asked` | the question asks for the web (see [Routing](#routing), the fourth rule) and `web_search` is on; or it holds an http or https URL and `web_fetch` is on; or the desktop app's scope is web | `web_fetch` on each URL, two at most. With no URL, `web_search` on the question without its URLs, the phrases that asked for the web, a closing instruction ("and tell me what it is in three lines") and the filler words at either end (see below); a follow-up too short to stand alone gets the earlier question, as a file search does, and one that speaks only of the web searches for the earlier question alone |
| `named` | an `auto` turn searched your files first, no connected tool is the question's target, the question doesn't say "my", "mine", "our" or "ours", it names a thing, and the excerpts don't cover it | `web_search` on the name in double quotes, then up to five of the question's other words that aren't filler |

The calls go through `dispatch` like any other. Their `tool_call` lines and
`tool_calls` rows carry `caller = "meru"`, so `meru log` shows `by meru` in the
approval column, and each sends its `tool_call` and `tool_result` events, so the
chat shows the search. What comes back sits in the prompt under "From the web",
among the parts each question changes, with a rule that mirrors the one for your
files: cite each source by its URL, never swap in a product, company or person
you know for the one the user named, and search again with other words when the
results are about something else. The section holds at most 8,000 characters,
in equal shares among the calls. The route becomes at least `tools` (`direct`
becomes `tools`, `search` becomes `search+tools`), so the model can search again
or read a page. A call that fails, as when SearXNG doesn't answer, leaves the
section out; the model still has the tools.

**The search words for an asked turn.** No model writes them; `merud` cuts the
question down with short word lists. A closing instruction goes: "and" or
"then" before a verb that asks for an answer ("tell", "explain", "summarize",
"give", "write", "list", "describe", "show", "say"), and a length at the very
end ("in three lines", "in 50 words"). So "search the web for Acme Flow pricing
and tell me what it costs in three lines" searches for "Acme Flow pricing". A
follow-up that speaks only of the web searches for the session's latest earlier
question that doesn't, cleaned the same way. It speaks only of the web when no
words are left once the web phrase and the filler go, or when fewer than three
subject words are left and one of them speaks of what Meru can reach, such as
"have", "access", "use" or "go ahead". A real session drove this. After the
song question in [Who decides what](#who-decides-what), the user wrote "you
have accerss to web search", and `merud` searched for "have accerss what does
the song ... acvtually mean", the follow-up's words glued to the question
before it. Now it searches for "song maname maname sung by r. devi acvtually
mean". A follow-up with a subject, such as "search the web for the pricing",
still gets the earlier question after it.

**Named things.** The detector reads English and knows three shapes: a term in
double quotes; a run of capitalised words, such as "Acme Flow", where a lone
capitalised word that starts a sentence doesn't count and a lone word needs
three characters; and a word shaped like a product name anywhere, such as
"GitHub", "iPhone" or "qwen3". "I", "Meru", the months, the days, greetings and
the short words a title capitalises never count.

**Files that don't cover a name.** The excerpts cover a name when the best one
scores above 1/61 and at least one holds the name in its text, heading or path.
Vector search ranks every chunk, so it always hands back a nearest one, however
unrelated. Reciprocal-rank fusion gives the top chunk of one list 1/61, about
0.0164, and a chunk both lists found at least 2/110, about 0.0182, so a best
score of 1/61 or less means only one search found the chunk. The keyword search
matches any word of the question, "what" and "is" included, so a strong score
alone doesn't show that your files know the name.

**What it skips.** A `direct` question never gets the named search: no file
search ran, and the router judged it small talk. Neither does a question that
points at mail, a calendar or notes, a turn in `agentic` retrieval, or a scope
of files, talk or mail. When the tool a case needs is off, the turn goes on
without the section and a debug line says why. The turn's info log line carries
`web_first` (`asked`, `named` or `none`), and the turn span
`meru.turn.web_first`.

**Follow-ups remember the web.** Each web call that succeeds, `merud`'s or the
model's, leaves notes on the turn's assistant line (see
[Session transcripts](#session-transcripts)), and a later turn's history carries
them after the answer. Each note's gist comes from the result as `dispatch`
returned it, after the redaction of secrets, and holds no more than 300
characters of a page.

---

## Other agents (A2A)

Meru hands tasks to other agents over A2A, an open protocol for agent-to-agent work.
As with MCP, Meru is only a client; it doesn't serve A2A. The client comes from the
A2A project's Go SDK, `github.com/a2aproject/a2a-go/v2` (v2.5.0), and speaks version
1.0 of the protocol over JSON-RPC or REST. An agent whose card speaks only an older
version fails with an error that says so.

The model sees each allowed agent skill as one more tool, named
`a2a.<agent>.<skill>`, that takes one argument, `{"message": "..."}`, and returns
the agent's answer as text. A2A has no field that picks a skill: the agent reads
the message and decides. The skill in the tool name picks the description the
model sees and the `allow` entry the call needs. The call goes through the same
`dispatch` function as MCP tools, so it gets the same allowlist check,
confirmation, `tool_calls` row (with `kind = "a2a"`) and trace span.

```toml
[[a2a.agents]]
name    = "research"
url     = "http://127.0.0.1:9100"   # Meru reads the agent card from here
allow   = ["summarize"]              # skills from the agent card
confirm = []
remote  = false                      # true only if the agent isn't on loopback
timeout = "60s"                      # the default
```

![Sequence diagram of one A2A call across the main model, the agent loop, dispatch and a research agent. The main model asks for the tool a2a.research.summarize with a message. The loop hands the call to dispatch, which checks the allowlist, asks you if the skill sits in the confirm list, and opens a span. Dispatch sends SendStreamingMessage to the agent, which streams task updates and then a final artifact. Dispatch writes a tool_calls row with kind a2a, closes the span and returns the result text, which the loop passes to the main model as the tool result.](docs/architecture/img/300-a2a.png)

*Figure 6. To the model, a remote agent is one more tool. Everything that makes the call safe to audit happens in the two boxes on the `dispatch` line, the same boxes an MCP call passes through.*

Agents are deny-by-default too. Meru can reach only the agents in config, and only
the skills in `allow` become tools. An agent on another machine may use a cloud
model, so your data would leave the machine. Reaching one takes `remote = true`;
without it, `merud` refuses any agent URL that isn't loopback. As for MCP, `remote`
covers only where `merud` connects, and a config that still says `network` fails
with "network was renamed remote".

- **Lazy card fetch.** `merud` starts without contacting any agent. It reads an
  agent's card the first time a turn needs its tools, so an agent that starts after
  `merud` shows up on the next turn. A failed fetch waits 10 seconds before the
  next try; inside that wait the agent offers no tools and a call fails at once.
- **Always a stream.** Meru sends every call with `SendStreamingMessage`, under the
  same per-call timeout as a tool (60 seconds unless the entry sets `timeout`).
  When the card says the agent can't stream, the SDK sends a plain request instead,
  so one code path covers both. When the timeout passes or you cancel the turn,
  Meru asks the agent to cancel the task.
- **No follow-up turns.** An agent that asks for more input ends the call with an
  error result. Carrying a task across turns waits until something needs it.
- **Guards on the connection.** The config check covers the card's URL, but the
  card names the URLs the calls go to. So the client's dialer checks each address
  just before it connects, after DNS, and refuses anything off loopback unless the
  entry says `remote = true`. The client follows no redirects, because a redirect
  could carry the entry's `headers`, which may hold an API key, to another host.

---

## Scheduler

A job is a prompt plus a cron expression, declared in `config.toml`:

```toml
[[jobs]]
name     = "morning-brief"
schedule = "0 7 * * 1-5"          # 7:00 on weekdays
prompt   = "Brief me on today's calendar, unread email and anything due this week."
output   = "notification"          # or "digest", or a file path
```

Each run is a session with its own transcript, so a job's work lands in the same log
and traces as your questions. `merud` runs it through the same agent loop
as a question you type, and sends the output to a digest, a file or a notification.
Jobs don't stream, since no one is watching, and nobody can approve a tool call, so
`dispatch` declines every call that needs a yes during a job. That includes a local
command with `confirm = true`: its outcome is `declined`, its row still goes in, and
the job's output says it skipped the call, so a brief with a missing section has a
reason on record.
The operating system's service manager (`launchd`, `systemd` or a Windows service)
restarts `merud` after a reboot. The scheduler lives inside `merud`, not in the
service manager, so jobs use the loaded models and land in the same audit log and
traces.

---

## Observability

Every stage of a turn emits OpenTelemetry (OTel) metrics and traces: routing,
retrieval, each model call and each tool call. A dashboard can then show where a slow
answer spent its time, how many tokens it used, and which tool failed.

### Local only

Meru collects this data about itself, and none of it leaves your machine. It goes
only to an endpoint you run on this machine:

- The OTLP (OpenTelemetry Protocol) exporter stays **off until you set
  `observability.otlp_endpoint`.** Without an endpoint, metrics go to a no-op
  provider and spans record nothing. Each span still gets a trace ID, so the log and
  the transcript can name the turn. Instrumentation then costs almost nothing.
- `merud` **refuses to start if the endpoint isn't a loopback address.** No setting
  sends metrics or traces anywhere else.
- **Spans carry no prompt or response text** unless you set `capture_content = true`.
  They always carry token counts, durations, model names and tool names, which reveal
  nothing about what you asked.

```toml
[observability]
otlp_endpoint    = "http://127.0.0.1:4318"   # OTLP/HTTP; loopback only; unset = off
metrics_interval = "10s"
traces           = true
capture_content  = false                     # prompt/response text in spans
```

### Traces

Each turn produces one trace. A question over the socket starts at `rpc.request`;
a scheduled job (v0.5) starts at `meru.turn`. In v0.3 a turn on the `search+tools`
route with one round of tool calls looks like this:

![Trace tree for one v0.3 turn on the search+tools route with one tool round. rpc.request, with op, source and question length, holds meru.turn, with route, source, session, iterations and outcome. Under meru.turn, in order: meru.session; meru.transcript.append for the user line; meru.route, with a gen_ai.chat fast span and its POST /api/chat; meru.search, with meru.retrieve and its POST /api/embed; meru.prompt; gen_ai.chat main for round 1, with its POST /api/chat; meru.dispatch, one per call, holding a tools/call span for MCP or an invoke_agent span for A2A; meru.transcript.append for the call's lines; gen_ai.chat main for round 2, the answer, with its POST /api/chat; and meru.transcript.append for the assistant line. The same trace ID goes on the transcript lines, the messages and tool_calls rows, and every log line of the turn.](docs/architecture/img/300-trace.png)

*Figure 7. One v0.3 turn on the `search+tools` route with one tool round. The router's one-token call and each round of the main model get a `gen_ai.chat` span with its HTTP request under it; the search sits before the first round, and each tool call gets a `meru.dispatch` span between the rounds. The trace ID also sits on the transcript lines and every log line of the turn, so a slow bar in Grafana leads to the exact lines, and a line leads back to its timing.*

A turn on the `direct` route has no `meru.search`, unless the question names an
indexed folder. A turn on `direct` or `search` has no tool spans, and one
`gen_ai.chat main`. The calls of one round run at the same time, so their
`meru.dispatch` spans overlap. v0.4 adds memories under `meru.retrieve`, and a
`meru.retrieve.sessions` span under `meru.turn` for past sessions. Each session
summary is a trace of its own: `meru.summarize`, with the fast model's
`gen_ai.chat` inside.

Indexing has traces of its own, apart from any turn. A scan of every folder is one
`meru.index.scan` span (folders, whether it re-embeds, and the counts it ends with),
with a `meru.index.file` span for each file it reads (kind, outcome, chunks, bytes,
and the skip reason). Indexing one path, as the watcher and `meru index <path>` do,
records only `meru.index.file` spans. The startup scan and each file the watcher
re-indexes start a new trace; `meru index` puts its spans under its own
`rpc.request`.

Model spans follow the OTel GenAI semantic conventions (`gen_ai.operation.name`,
`gen_ai.request.model`, `gen_ai.request.max_tokens`, `gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons`) and add `meru.tier`
plus Ollama's own timings: `meru.ollama.load_ms`, `meru.ollama.prompt_eval_ms` and
`meru.ollama.eval_ms`. The HTTP spans follow the OTel HTTP conventions. A failed
span records the error and sets its status to Error; a cancelled one gets a
`cancelled` event instead.

Every tool call gets a `meru.dispatch` span, whatever its kind. It carries
`gen_ai.tool.name`, `meru.tool.kind`, `meru.tool.server`, `meru.tool.outcome` and
`meru.tool.approval`, the same keys the tool metrics use, so a dashboard can go from
a metric to its traces. The backend's own span nests under it:

- **MCP** spans follow the OTel MCP semantic conventions: each is named
  `tools/call <tool>` and carries `mcp.method.name` and `gen_ai.tool.name`.
- **A2A** spans follow the GenAI convention for a call to a remote agent: each is
  named `invoke_agent <agent>`, with `gen_ai.operation.name = invoke_agent`,
  `gen_ai.agent.name`, `server.address` and `server.port`. Meru adds
  `meru.a2a.skill`, `meru.a2a.task.id` and `meru.a2a.task.state`.
- **Built-in** tools have only the `meru.dispatch` span, except `web_fetch` with a
  prompt, which adds the `fast` model's `gen_ai.chat` span under it.
- **Local commands** add `meru.command.exit_code` and `meru.command.truncated` to
  the `meru.dispatch` span, which has no child.

MCP and A2A spans also carry `meru.tool.server` and `meru.tool.allowed`. A failed
call's `error.type` says how it failed: `tool_error` (the MCP convention's name)
when the server or agent reports failure, or Meru's own `denied`, `unavailable` or
`timeout`. Arguments and results go on the spans only when `capture_content = true`.
`merud` writes each trace ID to the session transcript, `messages` and
`tool_calls`, and to every log line of the turn.

### Metrics

Metrics use the standard GenAI names where a convention exists, and `meru.*` names
for the rest.

| Metric | Type | Attributes | Answers |
| --- | --- | --- | --- |
| `gen_ai.client.token.usage` | histogram | model, tier, `gen_ai.token.type` (input/output) | tokens per call |
| `gen_ai.client.operation.duration` | histogram | model, tier, operation | model call latency |
| `gen_ai.server.time_to_first_token` | histogram | model, tier | the v0.1 "first token < 1 s" target |
| `gen_ai.server.time_per_output_token` | histogram | model, tier | decode speed |
| `meru.engine.load.duration` | histogram | model | cold loads Ollama had to do (should be ~0) |
| `meru.route.decisions` | counter | route, outcome (ok/low_confidence/degraded) | how often each route wins, and how often the router is unsure |
| `meru.turn.duration` | histogram | route, source (cli/tui/job), outcome (ok/error/cancelled/timeout/cut_off/gave_up/bad_output) | end-to-end latency; its sum over `outcome="ok"` is active time |
| `meru.sessions` | counter | source | sessions started |
| `meru.turn.tokens` | counter | `gen_ai.token.type` (input/output), route, source | the main model's tokens per answered question, summed over its model calls |
| `meru.turn.docs` | histogram | route | distinct files each answered question read |
| `meru.turn.iterations` | histogram | route | loop depth |
| `meru.context.tokens` | histogram | section (system/skills/memories/sessions/chunks/web/history/tools) | data for the context budget policy |
| `meru.tool.calls` | counter | `meru.tool.kind` (mcp/a2a/builtin/command), `meru.tool.server`, `gen_ai.tool.name`, `meru.outcome` (ok/error/denied/declined/cancelled/timeout) | tool usage and failures |
| `meru.tool.duration` | histogram | `meru.tool.kind`, `meru.tool.server`, `gen_ai.tool.name` | tool latency, for calls that ran |
| `meru.model.malformed_calls` | counter | model | tool calls the main model wrote that Meru couldn't run as written |
| `meru.retrieval.duration` | histogram | stage (vector/fts/fusion/memories/sessions) | retrieval cost (v0.2; memories and sessions stages v0.4) |
| `meru.rpc.active_streams` | up-down counter | — | open client sessions |
| `meru.scheduler.job_runs` | counter | job, outcome | (v0.5) scheduled work |

The OTel Go runtime package adds heap, garbage-collection and goroutine metrics.

**What counts as a malformed call.** The agent counts one for each of these, in
the round where it happens:

- Ollama sends `ErrModelOutput` in the middle of a stream: its parser couldn't
  read what the model wrote, most often a tool call. The retry that follows
  counts again if it fails too.
- A call names a tool the round didn't offer, whether the tool exists or not.
  This covers a call on the last round, which offers no tools, and a call to a
  tool the model made up, which `dispatch` also refuses as `denied`.
- A call's arguments aren't a JSON object. Ollama parses the model's call
  itself, so this is rare.

A repeated call isn't malformed: the model wrote it well, and the agent hands
back the earlier result (see [Agent loop](#agent-loop)). The counter carries
the model name alone, which config bounds; the tool's name, which a model can
make up, stays off it. The answer line's `bad_calls` holds the same count per
turn, for `/usage by model`.

Token counts come from Ollama's counters on each response; Meru doesn't estimate
them. `merud` measures time to first token from the start of the request to the
first streamed text, so it includes time spent waiting in the queue and reading the
prompt.

**Keep attribute values to small, fixed sets** such as model, tier, server, tool,
route and outcome. Session IDs, file paths and text belong on spans, never on
metrics. Server and tool names come from config, with one exception: a denied call
names a tool the model made up, and a model can make up any number. The tool
metrics record such a call's server and tool as `other`; its `tool_calls` row
keeps the real names. `meru.tool.duration` covers only the time a tool ran, so a
call that never ran (denied, declined, or cancelled before it started) adds to
`meru.tool.calls` and records no duration.

### The stack

`merud` speaks plain OTLP/HTTP, so any OTLP backend works. The reference setup is one
container, **`grafana/otel-lgtm`**, which bundles an OTel Collector, Prometheus,
Tempo and Grafana. The repo ships its compose file and a ready-made Meru dashboard.
The compose file:

- binds ports to `127.0.0.1` only (4318 for OTLP, 3000 for Grafana);
- turns off Grafana's own reporting and update checks:
  `GF_ANALYTICS_REPORTING_ENABLED=false`, `GF_ANALYTICS_CHECK_FOR_UPDATES=false`,
  `GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES=false`.

To run the parts as separate binaries (Collector, Prometheus, Jaeger or Tempo), point
`otlp_endpoint` at the Collector. `merud` needs no change.

### Logs

`merud` writes `key=value` lines with `log/slog` to `merud.log` in its home folder,
and doesn't export them. `[log] level` picks how much it writes; `merud -v` forces
`debug`.

- **`info`** (the default): startup settings, each model warm-up, the store's
  counts, each index scan's counts, shutdown, and one `turn` line per turn with its
  route, outcome, total time, time to first token and token counts.
- **`debug`**: adds a line for each stage of a turn: the request, the session, the
  history, each transcript write, the route with its whole distribution, the
  search's result count, the prompt's size, each Ollama call (status, time to headers, first token, Ollama's
  own timings, tokens per second) and the reply.

Every line of a turn carries its `trace_id`, the same ID the trace and the
transcript lines hold. No level writes question or answer text. With
`capture_content = true`, the debug lines add the first 200 characters of each.

---

## Privacy boundary

- The codebase contains no path to a cloud model. The engine talks only to a model
  runtime on loopback.
- You allow MCP tools and A2A skills one by one. A remote A2A agent or Streamable
  HTTP server needs `remote = true` in its config entry. `remote` says where
  `merud` may connect, not what the server reaches: a loopback server such as
  `google` still talks to Google. The A2A client checks
  each address again as it connects and follows no redirects, so an agent card
  can't send your messages elsewhere.
- API keys live in `~/.meru/secrets.toml`, which `merud` refuses to read when other
  users can. Config names them, never holds them, and `dispatch` strips their
  values from transcripts, `tool_calls`, logs and spans. No key passes through the
  model: `configure` sends you to `meru mcp add` to type one. `about_meru`
  reports server names and counts, never a value from `secrets.toml`, an env
  value or a header. The `gh` commands leave GitHub's token with `gh`.
- Meru doesn't sandbox MCP servers. They run with your permissions, so choose them as
  carefully as any program you install.
- Web search sends the search words to SearXNG on loopback, and SearXNG sends them
  to the search engines it asks. That is the one part of a question that leaves
  the machine when the model searches. `merud` also searches on its own before
  the model's first round (see [Web first](#web-first)): for a question that
  asks for the web, and for a name your files don't cover, in quotes with a few
  words of the question. A question that says "my" or "our", or one about mail,
  a calendar or notes, never gets the second search.
- `web_fetch` makes `merud` fetch public pages off this machine, by default,
  when the model asks; taking it out of `[builtin] tools` turns it off. It refuses any address
  on this machine or your network. It runs without asking only for a URL that a
  search result or your own question gave in the same session, or for a page
  with no query string on the same site, so the model can't carry your data out
  in a URL it made up; any other URL, and every
  download, asks you first, and a scheduled job declines them (see
  [Web search](#web-search)).
- A local command runs with your permissions too, and reaches the network if you
  declare one that does, such as `curl` or `ssh`. You chose it, and the
  `tool_calls` row records each run with its argv.
- No telemetry leaves the machine. Meru's own metrics and traces are off by default,
  go only to loopback when on, and leave out prompt text unless you opt in. Meru
  sends no crash reports and never checks for updates.
- The store is a plain file, readable only by you (mode `0600`). Back it up or
  delete it; it's yours.
- A file outside those folders reaches the model only when you attach it in the
  desktop app. `merud` copies that one file into `~/meru-output/uploads/`, and
  refuses a link, a folder and a file whose name looks like a secret's. An
  attached image goes to Ollama on loopback with that one question, like its
  text, and nowhere else.
- The indexer reads only the folders you list, never follows a symlink, and never
  indexes a file that looks like a secret. The file tools, `read_file`,
  `list_folder`, `grep` and `search_files`, apply the same rules through the
  indexer's own code, so the model can read no file that search couldn't reach.
- `meru log` and the `tool_calls` table let you review every external action.
- The Mac installer downloads only what a step names, after you press Continue:
  Ollama and uv through Homebrew, the models through Ollama, the SearXNG image
  through Docker, and Ollama.app from Ollama's site on a Mac with no Homebrew. It
  runs only the programs on its allowlist, with no shell, and keeps the Google
  client secret in the start script, readable only by you (see
  [Installer](#installer)).
- The desktop app loads nothing from the network. Its page, fonts and libraries
  ship inside the binary, its Content-Security-Policy blocks remote scripts,
  images and connections, and its Wails updater stays unconfigured, so it never
  checks for updates.

---

## Deliberate non-goals

- **Not a chat app clone.** No accounts, sync or mobile client.
- **Not a training framework.** Meru runs weights; it doesn't produce them.
- **Not multi-user.** One machine and one person, which keeps the design simple.
- **No cloud fallback.** Calling a cloud AI service when the local model struggles would
  break principle 1.
- **Not an agent framework.** The loop exists to serve Meru, and we won't package it
  as a library.
- **Not a shell.** Meru runs programs you declared, with parameters the model fills
  in. It never runs a command line a model composed, and ships no shell tool.

---

## Open questions

We'll settle these with working code and measurements.

1. **Router quality.** The router reads probabilities instead of parsing text (see
   [Routing](#routing)). A labelled set of 135 questions and `make router-eval`
   measure it. On the development machine with MiniCPM5-2B, a prompt that puts
   contrastive option text, the indexed folders and two examples per route ahead
   of the turn picks the labelled route for 32 of 40 held-out questions, and takes
   about 28 ms per warm decision. The v0.1 prompt picked 17 of 36. Naming the
   folders lifted held-out search recall from 8 of 12 to 11 of 12. At a
   temperature of 1.25 the probabilities are close to calibrated (expected
   calibration error 0.065 over all 135 rows), so `min_confidence = 0.45` means
   what it says. The weak spot is tools recall (4 of 9 held out): the model answers
   questions about recent events from memory, and sends requests such as "text
   alex that I'm on my way" to search. Next:
   label real turns from transcripts, refit, and decide whether `lite` needs a
   larger `fast` model for tool-heavy use. For the `main` model, [model
   sets](#model-sets) and `/usage by model` now measure a change on real
   questions: time to first token, speed, bad calls and capped turns per
   model, rebuilt from the transcripts.
2. **Context caps.** v0.4 sets a cap per part of the prompt and orders the parts
   for Ollama's prompt reuse (see [Agent loop](#agent-loop), step 2). The caps are
   first guesses; `meru.context.tokens` will show whether any part runs into its
   cap often.
3. **PDF extraction.** v0.2 reads each page's plain text with a pure-Go library and
   keeps no layout, so tables and columns come out as running text. A scanned PDF
   with no text layer yields nothing. Local tools that keep layout are weak, and Go
   has fewer of them than Python; better extraction may need a cgo library or an
   external tool.
4. **Leaving Ollama.** An embedded llama.cpp engine would make `merud` self-contained,
   but Meru would take over tool-call parsing and loading models. v0.3's tool calls
   work through Ollama's parser; decide once we can measure what taking it over
   would cost.
5. **WASM SQLite speed (answered in v0.2).** `ncruces/go-sqlite3` runs SQLite as
   WebAssembly, slower than native SQLite, so v0.2 measured it before building on
   it. On the development machine with 768-dimension vectors, 100,000 chunks index
   in 6.1 s, a file replaces in about 6 ms, a vector search takes 147 ms and a
   keyword search 90 ms; at 10,000 chunks the searches take 13 ms and 9 ms (see
   [Why this driver and this vector store](#why-this-driver-and-this-vector-store)).
   That fits a personal index. If search must get faster, use fewer dimensions or
   compute distances in Go.
6. **How many excerpts to keep.** Each search keeps the top 10 chunks, and with a
   tiny index that is every chunk, so the prompt carries excerpts that don't help.
   The clients list only the sources an answer cites (see [Citations](#citations)),
   so this costs prompt length, not a noisy list. A minimum fused score or fewer
   than 10 chunks would fix it; measurements from real questions will pick one.

### Resolved

- **Language:** Go, for a small footprint, easy distribution, room to scale, built-in
  concurrency and fewer dependencies (see [Why Go](#why-go)).
- **Platforms:** macOS on Apple silicon first, Linux supported, Windows untested at
  first. Cloud servers count as "a machine you control".
- **Agent harness:** our own loop plus the official MCP and A2A Go SDKs. We looked at
  Eino and ADK Go. ADK Go pulls a cloud-model client into the dependency tree, and
  neither saves much once the allowlist, audit and budget logic are ours.
- **Model runtime:** Ollama on loopback for now (see open question 4).
- **Models:** the `lite` profile by default (MiniCPM5-2B + `nomic-embed-text`), and
  `full` for Apple silicon with 32 GB or more, or a ~24 GB GPU (`qwen3.8:27b` +
  `qwen3-embedding:0.6b`).
- **Storage:** JSONL transcripts as the source of truth; SQLite via
  `ncruces/go-sqlite3` as the index, with vectors in a plain table and vec1's
  distance function. No separate vector database.
- **Routing:** a one-token classification read from log probabilities, falling back
  to search and tools when unsure ([docs/fast-router.md](docs/fast-router.md)).
  Temperature 1.25 and `min_confidence = 0.45`, fitted with `make router-eval`.
- **Hybrid search:** FTS5 BM25 plus vector distance over every stored vector,
  merged in Go with reciprocal-rank fusion. The query is the question, plus on a
  short follow-up the session's latest earlier question that isn't only filler
  words; no model call rewrites it. List sizes
  are constants (50, 50, 10) until measurements say otherwise.
- **Indexing:** only the folders in `[index] folders`, nothing by default; secrets,
  hidden files, build folders and ignored files skipped; symlinks never followed;
  email, calendar and Drive reached live through MCP, not indexed.
- **Built-in skills:** `writing`, `explainer`, `web-research` and `file-research` ship in the binary
  and are copied to `~/.meru/skills/` on first run; your edits always win.
- **Setup:** `meru setup` checks Ollama, pulls the models, writes a first
  `config.toml`, and offers a catalog of MCP servers, each added "for you" (with
  approval of the exact config block) or by copy-paste. For a server outside the
  catalog, `merud` probes it first, and `meru` proposes `allow` and `confirm`
  from the tools' read-only and destructive hints. A reload makes the server
  work without a restart.
- **Terminal UI:** Bubble Tea, with Bubbles for input and scrolling, Lip Gloss for
  styling and Glamour for Markdown answers, in `meru chat` only. Answers always
  stream. Each code block gets a `⧉ copy N` label; `/copy N` or Ctrl-Y copies
  it, and so does a click, unless `[chat] mouse_copy` is off. Links in an
  answer show short and open on a click. Questions typed while a turn runs
  wait in a queue of up to five in the client.
- **Tool approvals:** approve once, approve for this session, or deny, asked over
  the same socket as the answer. Session approvals never touch config; lasting
  trust comes only from editing the `confirm` list. With no one to ask (scripts,
  scheduled jobs), `dispatch` declines the call.
- **Tool results:** the model reads up to 16,000 characters of each result; the
  transcript and `tool_calls` keep 4,000, with secrets removed. Base64 runs of
  2,000 characters or more in an MCP or A2A result become a short note first.
- **Secrets:** one file, `~/.meru/secrets.toml`, mode `0600`, referred to from
  config as `secret:<name>`. No system keychain: each platform has its own, and a
  file only you can read works the same everywhere.
- **Memory:** one Markdown file per memory under `~/.meru/memory/<kind>/`, no index
  file; session summaries in the transcripts serve as episodic memory. Memories save
  without asking, through the `remember` tool and `dispatch`.
- **Other agents:** an A2A client on the A2A project's Go SDK (protocol 1.0),
  through the same `dispatch` path as MCP tools.
- **Local commands:** named commands in `[[commands]]`, with typed parameters that
  each fill one argv element, run with no shell; no MCP shell server and no
  command allowlist.
- **Isolation:** no sandbox. Meru runs as an ordinary user process, and the tool and
  agent allowlists do the controlling.
