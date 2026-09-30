# installer

**Code:** `internal/installer/` (`doc.go`, `run.go`, `steps.go`, `bridge.go`,
`machine.go`, `configfile.go`, `meru.go`, `ollama.go`, `folders.go`,
`websearch.go`, `obsidian.go`, `commands.go`, `google.go`, `profile.go`,
`connectors.go`, `merud.go`,
`assets.go`, the page in `web/`, and `launchd/com.meru.merud.plist`, a copy of
the one in `deploy/`), plus `cmd/meru-installer/main.go` and its `Info.plist`
**Milestone:** the Mac installer, asked for ahead of v0.5
**Architecture:** [Installer](../../ARCHITECTURE.md#installer)

## What it does

The Mac installer is "Install Meru.app" on each release's disk image. It walks a
person through ten steps, from checking the Mac to starting `merud`, and each step
says what it does and what it downloads before it runs.

The installer runs before `merud` exists, so it has to do what the clients never
do: run programs such as `brew` and `launchctl`, and write files such as `merud`'s
launchd job. The connectors are the exception: web search, Obsidian and Google
are `merud`'s to install and run, so their steps only gather values, and the
last step hands them to `merud` over the socket.
To keep that safe and testable, the code splits in two, as the desktop app's does.
`cmd/meru-installer` opens the window with Wails, a Go library that shows an HTML
page in the Mac's own WebView. Everything else sits here, in `internal/installer`,
which doesn't import Wails, so its tests run on any machine with fakes in place of
the real programs and servers.

## The picture

```mermaid
flowchart LR
    page["page<br/>(web/installer.js)"] -- "Run, Skip, Screen" --> bridge[Bridge]
    bridge --> flow["Flow<br/>(steps.go)"]
    bridge --> steps["the ten steps<br/>(meru.go, ollama.go, ...)"]
    steps --> run["Runner<br/>(run.go)"]
    run --> progs["brew, launchctl,<br/>open, ditto, xattr, ..."]
    steps --> http["HTTP on 127.0.0.1<br/>Ollama, SearXNG"]
    steps --> catalog["catalog<br/>config.toml edits"]
    steps --> memory["memory<br/>profile files"]
    steps --> rpc["rpc<br/>ping, index_status,<br/>connector_set, connector_adopt"]
    bridge -- "installer:progress" --> page
```

The page calls the Bridge; the Bridge asks `Flow` whether a step may start, runs
it, and sends each line of news back to the page. Every program goes through the
Runner, every `config.toml` change through `catalog`.

## Walk through the code

### run.go

This is the only file that starts programs. `Programs()` returns the allowlist, a
map from each program's name to the absolute paths it may live at:

```go
"brew": {"/opt/homebrew/bin/brew", "/usr/local/bin/brew"},
```

`docker` left the list in step 6 of issue #87: `merud` runs SearXNG now.

`Locate(program, home)` returns the first path that exists, or `ErrNotAllowed`
for a name the map lacks, or `ErrMissing` when none of the paths exists. The
installer never searches `PATH`, because an app opened from Finder gets a short
`PATH` that doesn't hold Homebrew.

`Runner` is a function type:

```go
type Runner func(ctx context.Context, program string, args []string, line func(string)) (string, error)
```

Any function with that signature is a Runner. `ExecRunner(home)` returns the real
one: it finds the program with `Locate`, runs it with `exec.CommandContext`, which
takes each argument as its own string and uses no shell, and hands each line of
output to `line` as it comes, so the screen shows `brew` working. The
tests pass a fake that records each call and runs nothing. `runLines` joins stdout
and stderr through an `io.Pipe` and reads it in a goroutine while the program
runs.

### steps.go

`Flow` holds the ten steps and their states: pending, running, done, failed or
skipped. `Start` moves a step to running and refuses while another runs, `Finish`
moves it to done or failed with the reason, and `Skip` refuses for About you,
the one step with `Skippable` false. A `sync.Mutex` guards the list, because Wails
runs each call from the page on its own goroutine. Each `Step` carries the text
its screen shows: what it does, why, and what it costs.

### bridge.go

The `Bridge` is what the window binds. `Detect` looks for work an earlier run
did and notes it on each step. `Screen(id)` returns the data a step's screen
needs, such as the Mac's facts or the folder list. `Run(id, input)` asks `Flow`,
calls the step, and records how it ended; a step that fails comes back as a
failed step with its reason, not as an error, so the page can show Retry and
Skip. `Links()` maps each name a screen may open to a fixed `https` address, so
the page can never open an address of its own.

The Bridge also keeps the connector hand-offs, a map from connector ID to the
`HandOff` its step gathered, under its mutex. `runConnector` runs a connector
step and keeps what it returns; `Skip` drops a skipped step's hand-off, so a
step skipped on a second visit hands nothing over. `handOffs` returns them in
step order for Start Meru. `OpenSignIn` opens the Google sign-in link `merud`
gave, which the Bridge holds in `signIn`; the page asks for it by name, as it
asks for a link, and never holds the address.

### machine.go

`CheckMachine` asks `sysctl` for the memory and chip, `sw_vers` for the macOS
version and `df` for the free disk, and fills in the model choices from
`config.Recommendations` (see [config](config.md)).

### configfile.go

`Paths` names every place the installer writes, all under one home folder, so the
tests point it at `t.TempDir()`. `EnsureConfig` writes the config template when
`config.toml` is missing, through a temporary file that must load first.

### meru.go

`InstallMeru` copies `meru`, `merud` and Meru.app from the payload inside the
installer's bundle with `ditto`, each to a new name first and then renamed over
the old one, because writing over a running `merud` would make macOS stop it. It
clears the quarantine mark with `xattr` only when the user ticked the box.
`ensurePath` adds the `PATH` line to `~/.zshrc` once.

### ollama.go

`InstallOllama` tries `brew install ollama`; when the Runner says `brew` is
missing, it downloads Ollama.app from Ollama's site, unpacks it with `ditto`, and
checks its signature with `codesign`. `PullModel` sends `POST /api/pull` and reads
the answer one JSON object per line. `readPull` adds up each layer's `completed`
and `total`, so the bar shows the whole model:

```go
if l.Digest != "" && l.Total > 0 {
    layers[l.Digest] = layer{completed: l.Completed, total: l.Total}
}
```

### folders.go

`SuggestFolders` lists the folders config has, then Documents, Desktop and Notes,
each with a file count that stops at 20,000 files or two seconds. `SaveFolders`
writes `[index] folders` with `catalog.SetTableLists`.

### websearch.go

`SetUpWebSearch` runs no program. When `VerifySearXNG`, one test search, passes
at `127.0.0.1:8888`, a SearXNG already runs there, such as the container an
older installer started, and the step hands nothing over: `merud` watches it as
an external SearXNG. Otherwise it needs Docker, which `HasDocker` looks for at
its usual paths with `os.Stat`, and returns a `HandOff` that turns the SearXNG
connector on. Either way it sets `[web] searxng_url` and makes sure `[builtin]
tools` has both web tools. The Bridge passes `HasDocker`'s answer in, so the
tests don't depend on the Mac they run on.

### obsidian.go

`SetUpObsidian` takes the vault folder from the screen, checks with `os.Stat`
that it is a folder, a path written `~/…` counting from the home folder, and
returns a `HandOff` with `vault_path` as the user wrote it. `connectorState`
reads config: when `[[mcp.servers]]` has an `obsidian` entry, the step keeps it
unless the user picked Adopt, and then the hand-off is an adopt. `ObsidianFound`
says what a second run finds.

### commands.go

`SampleBlocks` finds each commented `# [[commands]]` sample in the config template
and takes the `# ` off its lines. `SampleCommands` marks which ones this Mac can
run: a sample whose program isn't installed, or whose `under` folder is missing,
can't be ticked, because `merud` refuses to start with a bad `under`. A program
outside launchd's short `PATH`, such as `gh` from Homebrew, gets its full path in
`argv`. `SaveSkillsAndCommands` appends the ticked ones with
`catalog.AppendCommand` and takes the built-in skills out of `[skills] disabled`.

### google.go

`GoogleInput.check` refuses an empty field and any space or line break, and its
messages never quote the secret. `SetUpGoogle` returns a `HandOff` whose change
holds the address and client ID as values and the secret apart, in `Secrets`,
so `merud` puts it in `secrets.toml` and never in `config.toml`. It writes no
start script, launchd job or config entry. For a `google` entry set up by hand,
Adopt makes the hand-off an adopt, with any values the user typed for an entry
whose start script `merud` can't read.

### connectors.go

`HandConnectors` sends each hand-off to `merud` in order. A change goes out as
`connector_set`, and `handOne` shows each `connector` event's sentence as it
comes. An adopt goes out as `connector_adopt` with `Apply`, and then
`watchConnector` asks the `connectors` op every second until the connector is
no longer starting, since `merud` installs an adopted connector on its own. A
refusal or a failure becomes that connector's line, and the next one still
goes out. Each result keeps the sign-in link `merud` gave, for `OpenSignIn`;
the lines on screen never hold it.

### profile.go

`SaveProfile` writes "Name: …", "Email: …" and "Answers: …" as memory files
through `internal/memory`, the files `meru setup user` makes. A field that didn't
change keeps its file. `LoadProfile` reads them back for a second run.
`Profile.Check` requires the name, and the email after the Google step.

### merud.go

`MerudPlist` fills in the plist from `deploy/launchd`, embedded with `//go:embed`
from a copy in `launchd/`, because an embedded file must sit in the package's
folder; `TestMerudPlistIsDeployCopy` keeps the two equal. `StartMerud` loads the
job and waits for `Ping`. `FollowScan` asks `index_status` every two seconds while
`merud`'s own startup scan runs, and stops watching after two minutes; the scan
carries on in `merud`. `loadJob` and `xmlText`, which write and load the plist,
live here now that `merud`'s is the only launchd job the installer writes. The
Bridge's Start Meru step runs `StartMerud`, then `HandConnectors`, then
`FollowScan`.

### assets.go and web/

`Assets(fallback)` serves `web/` and passes any other path to `fallback`, which
the window sets to Meru.app's page. So `app.css`, the fonts and the logo come from
Meru.app, and the installer looks the same without a second copy.
`installer.css` adds the layout and a dark palette for
`prefers-color-scheme: dark`. `installer.js` draws one screen per step and calls
the Bridge by name through Wails' runtime.

## Go ideas used here

- **Function types.** `Runner` is a type whose values are functions, so a test
  passes a fake without an interface. More in
  [go-basics/interfaces.md](go-basics/interfaces.md).
- **os/exec with no shell.** Each argument is its own string. More in
  [go-basics/os-exec.md](go-basics/os-exec.md).
- **Mutex.** `Flow` guards its steps with a `sync.Mutex`. More in
  [go-basics/goroutines.md](go-basics/goroutines.md).
- **embed.** The page and the plist ride inside the binary. More in
  [go-basics/embed.md](go-basics/embed.md).
- **Iterators.** `rpc.Do` returns one, and `Ping` ranges over it. More in
  [go-basics/iterators.md](go-basics/iterators.md).
- **Build tags.** `cmd/meru-installer` builds only with `-tags desktop`. More in
  [go-basics/build-tags.md](go-basics/build-tags.md).

## Try it

```sh
go test ./internal/installer/        # every step, with fakes
go test ./internal/policy/           # the allowlist and import rules
make installer-app                   # bin/Install Meru.app, with its payload
make dmg                             # dist/Meru-dev-macos-arm64.dmg
```

The tests start no real program and reach no real server: a fake Runner stands in
for `brew` and `launchctl`, `httptest` servers stand in for Ollama and SearXNG,
and a small socket server stands in for `merud`, which `connectors_test.go` uses
to check every hand-off, the secret kept off the screen, and a refusal that
doesn't stop the next connector.

`make installer-app` and `make desktop-app` sign each finished app with
`codesign --sign -`, an ad-hoc signature with no certificate, and verify it.
Go's linker signs only the program inside a bundle; without a signature that
also seals `Info.plist` and the resources, macOS calls a downloaded copy
"damaged" and won't open it. To see what a download gets, mark a copy of the
disk image as downloaded and ask Gatekeeper, the macOS download check:

```sh
xattr -w com.apple.quarantine "0083;$(printf %x $(date +%s));Chrome;" copy.dmg
hdiutil attach -readonly copy.dmg
codesign --verify --deep --strict "/Volumes/Install Meru/Install Meru.app"   # valid on disk
spctl -a -vv "/Volumes/Install Meru/Install Meru.app"   # rejected: not notarized, opens with Open Anyway
```

## Why it's built this way

A shell script already installs Meru (`scripts/install.sh`), and it could have
grown a web of prompts. A window suits people who don't use Terminal, and a Go
package lets CI test each step, which a script's `curl | bash` flow doesn't.

The installer could have asked `merud` to do the work, as the desktop app does.
But nothing runs `merud` until the last step, and `merud` needs Ollama and the
models before it starts, so the installer must act on its own for those. The
connectors wait for `merud`, which installs each at the version pinned in its
release, runs it and repairs it later, so the installer doesn't duplicate that
work with its own docker, uv and launchd jobs. The allowlist keeps the
installer's power narrow: nine programs at fixed paths, no shell, and a policy
test that fails the build if a second way to start a program appears.

Each step writes config through `catalog`, the code `meru setup` and `merud`
already use, so the comments in `config.toml` survive and a bad write never lands.
The profile goes through `memory` for the same reason: one format, whoever writes
it.
