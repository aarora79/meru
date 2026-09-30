---
name: pr-review
description: Use when asked to review a Meru pull request. Reviews it as several expert personas (Merge Specialist, Go Developer, Client Developer, Security, Release Engineer, SRE, AI/Agent, Go Mentor, Chief Architect). Takes a PR URL or number, runs make check, walks the diff against Meru's non-negotiables, ARCHITECTURE.md and the security-patterns catalog, blocks on config keys or socket ops that miss a surface, and writes one review, following the writing skill, to .scratchpad/pr-{n}/review.md.
license: Apache-2.0
metadata:
  author: aarora79
  version: "2.0"
---

# PR Review Skill

Review a Meru pull request through several personas. Each persona reads the diff with
its own checklist, and the Chief Architect folds the results into one verdict.

Adapted from the `pr-review` skill in
[agentic-community/mcp-gateway-registry](https://github.com/agentic-community/mcp-gateway-registry),
rewritten for Meru's Go code, its three clients and its non-negotiables.

Read [AGENTS.md](../../../AGENTS.md) and [ARCHITECTURE.md](../../../ARCHITECTURE.md)
before you start, and check the diff against both.

The review follows the `writing` skill. Load it before drafting and run its revision
pass on `review.md` before you report.

## Input

A GitHub PR URL (`https://github.com/{owner}/{repo}/pull/{number}`) or a bare PR number.

## Output

`.scratchpad/pr-{number}/review.md`. Git ignores `.scratchpad/`; never commit it.

## Workflow

### Step 1: Fetch the PR

```bash
gh pr view {number} --json number,title,author,body,url,files,commits
gh pr diff {number}
gh pr diff {number} --name-only
```

Read the PR description. It must give a reason for each new dependency and say which
issue it closes.

### Step 2: Pick personas

Merge Specialist, Go Mentor and Chief Architect review every PR. Add the others by path:

| Changed files | Add |
| --- | --- |
| `cmd/meru/**`, `internal/tui/**` | Go Developer, Client Developer, Security (the CLI stays thin) |
| `cmd/meru-desktop/**`, `internal/desktop/**` (Go and `web/`) | Client Developer, Go Developer, Security |
| `cmd/meru-installer/**`, `internal/installer/**` | Release Engineer, Client Developer, Security |
| `cmd/merud/**` | Go Developer, Security, SRE |
| `internal/rpc/**` | Go Developer, Client Developer, Security, SRE |
| `internal/engine/**`, `internal/router/**` | AI/Agent, Go Developer, Security (loopback only), SRE |
| `internal/agent/**`, `internal/skills/**`, `internal/memory/**`, `internal/summarize/**` | AI/Agent, Go Developer, Security, SRE |
| `internal/dispatch/**`, `internal/mcp/**`, `internal/a2a/**`, `internal/commands/**`, `internal/builtin/**` | AI/Agent, Security, Go Developer, SRE |
| `internal/catalog/**`, `internal/secrets/**`, `internal/config/**` | Security, Go Developer, Release Engineer |
| `internal/store/**`, `internal/retrieve/**`, `internal/index/**`, `internal/transcript/**` | Go Developer, Security, SRE |
| `internal/obs/**`, `deploy/observability/**` | SRE, Security (loopback, content capture) |
| `internal/opener/**`, `internal/loopback/**` | Security, Go Developer |
| `internal/policy/**` | Security, Chief Architect (a weaker policy test is a weaker non-negotiable) |
| `Makefile`, `.github/**`, `scripts/**`, `deploy/launchd/**`, `deploy/systemd/**` | Release Engineer, Security |
| `go.mod`, `go.sum` | Security, Go Developer, Release Engineer |
| `internal/skills/builtin/**` | AI/Agent (and see the copy rule in Step 3) |
| `test/e2e/**`, `cmd/fakeollama/**`, `cmd/fakemcp/**`, `*_test.go` | Go Developer |
| `ARCHITECTURE.md`, `docs/**`, `*.md` | Go Mentor and Chief Architect only, unless the doc changes a contract |

Persona files:

- [Merge Specialist](personas/merge-specialist.md) (always)
- [Go Developer](personas/go-developer.md)
- [Client Developer](personas/client-developer.md)
- [Security Engineer](personas/security-engineer.md), with the
  [security patterns](personas/security-patterns.md)
- [Release Engineer](personas/release-engineer.md)
- [SRE Engineer](personas/sre-engineer.md)
- [AI/Agent Developer](personas/ai-agent-developer.md)
- [Go Mentor](personas/go-mentor.md) (always)
- [Chief Architect](personas/chief-architect.md) (always, writes the synthesis)

### Step 2.5: New or changed config keys (blocking)

A `config.toml` key lives in several places, and only review keeps some of them in
step. Run this before the persona reviews:

```bash
gh pr diff {number} --name-only | grep -E \
  -e '^internal/config/[^/]+\.go$' \
  -e '^internal/config/template\.toml$' \
  -e '^config\.example\.toml$' \
  -e '^internal/catalog/' \
  -e '^internal/desktop/(settings|options)\.go$' \
  -e '^internal/desktop/web/js/(settings|setup)\.js$'
```

If anything matches, list each key the diff adds, renames or removes, and check:

- [ ] `internal/config` has the field, its default and its validation. A key that
      takes an address calls `loopback.CheckURL`. A key that takes a secret accepts
      `secret:<name>`, and the secret itself lives in `~/.meru/secrets.toml`.
- [ ] `internal/config/template.toml` documents the key with its default and a comment,
      and `config.example.toml` is a byte-for-byte copy
      (`TestExampleIsTemplate` fails otherwise).
- [ ] An existing `config.toml` without the key still loads and behaves as before.
- [ ] ARCHITECTURE.md documents the key when it changes behavior a user can see.
- [ ] A key the user changes at run time goes through a socket op, and `merud` writes
      the file (through `internal/catalog` for list edits). Both clients offer it: the
      desktop Settings screen and a `meru chat` slash command, or the PR says why only
      one.
- [ ] A how-to page in `docs/faq/` covers it when a user would ask how to set it.
- [ ] No concrete model name in code; model names live in config only.
- [ ] A renamed key has its old name handled or its removal noted in the release notes.
      A removed key leaves no stale lines in the template, ARCHITECTURE.md or the FAQ.

Any unchecked item makes the verdict **REQUEST CHANGES**, with a blocker titled
"Config key not carried to every surface".

### Step 2.6: New or changed socket ops and events (blocking)

`internal/rpc` is the contract between `merud` and its three clients: `meru`, `meru
chat` and `meru-desktop`. Scripts that run `meru run --json` also read its events. A
change that lands in `merud` and one client passes every test and leaves the other
client behind. `gh pr diff` can't filter by path, so fetch the PR and diff it with git:

```bash
git fetch origin pull/{number}/head:pr-{number}
git diff origin/main...pr-{number} -- internal/rpc \
  | grep -nE '^\+\s*(Op|Event)[A-Z][A-Za-z]*\s+(Op|EventType)\s*='
git diff origin/main...pr-{number} -- internal/rpc | grep -nE '^[-+].*`json:"'
```

If either matches, check:

- [ ] `cmd/merud` handles the new op, in the file for its group of ops.
- [ ] Both interactive clients use it: the desktop Bridge (`internal/desktop`) with its
      page in `internal/desktop/web/js/`, and `internal/tui`, with a case in
      `internal/tui/parity_test.go`. Otherwise the PR says why only one needs it.
- [ ] Changes to request or event fields keep older clients working: new fields are
      optional, and no field changes meaning or disappears without a note in the
      release notes.
- [ ] A new event type on the `ask` stream appears in ARCHITECTURE.md's "merud as an
      agent harness" section, since `meru run --json` passes it to scripts.
- [ ] [docs/lld.md](../../../docs/lld.md) and `docs/coding-notes/rpc.md` describe the op.

If an op reaches `merud` and only one client with no reason given, the verdict is
**REQUEST CHANGES** with a blocker titled "Socket op not carried to both clients".

### Step 3: Meru merge-blocking checklist

The Merge Specialist owns this list. Any unchecked item is a **blocker** unless the PR
description argues for the exception and the Chief Architect accepts it.

**Non-negotiables (AGENTS.md)**

- [ ] **No cloud-model code path.** No HTTP client, SDK or config key that could reach
      a cloud model, even behind a flag. Model traffic goes only to Ollama on loopback.
- [ ] **No telemetry leaves the machine.** No update check, crash report or phone-home.
      OTLP export stays off until config sets an endpoint, and `merud` refuses a
      non-loopback one. Prompt and response text stay out of spans unless
      `capture_content = true`.
- [ ] **Deny-by-default for tools and agents.** A new MCP server or A2A agent offers no
      tools until its `allow` list names them. `merud` connects only to loopback, except
      to A2A agents and Streamable HTTP MCP servers marked `remote = true`, and to
      public pages through `web_fetch` under its URL guard.
- [ ] **Every tool call goes through `dispatch`**, which logs it to `tool_calls` and the
      transcript. An incognito chat's calls still get a `tool_calls` row, with no
      arguments and no result. No second path.
- [ ] **Nothing for sale.** No paid licence, pricing, sponsor link or invitation to buy
      in the README, `CONTRIBUTING.md`, the About panel, the landing page, posters or
      issue templates.

**Shape and conventions**

- [ ] `cmd/meru` and the desktop app import only what the dependency rule in AGENTS.md
      allows. `internal/policy` enforces this; a PR that loosens a policy test argues for
      it.
- [ ] The `Engine` interface still has four methods: `Generate`, `Stream`, `Embed`, `Info`.
- [ ] Anything in `meru.db` can be rebuilt from files and config. The one exception is
      the `tool_calls` rows of incognito and deleted chats.
- [ ] Metric attributes come from bounded sets (model, tier, server, tool, outcome). No IDs,
      paths or text on metrics; those go on spans.
- [ ] The PR gives a reason for each new module in `go.mod`, and prefers the standard
      library.
- [ ] Nothing from a later ROADMAP milestone (the v0.5 scheduler, for one) slipped in.

**Docs that travel with the code**

- [ ] `docs/coding-notes/` has a new or updated note for each changed package, and a new
      note is in the index. A new Go concept gets a note in `go-basics/`.
- [ ] A new package has a `doc.go` (or a package comment in `main.go`) and a line in
      the AGENTS.md layout.
- [ ] If ARCHITECTURE.md changed, the same PR carries the change into `300.html`,
      `200.md`, `200.html`, `100.md` and `100.html` in `docs/architecture/`. A changed
      figure starts in the page's SVG, and `make figures` redrew the PNGs in `img/`.
- [ ] A change to `internal/skills/builtin/writing` or `explainer` is a copy from
      `my-ai-assets`, not an edit in place.
- [ ] Prose follows the `writing` skill: code comments, docs, coding notes, commit
      messages and the PR description (see Go Mentor).
- [ ] No AI or assistant attribution in commits, the PR body, code comments or docs
      (`Co-Authored-By: Claude`, "Generated with", and so on). Check with
      `gh pr view {number} --json commits,body`.
- [ ] No cloud vendor named in docs, tests or examples; examples use the catalog
      servers (`google`, `obsidian`) and `web_search`, with no trading or finance.

### Step 4: Run the checks

```bash
gh pr checkout {number}

make check          # gofmt, vet, staticcheck, tidy, tests with -race, e2e,
                    # five-platform build, govulncheck, gosec, gitleaks, actionlint

# Only when the PR touches cmd/meru-desktop, cmd/meru-installer or their packages,
# on a Mac with cgo:
make desktop installer desktop-check

git checkout main
git branch -D pr-{number}   # the branch Step 2.6 fetched, if it ran
```

The Makefile runs each tool through `go run tool@version`, so nothing needs
installing. Report each target that fails and paste its first error. A failing target
is a blocker, and so is a govulncheck finding in reachable code.

When the PR touches the engine, router, agent or retrieval, and Ollama runs on this
machine, also run the opt-in tests and report them as information, not a gate:

```bash
go test -tags integration ./...
go test -tags 'e2e integration' ./test/e2e/...
```

A router change also gets `make router-eval`, and a skill-pick change gets
`make pick-eval`. Compare the score with the one on `main`.

### Step 5: Review

Adopt each selected persona in turn and fill in its section in the format from its
persona file. Cite `file:line` for every finding. Skip sections that don't apply;
don't pad with "N/A".

The Security Engineer walks the
[security-patterns checklist](personas/security-patterns.md#review-checklist) for every
changed file. The Chief Architect walks ARCHITECTURE.md's "Principles", "Privacy
boundary" and "Deliberate non-goals". If the PR drifts from any of them without
arguing for it, raise it as a blocker and tell the user.

### Step 6: Write review.md

```markdown
# PR Review: #{number} - {title}

*Date: {date} · URL: {url} · Author: {author} · Closes: #{issue}*

## Summary

{Two to four sentences: what the PR does and why.}

| File | Area | + | - |
| --- | --- | --- | --- |
| {file} | {cmd/merud, internal/agent, desktop page, docs, ...} | {n} | {n} |

## Checks

| Target | Status | Details |
| --- | --- | --- |
| `make check` | {PASS/FAIL} | {failing targets and first errors} |
| `make desktop installer desktop-check` | {PASS/FAIL/N/A} | |
| Integration tests (opt-in) | {PASS/FAIL/not run} | |
| `make router-eval` / `make pick-eval` | {score vs main / N/A} | |

## Config keys

*Only when Step 2.5 matched. Otherwise write "Not applicable: no config change."*

| Key | Change | `internal/config` | template + example | ARCHITECTURE.md | Settings + slash command | FAQ |
| --- | --- | --- | --- | --- | --- | --- |
| `[section] key` | {added/renamed/removed} | {PASS/FAIL} | {PASS/FAIL} | {PASS/FAIL/N/A} | {PASS/FAIL/N/A} | {PASS/FAIL/N/A} |

## Socket ops and events

*Only when Step 2.6 matched. Otherwise write "Not applicable: no protocol change."*

| Op or event | `merud` | Desktop | `meru chat` | Older clients | Docs |
| --- | --- | --- | --- | --- | --- |
| `{op}` | {PASS/FAIL} | {PASS/FAIL/why not} | {PASS/FAIL/why not} | {PASS/FAIL} | {PASS/FAIL} |

## Meru Checklist

| Item | Status | Details |
| --- | --- | --- |
| No cloud-model code path | {PASS/FAIL} | |
| No telemetry off the machine; OTLP loopback only; no content in spans by default | {PASS/FAIL/N/A} | |
| Deny-by-default tools and agents; loopback-only `merud` except `remote = true` and `web_fetch` | {PASS/FAIL/N/A} | |
| Every tool call through `dispatch` and `tool_calls` | {PASS/FAIL/N/A} | |
| Nothing for sale | {PASS/FAIL} | |
| Client imports within the dependency rule | {PASS/FAIL/N/A} | |
| `Engine` still four methods | {PASS/FAIL/N/A} | |
| `meru.db` rebuildable from files | {PASS/FAIL/N/A} | |
| Metric attributes bounded | {PASS/FAIL/N/A} | |
| New modules justified | {PASS/FAIL/N/A} | {module: reason} |
| In milestone scope | {PASS/FAIL} | |
| Coding notes, package docs, AGENTS.md layout | {PASS/FAIL/N/A} | |
| ARCHITECTURE.md carried to the 300, 200 and 100 pages and figures | {PASS/FAIL/N/A} | |
| Built-in skill copies untouched in place | {PASS/FAIL/N/A} | |
| Prose follows the `writing` skill | {PASS/FAIL} | |
| No AI attribution; no cloud vendor names | {PASS/FAIL} | |

## Review Panel

| Role | Reviewer | Verdict |
| --- | --- | --- |
| Merge Specialist | Gatekeeper | {verdict} |
| {Role} | {Name} | {verdict} |
| Chief Architect | Atlas | {verdict} |

---

{One section per persona, in the format from its persona file.}

---

## Findings

### Blockers
1. {Issue} (raised by {persona}, `{file:line}`). Fix: {fix}

### Should fix
1. {Issue} (raised by {persona}, `{file:line}`). Fix: {fix}

### Consider
1. {Suggestion} (raised by {persona})

## Verdict: {APPROVE / APPROVE WITH CHANGES / REQUEST CHANGES}

### Before merge
- [ ] {Required action}

### After merge
- [ ] {Follow-up, such as a release-notes line or an issue for deferred work}
```

### Step 7: Report

Tell the user:

1. the verdict;
2. each blocker, one line apiece;
3. the path to `review.md`;
4. that you can explain any finding in more depth.

End every reply about the PR with a clickable link to it, the full URL with the title:

```markdown
[#69 Delete, file, tag and hide chats in the chat list](https://github.com/aarora79/meru/pull/69)
```

A bare `#69` shows as plain text in the terminal. The rule holds for every reply about
the PR, follow-ups included, and a reply that covers several PRs links each one.

## Principles

- **Simple wins every time.** A smaller diff with fewer moving parts beats a clever one.
  Flag abstractions, interfaces, packages, flags or options the change doesn't need yet.
- **Build to the milestone.** Code for a later ROADMAP milestone is scope creep.
- **Readers may be new to Go.** Phrase findings so they teach: say what's wrong, why it
  matters in Go, and show the idiomatic fix.
- **Grep the diff; don't trust the description.** Check each claim in the PR body
  against the code.
- **Credit good work** alongside the problems.

### Severity

- **Blocker:** a failing `make` target, a broken non-negotiable, a matched security
  anti-pattern, a data-loss risk, a config key or socket op that misses a surface, or
  a change to ARCHITECTURE.md's contract that the PR doesn't argue for.
- **Should fix:** missing tests, unclear errors, missing comments or coding notes,
  unbounded metric attributes, leaked goroutines.
- **Consider:** naming, style, small simplifications.

### Verdicts

All personas use the same three: **APPROVE**, **APPROVE WITH CHANGES** (no blockers,
small fixes), **REQUEST CHANGES** (any blocker).

## Example

User: "/pr-review 69"

1. Fetch PR #69 and its diff. It touches `cmd/merud/chats.go`, `internal/rpc`,
   `internal/tui/organize.go`, `internal/desktop`, `internal/transcript`,
   `internal/agent/incognito.go`, ARCHITECTURE.md with its pages, and the coding notes.
2. Personas: Merge Specialist, Go Developer, Client Developer, Security, SRE, AI/Agent,
   Go Mentor, Chief Architect.
3. Step 2.6 matches the new session ops, such as `OpSessionDelete` and
   `OpSessionTag`. Check that `merud`, the desktop page and `meru chat` all handle
   them, and that `parity_test.go` covers them.
4. Security walks pattern #9: a deleted chat must strip its `tool_calls` rows.
5. The Merge Specialist checks that the ARCHITECTURE.md change reached `300.html`,
   `200.md`, `200.html`, `100.md` and `100.html`.
6. Run `make check`, write `.scratchpad/pr-69/review.md`, report, and end with the PR
   link.
