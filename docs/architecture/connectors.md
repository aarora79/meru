# Connectors: how Meru will run its own servers

A connector is a program Meru needs, such as the search engine behind web
search, that Meru installs, starts, checks and restarts for you. This page walks
through the design in issue #87: what you set, what Meru's developers set, what
happens on disk, and which parts exist today.

> **Where the work stands.** Steps 1 and 2 of 7 are built. Step 1, merged in
> PR #90, added the manifests, their checks, the config table and a test that
> refuses unpinned versions. Step 2 adds the pinned Node and uv and the code
> that installs each connector into `~/.meru/runtime`. `merud` calls none of it
> yet, so Meru behaves as before. Anything marked (planned) below arrives in
> steps 3 to 7.

Meru · 30 September 2026 · written from branch `connectors-step2`

| Figure | What it counts | Source |
| --- | --- | --- |
| 4 | manifests compiled into `merud`: SearXNG, Obsidian, Google and Ollama | `internal/connectors/manifests/`, 29 Sep 2026 |
| Node 24.21.0, uv 0.12.21 | the runtimes Meru downloads for itself, each checked against a pinned SHA-256 | `internal/connectors/runtimes.go`, 30 Sep 2026 |
| 1, 2, 4 … 60 s | wait before each restart after a crash (planned) | ARCHITECTURE.md, 29 Sep 2026 |
| 5 in 10 min | crashes before a connector stops trying and says why (planned) | ARCHITECTURE.md, 29 Sep 2026 |
| 10 min | idle time before Obsidian or Google stops (planned) | `idle_timeout` in both manifests, 29 Sep 2026 |

1. [The problem today](#the-problem-today)
2. [What a connector is](#what-a-connector-is)
3. [Where things live on disk](#where-things-live-on-disk)
4. [The supervisor and its states](#the-supervisor-and-its-states)
5. [Turning on Obsidian, end to end](#turning-on-obsidian-end-to-end)
6. [The four connectors](#the-four-connectors)
7. [Status and Fix](#status-and-fix)
8. [Pinned versions](#pinned-versions)
9. [Moving a working setup over](#moving-a-working-setup-over)
10. [What it doesn't do](#what-it-doesnt-do)
11. [Where the work stands](#where-the-work-stands)
12. [Sources](#sources)

## The problem today

`merud`, Meru's background program, leans on four other programs. Ollama runs
the models. SearXNG, a search engine you host on your own machine, backs the
built-in `web_search` tool. An Obsidian server reads your notes, and a Google
server reaches Gmail, Calendar and Drive. Those last two speak the Model Context
Protocol (MCP), the standard way a program offers tools to a model.

Today you install each of these, start it, and restart it when it stops. `merud`
reads the MCP servers from `~/.meru/config.toml`, starts the ones it can, and
tells you little when one fails. Three failures show what that costs.

**Obsidian fails on every turn.** A common setup starts the server through
`npx`, the Node.js package runner:

```toml
[[mcp.servers]]
name    = "obsidian"
command = "npx"
args    = [
  "-y", "obsidian-mcp", "serve",
  "--vault", "notes=~/Notes/vault",
]
```

On a Mac, launchd, the service manager that starts `merud` at login, hands
`merud` a short `PATH`, the list of folders a program searches for other
programs, and that list holds no Node.js. Each start then fails with this error,
and the server stays down:

```text
exec: "npx": executable file not found in $PATH
```

The same entry works when you start `merud` from a terminal, whose `PATH` does
hold Node.js, so the fault shows up only once Meru runs in the background.

**Web search says "Connected" while SearXNG is down.** Settings marks web search
connected whenever `[web] searxng_url` holds a URL, because `merud` never asks
SearXNG whether it answers. The model still gets `web_search`, and each call
fails.

**Google runs by hand.** `merud` connects to the Google server at
`127.0.0.1:8000` but never starts it. The installer writes a start script and a
launchd job for it. When that job is missing, you start the server in a terminal
and leave the terminal open.

`merud` can't repair any of this, because of how it holds its servers today. The
MCP pool, the part of `merud` that keeps its server connections, connects to
each server once at start, one after another, with 30 seconds for each. It has
no restart and no timer. At the start of each turn that offers tools it tries a
dead server once more, and it never sends a failed call a second time. SearXNG
sits outside the pool. `merud` checks Ollama once, and if Ollama doesn't answer,
`merud` exits before it opens its socket, the file the clients talk to it
through, so no client can show you why.

## What a connector is

A connector is one of those programs that Meru installs, configures, starts,
checks and restarts for you, at a version fixed in Meru's own release. Two files
describe each one, and two different people write them.

**The manifest** belongs to Meru's developers. It is a TOML file, the same plain
format as `config.toml`, at `internal/connectors/manifests/<id>.toml`, compiled
into the `merud` binary. It names the package and its exact version, the command
that starts it, the values to ask you for, a health check (a cheap request that
proves the program works), and which of its tools the model may call. Obsidian's
manifest, without its comments:

```toml
id = "obsidian"
name = "Obsidian"
kind = "stdio"
required = false
default_on = false
idle_timeout = "10m"

[install]
type = "npm"
package = "obsidian-mcp"
version = "2.0.1"

[launch]
command = "obsidian-mcp"
args = [
  "serve",
  "--vault", "{field.vault_name}={field.vault_path}",
]

[[field]]
id = "vault_path"
type = "folder"
label = "Vault folder"
help = "The folder that holds your Obsidian notes."
required = true

[[field]]
id = "vault_name"
type = "text"
label = "Vault name"
help = "A short name the tools use for the vault. Leave it empty and Meru makes one from the folder's name."
required = false
pattern = "^[a-z][a-z0-9_-]*$"

[health]
tool = "obsidian_list_vaults"
args = {}
expect = "nonempty"

[mcp]
allow = [
  "obsidian_list_vaults",
  "obsidian_search_vault",
  "obsidian_read_note",
]
confirm = []
```

`kind = "stdio"` means Meru starts the program and talks MCP over its standard
input and output. `command` names a program the npm package installs; Meru
finds it in the install folder and runs it with its own Node (see [Where things
live on disk](#where-things-live-on-disk)). `{field.vault_path}` stands for the
value you type into the Vault folder field. Every
client will draw its form from the `[[field]]` tables, so the desktop app and
`meru chat` ask the same questions. `[mcp]` works as in a hand-added server: the
model sees only the three tools `allow` names.

**Your config** belongs to you. It holds whether the connector is on and the
value of each field, in one table per connector in `config.toml`:

```toml
[connectors.obsidian]
enabled    = true
vault_path = "~/Notes/vault"
```

A secret never goes in that table. Google's OAuth client secret, for example,
lives in `~/.meru/secrets.toml` under the name `connector_google_client_secret`,
the pattern `connector_<id>_<field>`, and `merud` refers to it as
`secret:connector_google_client_secret`. `config` reads each table as a plain
map, because every connector has its own fields:

```go
// internal/config/config.go
type Connector map[string]any

func (c Connector) Enabled() (enabled, ok bool) {
	enabled, ok = c["enabled"].(bool)
	return enabled, ok
}
```

`config.Load` checks only the table's shape: lower-case keys, and `enabled` set
to true or false. It can't check the fields against the manifest, because the
desktop app and `meru` import `config` and must never reach the connector code.
`merud` will run that check (planned).

**Adding your own MCP server stays the same.** You add it to `[[mcp.servers]]`
in `config.toml`, or with `meru mcp add`, and the supervisor leaves it alone.
You can't edit a manifest on your machine: it sits inside the `merud` binary, so
it changes only when Meru's developers ship a release. A new manifest is how a
Meru release adds a managed connector.

The split has two effects. Nobody can point a working install at an untested
version by editing a file on disk, because the pins live in the binary. And a
Meru upgrade can move a pin without touching your settings, because your config
holds only your values.

```text
+-------------------------------------+   +--------------------------------+
| Manifest                            |   | Your config                    |
| by Meru's developers, inside merud  |   | ~/.meru/config.toml            |
|                                     |   | [connectors.obsidian]          |
| package  obsidian-mcp 2.0.1         |   |   enabled, vault_path          |
| start    obsidian-mcp, with Meru's  |   +--------------------------------+
|            own Node                 |   | ~/.meru/secrets.toml           |
| fields   vault_path, vault_name     |   |   connector_<id>_<field>       |
| health   obsidian_list_vaults       |   +--------------------------------+
| tools    allow 3, confirm 0         |                   |
+-------------------------------------+                   |
                   |                                      |
                   v                                      v
        +--------------------------------------------------------+
        | Supervisor in merud                          (planned) |
        +--------------------------------------------------------+
                                   |
                                   v
~/.meru/runtime/          the only place installs land
  node-24.21.0/           pinned Node.js
  uv-0.12.21/             pinned uv
  pkg/obsidian-2.0.1/     the installed server
  state/obsidian.json     last tool list (planned)
  logs/obsidian.log       its error output (planned)
```

*Figure 1. Each file has one owner. A Meru release changes the manifest; you
change the config and secrets; only `merud` writes the runtime folder.*

## Where things live on disk

*Built in step 2. `merud` doesn't call this code until the supervisor lands in
step 3.*

Every install lands under one folder, `~/.meru/runtime/`:

```text
~/.meru/runtime/
  node-24.21.0/           pinned Node.js, for npm packages
  uv-0.12.21/             pinned uv, a Python package installer
  python/                 the Python 3.12 uv downloads
  cache/npm/, cache/uv/   download caches
  home/                   HOME for npm and uv
  npmrc                   an empty npm settings file
  pkg/obsidian-2.0.1/     obsidian-mcp and its npm packages
  pkg/google-1.30.0/      workspace-mcp and its Python environment
  state/obsidian.json     the tool list from the last health check (planned)
  logs/obsidian.log       the server's error output (planned)
```

`merud` downloads Node.js 24.21.0, the newest Long Term Support release, and
uv 0.12.21, a Python package installer, the first time a connector needs one.
Before it unpacks a download, it compares the file's SHA-256, a 64-character
fingerprint of its contents, with the pin compiled into `merud`, and refuses a
file that doesn't match. A changed or corrupt download never runs. Unpacking
refuses any entry that would land outside the runtime's folder.

Each connector installs into its own folder, named for its ID and version. The
install type in the manifest picks the command, and each program runs by its
full path:

| Type | What `merud` runs |
| --- | --- |
| `npm` | the pinned `node` running npm's own script: `npm-cli.js install --prefix <dir> <package>@<version> --no-audit --no-fund --no-update-notifier --ignore-scripts` |
| `pip` | the pinned `uv venv <dir>/.venv --python 3.12`, then `uv pip install --python <dir>/.venv/bin/python <package>==<version>` |
| `binary` | download the file for this platform and check its SHA-256 |
| `container` | `docker info`, then `docker pull <image>@sha256:<digest>` |

A folder is finished only when it holds a marker file, `.meru-installed`, which
`merud` writes last. A folder without one is a leftover from an install that
stopped part way, and `merud` deletes it rather than trust it. When a new Meru
release moves a pin, the version in the manifest no longer matches the marker,
so `merud` installs the new version beside the old one, and removes the old
folder once the new one is complete.

Two rules keep your own tools out of it:

- **Meru runs Node itself.** A connector such as `obsidian-mcp` ships a script
  whose first line, `#!/usr/bin/env node`, asks `PATH` for Node, and launchd's
  short `PATH` has none: the same failure as the `npx` entry above. So `merud`
  reads the package's `package.json`, finds the script, and runs
  `~/.meru/runtime/node-24.21.0/bin/node <script>`. It works whatever the `PATH`
  holds, and the child's `PATH` still starts with the pinned Node's folder, so
  any `node` the server starts is the same one.
- **uv's folders stay under `~/.meru/runtime`.** Left alone, uv keeps its cache
  and the Pythons it downloads in your home folder. `merud` sets
  `UV_CACHE_DIR`, `UV_PYTHON_INSTALL_DIR`, `UV_PYTHON_BIN_DIR`,
  `UV_PYTHON_CACHE_DIR`, `UV_TOOL_DIR` and `UV_TOOL_BIN_DIR` inside the runtime
  folder, tells uv to use only the Python it downloaded there, and skips your
  `uv.toml`. npm's cache and settings file sit there too.

Every program runs from one Go file, `internal/connectors/run.go`, by absolute
path, with no shell and a short environment; a policy test fails the build if
another file in the package runs one. Meru never runs your global npm, pip or
Homebrew, so a connector can't break another program's packages, and removing
`~/.meru/runtime/` removes every install.

## The supervisor and its states

*Planned: none of this section is built yet.*

The supervisor is the part of `merud` that runs each connector. It keeps one
small state machine per connector: a record of which state the connector is in,
and which events move it to the next one. It resembles launchd, which also
starts and restarts programs. The likeness ends at two points: the supervisor
installs the program first, and it gives up after five crashes in ten minutes
and tells you why, where launchd would keep restarting.

| State | What it means |
| --- | --- |
| `off` | you haven't turned it on |
| `needs_config` | a required field is empty or fails its pattern |
| `installing` | downloading the runtime or the package, then the first health check |
| `ready` | installed and stopped; its tool list is known from the last health check |
| `starting` | the process is starting, and must pass its health check |
| `ok` | running and healthy |
| `crashed` | the process exited or failed its check; waiting out a backoff, a pause that grows after each crash, before the next start |
| `failed` | stopped trying; the reason says why |

```text
  off
   |  turn on
   v
  needs_config <---------------------------------------+
   |  fields valid                                     |  Fix, or a
   v                                                   |  field change
  installing ------------ install fails --------> failed(reason)
   |  installed, health ok                             ^
   v                                                   |
  ready <-------------------------------+              |
   |  first tool call                   |  idle 10 min |
   v                                    |              |
  starting ------- health ok -------> ok               |
   |      ^                             |              |
   |      |  backoff 1, 2, 4 ... 60 s   |  process     |  5 crashes
   |      |                             |  exits       |  in 10 min
   |      +------- crashed <------------+              |
   |                  ^  |                             |
   +-- start or ------+  +-----------------------------+
       check fails

  You see:  ok              = ok, ready
            needs_config    = off, needs_config
            starting        = installing, starting, crashed
            failed(reason)  = failed
```

*Figure 2. Eight states inside, four outside: the outline colour shows which of
the four you see. A crash loops through `crashed` and `starting` with a growing
wait, and only a Fix or a changed field leaves `failed`.*

You see four of those states, so a status line never has to explain the machine:

| You see | States inside it |
| --- | --- |
| `ok` | `ok` (running) and `ready` (stopped, tools known) |
| `needs_config` | `off` and `needs_config` |
| `starting` | `installing`, `starting` and `crashed` during its backoff |
| `failed(reason)` | `failed` |

Five rules govern the machine.

- **Lazy start.** A `ready` connector's tools reach the model from the list
  saved at its last health check, in `state/<id>.json`, with no process running.
  The first call starts the process and waits for it, up to the connect timeout;
  today's pool allows 30 seconds for a connect. So Obsidian and Google use no
  memory until you ask for them.
- **Idle stop.** A process that gets no call for its `idle_timeout`, 10 minutes
  for Obsidian and Google, stops and goes back to `ready`.
- **Crashes and backoff.** After a crash the supervisor waits, then starts the
  process again. The wait doubles each time, 1, 2, 4 seconds and on, up to 60.
  Five crashes in ten minutes set `failed("keeps stopping: <last line of its
  error output>")`, so a program that dies on start can't spin.
- **No replay.** A call that was running when the process crashed fails with its
  error, and `merud` never sends it again, as for a hand-added server today. A
  call such as "send this mail" may have done its work before the crash, and
  sending it twice could send two mails.
- **Tools follow health.** Only a connector you see as `ok` offers tools. While
  SearXNG is down, the model doesn't get `web_search` at all, where today it
  gets a tool that fails.

Health checks run after an install, at each start and on Fix. A container also
gets one every 60 seconds while in use. The supervisor also outlives a reload of
the MCP pool, which today stops every child process each time you change a
tool's policy: the pool asks the supervisor for a connector's session, and a
hand-added server keeps today's code.

## Turning on Obsidian, end to end

*Planned: none of this section is built yet.*

This walk-through follows one connector from off to a first answer, with the
values from its manifest. The rest of the design follows the same path with a
different install type.

1. **You fill in the fields.** In Settings, under Connections, you turn Obsidian
   on and pick your vault folder, `~/Notes/vault`. You leave Vault name empty,
   so Meru makes one from the folder's name: `vault`. The client sends the
   socket op `connector_set`, and `merud` writes `[connectors.obsidian]` with
   `enabled = true` and the folder. The fields pass, so the state moves from
   `off` through `needs_config` to `installing`, and the card reads `starting`.
2. **Node.js arrives.** obsidian-mcp 2.0.1 needs Node.js 22 or later. `merud`
   downloads its pinned Node.js 24.21.0 into `~/.meru/runtime/node-24.21.0/`,
   checks the SHA-256 and unpacks it.
3. **The package installs.** With that Node.js, `merud` runs `npm install
   --prefix ~/.meru/runtime/pkg/obsidian-2.0.1 obsidian-mcp@2.0.1 --no-audit
   --no-fund --no-update-notifier --ignore-scripts`. The server's script lands
   at `pkg/obsidian-2.0.1/node_modules/obsidian-mcp/dist/main.js`.
4. **The first health check.** `merud` starts the pinned `node` by its full path
   with that script and `serve --vault vault=/Users/you/Notes/vault`, and calls the tool
   `obsidian_list_vaults` with no arguments. The answer lists one vault, which
   meets `expect = "nonempty"`. `merud` saves the server's tool list to
   `state/obsidian.json`, stops the process, and moves to `ready`. The card now
   reads **"Obsidian is ready."**
5. **The first question.** You ask "what did I write about the garden plan?".
   The model sees `obsidian.obsidian_search_vault` from the saved list and calls
   it. Figure 3 traces that call.
6. **Ten quiet minutes.** No further call comes, so the process stops and the
   state goes back to `ready`. The card still reads "Obsidian is ready.", and
   the next call starts the process again.

```text
 model             dispatch           pool + supervisor     obsidian-mcp
   |                   |                  (planned)               .
   | obsidian_search_vault                    |                   .
   | (offered from state/obsidian.json)       |                   .
   |------------------>|                      |                   .
   |                   | allow list;          |                   .
   |                   | tool_calls row       |                   .
   |                   | session for obsidian?|                   .
   |                   |--------------------->|                   .
   |                   |                      | ready -> starting  .
   |                   |                      | spawn by full     .
   |                   |                      | path, no shell    .
   |                   |                      |------------------>|
   |                   |                      | obsidian_list_vaults
   |                   |                      |------------------>|
   |                   |                      |<- nonempty -> ok -|
   |                   |                      | obsidian_search_vault
   |                   |                      |------------------>|
   |                   |<- - - - - - - - - - - - matching notes - |
   |<- result, logged -|                      |                   |
   |                   |                      | 10 min, no call:  |
   |                   |                      | stop -> ready - ->x
```

*Figure 3. The model gets the tool before any process runs. The first call pays
for the start and one health check, and every later call within ten minutes goes
straight through. `dispatch`, the one path every tool call takes, logs it the
same way it logs any other call.*

If the process crashes while it works on that search, the search fails and the
model sees the error. The supervisor waits 1 second and starts the process
again, so your next question finds it running.

## The four connectors

Four manifests ship in step 1. The table shows what each one pins and how
`merud` checks it. Each pin carries a comment in its manifest that says where it
came from and on what date.

| ID | Kind | Pin | Health check | You give |
| --- | --- | --- | --- | --- |
| `searxng` | container | image `searxng/searxng` by digest | `GET /healthz`, expects `OK` | nothing |
| `obsidian` | stdio | npm `obsidian-mcp` 2.0.1 | `obsidian_list_vaults`, nonempty | vault folder, vault name |
| `google` | http | PyPI `workspace-mcp` 1.30.0 | `list_calendars`, nonempty | address, client ID, client secret, sign-in |
| `ollama` | dependency | none; Meru installs nothing | `GET /api/version`, has `version` | nothing |

### SearXNG, for web search

SearXNG is the only connector on by default. It runs as a Docker container named
`meru-searxng`, from an image pinned by digest, the hash of the image's
contents:

```toml
[install]
type = "container"
image = "docker.io/searxng/searxng:2026.9.29-4e2c1ea7f@sha256:3284e8900e9b3e5df284ae8c48a26851ae2eff6f99b4b0b18ec3da5a4d9095c3"
```

A tag such as `2026.9.29-4e2c1ea7f` can move to a new image; the digest can't,
so the pin holds even if someone retags. Docker maps `127.0.0.1:8888` to the
container's port 8080 and mounts `~/.meru/searxng` as its settings folder. The
health check asks `/healthz`, which answers `OK` without running a search, so a
check sends nothing off the machine.

Some people already run SearXNG, with Docker Compose for example. If something
that isn't `meru-searxng` already answers on port 8888, Meru calls it
**external**: it reports on it and never starts, stops or changes it (planned).

### Obsidian, for notes

The Obsidian connector uses the npm package `obsidian-mcp`, which reads the
vault folder on disk, so the Obsidian app needn't be open. Meru's catalog, its
list of starter servers, holds another Obsidian server, `mcp-obsidian`, which
talks to a plugin inside the running app. It stays in the catalog for anyone who
adds it by hand.

### Google, for mail, calendar and files

Google is an `http` connector: Meru starts `workspace-mcp` and talks MCP to it
at `http://127.0.0.1:8000/mcp`. You sign in with OAuth, the standard that lets a
program act for your account without your password. The server gives you a link,
a client opens it, and you sign in once (planned). Google sends the browser back
to a callback address on port 8000, which is why the manifest fixes that port.

The four fields are your Google address, the ID of an OAuth client you create
(it must end in `.apps.googleusercontent.com`), that client's secret, and the
sign-in. The secret reaches the server as an environment variable and never as a
command-line argument, because any user on the machine can read another's
arguments in the process list; the manifest check refuses a secret in `args`.
The server runs with your real home folder and saves its sign-in tokens there,
so the tokens from a server you ran by hand keep working once Meru runs it. Mail
attachments go to `~/meru-output/attachments`, where Meru's `read_file` tool can
read them. The model may call nine tools, and two of them, `send_gmail_message`
and `manage_event`, ask you first.

### Ollama, reported and never run

Ollama is a `dependency`: Meru never installs or starts it, and only checks `GET
/api/version` at `[ollama] base_url`. Today a missing Ollama stops `merud`
before it opens its socket. The plan reverses that order (planned): `merud`
opens its socket first, checks Ollama every 30 seconds until it answers, then
warms the models. A question asked meanwhile gets "Ollama isn't running" as its
error, and every status view shows the same sentence.

## Status and Fix

*Planned: none of this section is built yet.*

Today every status view knows two states, connected and not connected, and none
offers a way to repair anything. The plan gives each connector one plain
sentence and one Fix action, the same in every client.

A new socket op, `connectors`, returns one status per connector: its ID, name,
state and sentence, whether Meru needs it, the field list with the values of the
non-secret fields and whether each secret has a saved value, and the Fix, which
names the fields to ask again. Two more ops make changes. `connector_set` saves
values and on or off, then installs, starts and checks the connector while it
streams progress to the client. `connector_fix` runs the connector's config step
again.

| You see | Example sentence | Fix |
| --- | --- | --- |
| `ok` | Obsidian is ready. | none needed |
| `needs_config` | Obsidian needs your vault folder. | asks for the vault folder |
| `needs_config` | Google needs you to sign in. | opens the sign-in link |
| `failed(reason)` | Web search can't start: Docker isn't running. | runs the check again |
| `failed(reason)` | Obsidian keeps stopping: `<last line of its error output>` | asks for the fields again |
| (Ollama) | Ollama isn't running. | runs the check again |

The Fix column follows one rule: Fix asks again for the fields it names, or runs
the check again when it names none. Four places show these rows:

- **Settings, under Connections**, in the desktop app: one card per connector
  with a Fix button, a folder picker for folder fields, and a "Sign in to
  Google" button.
- **The rail**, the desktop app's side column: each connector with a coloured
  dot.
- **`/mcp` in `meru chat`**: the sentence, and `f` walks the fields in the
  terminal.
- **`meru mcp status`**: the sentence, for a script or a quick look.

Because every client draws its form from the same field list, a new connector in
a later release needs no new screen in any client.

## Pinned versions

A pin is one exact version, such as `obsidian-mcp` 2.0.1. "Latest" can mean one
release today and another tomorrow, so an unpinned install can break a working
Meru with no change on your side. With a pin, every Meru user runs the version
Meru's developers tested for that release. This part exists today.

`connectors.Load` parses the four manifests and refuses the set if any one
breaks a rule. It refuses `latest`, an empty version, a range such as `^2.0.0`
or `>=1.30`, a bare major such as `2`, a container image without `@sha256:` and
its digest, and a download without an https URL and a SHA-256 for each platform.
A policy test calls it:

```go
// internal/policy/pins_test.go
func TestManifestsPinned(t *testing.T) {
	if _, err := connectors.Load(); err != nil {
		t.Fatalf("connector manifests: %v", err)
	}
}
```

A second test, `TestNoUnpinnedVersions`, reads more than the manifests. It scans
the installer, the catalog, `cmd/meru`, `scripts/`, `deploy/` and every code
block in the Markdown under `docs/`. It fails on `@latest`, `:latest`, a version
range, `npx` or `uvx` with no exact version, an image with no digest, and a file
fetched from a branch such as `main`. Meru's own `releases/latest` link passes,
because it fetches Meru and not a dependency.

Thirteen places break the rule today and keep working, each listed in the test
with a reason. Ten of them, such as the installer's `searxng:latest` and the
catalog's `uvx workspace-mcp`, go away in steps 4 and 5. The other three stay:
an Ollama model tag, a made-up server name in an example, and the local Grafana
stack. The list can only shrink, since an entry that no longer matches anything
also fails the test.

A pin moves only in a Meru release. Its developers pick the new version, test
it, and list it in the release notes under "Connector versions". Your installed
Meru never picks up a new connector version by itself.

## Moving a working setup over

*Planned: none of this section is built yet.*

A setup that works today keeps working. At start, an `[[mcp.servers]]` entry
named `google` or `obsidian` with no matching `[connectors.*]` table runs as
before. Its status reads "set up by hand" and offers **Adopt**. When you
confirm, Adopt:

1. writes `[connectors.<id>]` from the old entry: the vault folder from
   `--vault`, or the Google address and client ID, with the client secret moved
   into `secrets.toml`;
2. keeps your allow and confirm lists;
3. comments out the old block and stops the old launchd job;
4. starts the connector on the same port, so Google's sign-in callback and saved
   tokens still work.

The IDs stay `google` and `obsidian`, so tool names such as
`google.search_gmail_messages` read the same in the `tool_calls` log, the
transcripts and the router, the part of Meru that picks how to handle each
question. To undo Adopt, restore the commented block.

## What it doesn't do

- **Servers you add by hand stay unsupervised.** An `[[mcp.servers]]` entry
  keeps today's rules: one connect try per tools turn, no restart, no health
  check. To have Meru run a server for you, it needs a manifest, and only a Meru
  release can add one.
- **Web search still needs Docker.** Meru pulls and runs the SearXNG image, but
  you install Docker and keep it running. Without it the status reads "Web
  search can't start: Docker isn't running."
- **The first run needs the internet and takes time.** Node.js, uv, each package
  and the SearXNG image download once. Node.js for Apple silicon is 52,909,993
  bytes (nodejs.org), uv 17,001,427 bytes (its GitHub release), and the SearXNG
  image 96 MB compressed (Docker Hub), all read on 30 September 2026. Offline,
  the install fails and the connector reads `failed`.
- **Only the package itself is pinned.** npm picks the package's own
  dependencies within the ranges the package names, so they can move between
  two installs of the same `obsidian-mcp` 2.0.1. The same holds for pip.
- **Windows has no runtime pin yet.** Node.js ships a `.zip` there and uv an
  `.exe`, and the runtime folder, the `{pkg}` paths and `.venv/bin` follow
  macOS and Linux.
- **Two health checks are still open.** The plan's first draft checked SearXNG
  with an empty JSON search; the manifest uses `/healthz`. Google's check calls
  `list_calendars`, which nobody has yet confirmed against `workspace-mcp`
  1.30.0, and which isn't in the model's allow list.
- **An older `merud` refuses the new config.** `config.Load` rejects any key it
  doesn't know, so a `config.toml` with a `[connectors.*]` table fails to load
  in a Meru from before this change. The release notes have to say so.
- **No sandbox.** A connector runs as an ordinary process with your user's
  permissions, like any MCP server. Meru decides which tools the model may call,
  and the program decides what each call does.

## Where the work stands

The plan lands in seven pull requests, each of which keeps `main` working.

| Step | What it adds | State |
| --- | --- | --- |
| 1 | ARCHITECTURE.md rule change, the manifest types, the four manifests, their checks, `[connectors.<id>]` parsing and the pin test | **built**, merged in PR #90 |
| 2 | Node.js and uv download and check; npm, pip, binary and container installs into `~/.meru/runtime`; Node run by Meru; uv's folders under `~/.meru/runtime` | **built**, in review |
| 3 | the supervisor for Obsidian: lazy start, idle stop, backoff, health checks, the pool's hook, the `connectors` op and the new states in every status view | (planned) |
| 4 | SearXNG as a container with the external rule; `web_search` only while healthy; `merud` stays up without Ollama | (planned) |
| 5 | Google over HTTP with sign-in, and Adopt for existing entries | (planned) |
| 6 | the config flow and Fix in the clients; the installer and `meru setup` hand connectors to `merud` | (planned) |
| 7 | docs, the install skill and the release notes | (planned) |

## Sources

Repository files, as of branch `connectors-step2` (30 September 2026):

- [ARCHITECTURE.md, "Connectors and the
  supervisor"](https://github.com/aarora79/meru/blob/connectors-step2/ARCHITECTURE.md#connectors-and-the-supervisor),
  the design contract; [level
  300](../../ARCHITECTURE.md#connectors-and-the-supervisor) is its web page
- [internal/connectors/](https://github.com/aarora79/meru/tree/connectors-step2/internal/connectors):
  `manifest.go` and `manifests/` (`searxng.toml`, `obsidian.toml`,
  `google.toml`, `ollama.toml`); `runtimes.go`, `download.go`, `install.go`,
  `launch.go` and `run.go` for the installs
- [internal/config/config.go](https://github.com/aarora79/meru/blob/connectors-step2/internal/config/config.go)
  and `load.go`: the `[connectors.<id>]` table
- [internal/policy/pins_test.go](https://github.com/aarora79/meru/blob/connectors-step2/internal/policy/pins_test.go):
  the pin rules and the list of today's offenders; `connectors_test.go`: the
  one exec site
- [docs/coding-notes/connectors.md](https://github.com/aarora79/meru/blob/connectors-step2/docs/coding-notes/connectors.md):
  the code, walked through for readers new to Go
- Today's wiring: `internal/mcp/pool.go` (connect once, 30 s each, no restart),
  `cmd/merud/connections.go` (web search always "connected"),
  `internal/installer/websearch.go` and `google.go`, `cmd/merud/main.go` and
  `runtime.go` (the Ollama check before the socket)

The plan and its review:

- [Issue #87](https://github.com/aarora79/meru/issues/87), the connector
  supervisor plan and its seven steps
- [PR #90](https://github.com/aarora79/meru/pull/90), step 1

Upstream pages for the pins, checked 30 September 2026:

- [Node.js 24.21.0 SHASUMS256.txt](https://nodejs.org/dist/v24.21.0/SHASUMS256.txt):
  the SHA-256 of each archive; 24.21.0 "Krypton" is the newest Long Term
  Support release, from 7 September 2026
- [uv 0.12.21](https://github.com/astral-sh/uv/releases/tag/0.12.21): the
  `.sha256` file beside each archive, published 29 September 2026

- [npm, obsidian-mcp](https://www.npmjs.com/package/obsidian-mcp): 2.0.1 is the
  newest release, published 14 August 2026
- [PyPI, workspace-mcp 1.30.0](https://pypi.org/project/workspace-mcp/1.30.0/):
  uploaded 28 September 2026
- [Docker Hub, searxng/searxng](https://hub.docker.com/r/searxng/searxng/tags):
  tag `2026.9.29-4e2c1ea7f` has index digest `sha256:3284e890…95c3`, pushed 29
  September 2026; the arm64 image is 95,981,206 bytes compressed

---

HTML version: [connectors.html](connectors.html) · [Level 100](100.md) · [Level 200](200.md) · [Level 300](../../ARCHITECTURE.md) · [Roadmap](../../ROADMAP.md)
