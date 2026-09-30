# Security Patterns and Anti-Patterns

This catalog lists the ways Meru can go wrong on security, and the guard the code
already has for each one. New code and reviews use it so the next change keeps each
guard in place.

Meru has one user, the owner, and no sandbox. It runs as an ordinary user process,
so control sits in `dispatch`, the allowlists and the approval prompts. The threats
come from four places: a model that injected text steers, an MCP (Model Context
Protocol) server or A2A (agent-to-agent) peer that misbehaves, another local account
or process, and data that leaves the machine.

**How to use it.**

- **Designing a feature:** read the patterns your change touches. A new outbound
  connection means #1 and #2. A new tool means #3 and #4. A new file path means #5.
  A new secret means #7. Apply the rule, not only the past fix.
- **Reviewing a PR:** walk the [review checklist](#review-checklist) at the bottom.
  The `pr-review` Security Engineer and the `new-feature-design` security review both
  use it.

Each pattern gives **the mistake**, **the rule**, **where the code enforces it** and
**what to check**. When a security fix lands, add its PR number under the pattern, or
add a new pattern, so the next review catches the next instance.

---

## 1. Data leaving the machine

**The mistake.** An HTTP client, exporter or dial that can reach a host off the
machine: a cloud-model SDK (software development kit) in `go.mod`, an update check, a
crash reporter, an OTLP (OpenTelemetry Protocol) endpoint on the network, or a check
that treats the string `localhost` as proof of loopback.

**The rule.** Model traffic goes only to Ollama on loopback. OTLP goes only to a
loopback endpoint, and `merud` refuses to start with any other. An MCP server over
Streamable HTTP or an A2A agent may reach another machine only when its config entry
says `remote = true`. `web_fetch` and `web_search` are the other exceptions, and #2
covers them. Code decides "loopback" in one place, by resolving the address.

**Where the code enforces it.** `loopback.CheckURL` in
[`internal/loopback/loopback.go`](../../../../internal/loopback/loopback.go), called
from `internal/config/load.go` (Ollama, OTLP), `internal/mcp/config.go` and
`internal/a2a/config.go`. The policy tests in
[`internal/policy/privacy_test.go`](../../../../internal/policy/privacy_test.go)
(`TestNoDeniedImports`, `TestGoModRequiresNoDeniedModule`,
`TestNoProviderHostLiterals`, `TestNoNonLoopbackURLLiterals`) read the deny-lists in
`internal/policy/testdata/` and the allowed URLs in `allowed_urls.txt`.

**What to check.** A new `http.Client`, `http.NewRequest`, `net.Dial` or URL literal
outside the packages that own one (`engine`, `obs`, `mcp`, `a2a`, `builtin`'s web
tools). A new line in `allowed_urls.txt` without a reason. A new module whose
dependencies pull in a network client. A loopback test by string match instead of
`loopback.CheckURL`.

---

## 2. Fetching a URL the model chose

**The mistake.** Fetching a URL that came from the model, a tool result or a web
page with a plain client. An injected page can point it at `127.0.0.1`, the router at
`192.168.1.1`, or a cloud metadata address, and read what answers. A check that
resolves the name once and then dials again loses to DNS rebinding.

**The rule.**

- Only `http` and `https`.
- Check the address at connect time, on the dialer, so the address checked is the
  address used. Refuse loopback, private, link-local and the other reserved ranges,
  and unmap IPv4-in-IPv6 first.
- Check every redirect again, cap their number, and refuse a redirect to another
  scheme. The SearXNG client follows no redirect.
- Ask the user before fetching a URL that no search result and no question of theirs
  gave in the same session, and before any download.

**Where the code enforces it.** `publicDialer`, `checkPublic` and `privateRanges` in
[`internal/builtin/web.go`](../../../../internal/builtin/web.go), and the
`CheckRedirect` functions in `newWebClients`. `knownURLs` and `ConfirmCall` in
[`internal/builtin/webguard.go`](../../../../internal/builtin/webguard.go).

**What to check.** A new fetch that skips `webClients`. A redirect policy that
follows without the check. A change to `knownURLs` that trusts URLs the model wrote
itself. A download path that skips the prompt.

---

## 3. A tool call that skips `dispatch`

**The mistake.** Running a tool from a second path: calling an MCP session, a local
command or a built-in straight from the agent loop, a client or a background job. The
call then skips the allowlist, the approval prompt, the `tool_calls` row and the
transcript line.

**The rule.** Every call to an MCP tool, an A2A agent, a `[[commands]]` entry or a
built-in (`configure`, `remember`, `write_file`, the file tools, the web tools) goes
through `dispatch`. A new MCP server or A2A agent offers nothing until its `allow`
list names a tool. A tool in `confirm` asks before each call, and the user may approve
it for the rest of the session; one in `always_confirm` asks every time. Text in the system prompt enforces nothing. The
clients answer approval questions and never run a tool.

**Where the code enforces it.** `Dispatcher` in
[`internal/dispatch/dispatcher.go`](../../../../internal/dispatch/dispatcher.go) and
the `Backend` interface in `internal/dispatch/dispatch.go`. The client import rules in
[`internal/policy/layout_test.go`](../../../../internal/policy/layout_test.go)
(`TestClientImports`, `TestClientDependencies`) keep tool code out of `cmd/meru`,
`cmd/meru-desktop` and `internal/desktop`.

**What to check.** Any call into `mcp`, `a2a`, `commands` or `builtin` that doesn't
come from a `dispatch.Backend`. A new tool that writes, sends or deletes without an
entry in `confirm`. A wildcard in an allowlist. A client that imports a package
`layout_test.go` bans.

---

## 4. Tool output treated as instructions

**The mistake.** Treating text from an MCP tool, an A2A peer, a web page, a retrieved
chunk or a memory as orders: pasting it into the system prompt, letting it name the
next tool without a prompt, or letting a huge result crowd out the context.

**The rule.** All tool output is untrusted data. It goes back to the model as a tool
result, never as system text. `dispatch` strips base64 runs from results. The loop
stops at the iteration cap. When an answer claims an action no tool call backs, Meru
warns the user.

**Where the code enforces it.** `stripBase64` in
[`internal/dispatch/base64.go`](../../../../internal/dispatch/base64.go) (#26). The
`max_rounds` cap in `[agent]` and the loop in `internal/agent`. The unbacked-claim check
(#29, #71) in `internal/agent/honest.go`.

**What to check.** Tool, page or memory text concatenated into the system prompt. A
new result path that skips `stripBase64` or the size cap. A change that raises or
removes the iteration cap.

---

## 5. Paths that escape their folder

**The mistake.** Joining a path the model, a tool or config supplied with
`filepath.Join` and opening it without checking where it lands. `../../.ssh/id_rsa`
reads a key, and a symlink walks out of the tree.

**The rule.** Clean the path, then open it through `os.Root` or confirm it stays
under its base (`filepath.Rel` with no leading `..`). `write_file` writes only under
`~/meru-output/`. The file tools read only the `[index]` folders, `~/meru-output/`
and the past chats in `~/.meru/sessions`, and they apply the indexer's skip rules,
secrets included. The indexer follows no symlink.

**Where the code enforces it.** `cleanRelPath`, `makeParents` and `writeAtomic` over
an `os.Root` in [`internal/builtin/writefile.go`](../../../../internal/builtin/writefile.go).
`resolve` and `absPath` in `internal/builtin/files.go`. `IsSecret` and `skipPath` in
[`internal/index/skip.go`](../../../../internal/index/skip.go). `checkPath` and `inside`
in `internal/commands/render.go`. The memory store writes through `os.Root` in
`internal/memory/memory.go`.

**What to check.** New file access built from any string that didn't come from the
owner's own config. A read tool that bypasses `resolve`. A write outside
`~/meru-output/` or `~/.meru/`.

---

## 6. Running a program

**The mistake.** Building a command line from strings and handing it to `sh -c`,
running an interpreter whose argument is code, passing the whole environment down, or
opening a link with a shell.

**The rule.** `exec.CommandContext(name, args...)` with no shell. A `[[commands]]`
entry declares its program and typed parameters, and `merud` refuses an interpreter in
`argv[0]`. The child gets `PATH`, `HOME`, `LANG` and the entry's `env_allowlist`, no
more. The opener takes only `http`, `https` and `file` links. The Mac installer starts
programs only from `internal/installer/run.go`, from a fixed allowlist of absolute
paths.

**Where the code enforces it.** `check`, `isInterpreter` and `checkParam` in
[`internal/commands/commands.go`](../../../../internal/commands/commands.go); `Render`
in `render.go`; `Run` and `childEnv` in `run.go`. `Check` and `Open` in
[`internal/opener/opener.go`](../../../../internal/opener/opener.go). `Programs` in
[`internal/installer/run.go`](../../../../internal/installer/run.go), held there by
`TestInstallerRunsOnlyThroughRun` and `TestInstallerAllowlist` in
`internal/policy/installer_test.go`.

**What to check.** `sh -c`, `bash -c`, `cmd /c` or `fmt.Sprintf` into an argument
list. A new `exec.Command` outside `commands`, `opener`, `installer/run.go` and the
MCP stdio launcher. `cmd.Env = os.Environ()`.

---

## 7. Secrets in config, logs and transcripts

**The mistake.** An API key written into `config.toml`, a secrets file other
accounts can read, or a key that reaches `slog`, a span, a `tool_calls` row, a
transcript or a memory.

**The rule.** Config names a secret as `secret:<name>`. The value lives in
`~/.meru/secrets.toml`, mode `0600`, and `merud` refuses the file when group or
others can read it. `merud` redacts every known secret from tool text and errors
before they reach the model, a log or a transcript. `remember` refuses text that holds
a secret. Spans carry no prompt or response text unless `capture_content = true`, and
`slog` never logs prompt text or tool results at info.

**Where the code enforces it.** `Resolve`, `Redact`, `Set` and `checkMode` in
[`internal/secrets/`](../../../../internal/secrets/). The redaction calls in
`cmd/merud/tools.go`. The check in `internal/builtin/remember.go`. `CaptureContent` in
`internal/obs`. `AuditArgs` in `internal/commands/set.go`.

**What to check.** A new config key that takes a raw token. A new `slog` call or span
attribute holding arguments, results or prompt text. A new tool result path that
skips `Redact`.

---

## 8. Files and the socket other accounts can read

**The mistake.** Creating `~/.meru/`, `meru.db`, a transcript, a memory, config or
`merud.sock` with the default mode, so another account on the machine can read them
or drive `merud`.

**The rule.** Folders `0700`, files `0600`, and the socket `0600` so only the owner
can connect. Write with a temporary file and a rename when a crash could leave half a
file.

**Where the code enforces it.** The `os.Chmod` in
[`internal/rpc/server.go`](../../../../internal/rpc/server.go). The modes in
`internal/transcript/transcript.go`, `transcript/folders.go`, `memory/memory.go`,
`catalog/append.go` and `secrets/secrets.go`.

**What to check.** `os.MkdirAll`, `os.WriteFile`, `os.OpenFile` or `os.Create` with
`0o755`, `0o644` or no explicit mode under the Meru home. gosec flags most of these
(`make sec`).

---

## 9. Incognito and deleted chats

**The mistake.** Writing an incognito chat's words anywhere, or leaving a deleted
chat's words behind: a transcript line, a `tool_calls` row with arguments, a span
with content, a summary, a memory.

**The rule.** An incognito chat has no transcript. Its tool calls still go through
`dispatch` and still get a `tool_calls` row with the tool, server, kind, time,
duration, outcome and approval, but no arguments and no result. Deleting a chat
strips its rows the same way. Content capture stays off for incognito calls even when
`capture_content = true`.

**Where the code enforces it.** `Call.Incognito` in `internal/dispatch/dispatch.go`
and its checks in `dispatcher.go`. The in-memory sessions and delete in
`internal/transcript`.

**What to check.** A new record of a turn (a table, a file, a summary, a metric
attribute) that doesn't check incognito. A delete path that keeps text.

---

## 10. The desktop page

**The mistake.** Putting model output into the page as HTML without cleaning it, so
an answer that quotes a hostile web page runs script in the app. Loading anything from
the network into the page. Opening a clicked link inside the WebView.

**The rule.** marked turns Markdown into HTML, and DOMPurify keeps only an allowlist
of tags and attributes and only `http`, `https` and `file` links. A strict
Content-Security-Policy stands behind both. The page loads only its own embedded
files and vendored libraries, with no CDN. A clicked link goes to the system opener
through the Bridge. The app never touches Wails' updater.

**Where the code enforces it.** `renderMarkdown` and the `PURIFY` settings in
[`internal/desktop/web/js/markdown.js`](../../../../internal/desktop/web/js/markdown.js).
`ContentSecurityPolicy` in
[`internal/desktop/assets.go`](../../../../internal/desktop/assets.go). `internal/opener`.

**What to check.** `innerHTML` with text that didn't pass through `markdown.js`. A
new tag or attribute in the DOMPurify allowlist. A weaker CSP. A `<script src>` or
`fetch` to any origin. A Wails updater import.

---

## 11. SQL and search-query injection

**The mistake.** Building SQL with `fmt.Sprintf`, or passing the user's words
straight into an FTS5 `MATCH`, where `"`, `*`, `NEAR` and column filters change the
query.

**The rule.** SQL takes `?` parameters. Keyword search quotes each term before the
`MATCH`.

**Where the code enforces it.** `ftsQuery` in
[`internal/store/search.go`](../../../../internal/store/search.go).

**What to check.** `fmt.Sprintf` or `+` building SQL. A new `MATCH` that skips
`ftsQuery`.

---

## 12. Dependencies

**The mistake.** A module with a known vulnerability, a module nobody imports, or one
that drags in a cloud-model client or a telemetry library.

**The rule.** The standard library first. Each new module needs a reason in the PR
description. `govulncheck` stays clean for reachable code, and the policy deny-lists
keep banned modules out even when unused.

**Where the code enforces it.** `make vuln`, `make sec`, `make secrets` and
`make tidy-check`, all in `make check`. `TestGoModRequiresNoDeniedModule` with
`internal/policy/testdata/model_provider_modules.txt` and `telemetry_modules.txt`.
Dependency review on GitHub.

**What to check.** New lines in `go.mod`. What they pull into `go.sum`. Whether a
short piece of standard-library code would do.

---

## Review checklist

Any "no" is a blocker until the PR justifies it.

**Network**

- [ ] Every new outbound connection goes to loopback (checked by `loopback.CheckURL`), to a `remote = true` server or agent, or through the web tools' guard (#1, #2)
- [ ] No cloud-model SDK, provider host, telemetry library or update check (#1)
- [ ] The web tools check model-chosen URLs at connect time and on every redirect; unknown URLs and downloads ask first (#2)

**Tools**

- [ ] Every tool call goes through `dispatch`; clients import no tool code (#3)
- [ ] New tools that write, send or delete are in `confirm` by default; no wildcard allowlists (#3)
- [ ] Tool output returns as a tool result, stripped of base64, and never enters the system prompt (#4)

**Files and programs**

- [ ] Paths from the model or a tool open through `os.Root` or stay under their base; secrets skipped; no symlinks followed (#5)
- [ ] Programs run with no shell, no interpreter in `argv[0]`, and a trimmed environment; the installer runs only from `run.go` (#6)
- [ ] New folders and files under the Meru home use `0700` and `0600`; the socket stays `0600` (#8)

**Secrets and content**

- [ ] Secrets live in `secrets.toml` behind `secret:<name>` and pass through `Redact`; none in logs, spans, rows or transcripts (#7)
- [ ] No prompt or response text in spans unless `capture_content = true`; no content at all for incognito calls (#7, #9)
- [ ] Incognito and deleted chats leave no words behind (#9)

**Desktop page**

- [ ] Model text reaches the page only through `markdown.js`; the DOMPurify allowlist and the CSP hold (#10)

**Queries and dependencies**

- [ ] SQL uses `?` parameters; keyword search goes through `ftsQuery` (#11)
- [ ] Each new module has a reason; `make vuln` and `make sec` pass (#12)
