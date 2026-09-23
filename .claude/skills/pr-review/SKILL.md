---
name: pr-review
description: Use when asked to review a Meru pull request. Reviews it as several expert personas (Merge Specialist, Go Developer, Security, SRE, AI/Agent, Go Mentor, Chief Architect). Takes a PR URL or number, runs the Go checks, walks the diff against Meru's non-negotiables and ARCHITECTURE.md, and writes one review, following the writing skill, to .scratchpad/pr-{n}/review.md.
license: Apache-2.0
metadata:
  author: aarora79
  version: "1.0"
---

# PR Review Skill

Review a Meru pull request through several personas. Each persona reads the diff with its
own checklist; the Chief Architect synthesizes the result into one verdict.

Read [AGENTS.md](../../../AGENTS.md) and [ARCHITECTURE.md](../../../ARCHITECTURE.md) before
starting, and check the diff against both.

The review follows the `writing` skill. Load it before drafting and run its revision pass
on `review.md` before you report.

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

Read the PR description. It must give a reason for each new dependency (Step 3).

### Step 2: Pick personas

Merge Specialist, Go Mentor and Chief Architect review every PR. Add the others by path:

| Changed files | Add |
| --- | --- |
| `cmd/meru/**` | Go Developer, Security (CLI must stay thin: no engine/store imports) |
| `cmd/merud/**` | Go Developer, Security, SRE |
| `internal/rpc/**` | Go Developer, Security, SRE |
| `internal/engine/**` | Go Developer, AI/Agent, Security (loopback-only), SRE |
| `internal/agent/**` | AI/Agent, Go Developer, Security, SRE |
| `internal/mcp/**` | AI/Agent, Security, Go Developer, SRE |
| `internal/a2a/**` | AI/Agent, Security, Go Developer |
| `internal/store/**` (and retrieval, memory) | Go Developer, Security, SRE |
| `internal/obs/**` | SRE, Security (loopback, content capture) |
| `internal/**` (skills, scheduler, anything else) | Go Developer, AI/Agent if it touches a turn |
| `deploy/**` (launchd/systemd files, compose file, dashboards) | SRE, Security |
| `go.mod`, `go.sum` | Security (vulns, provenance), Go Developer |
| `*_test.go` | Go Developer |
| `docs/**`, `*.md` | Go Mentor, Chief Architect only (unless the doc changes a contract) |

Persona files:

- [Merge Specialist](personas/merge-specialist.md) (always)
- [Go Developer](personas/go-developer.md)
- [Security Engineer](personas/security-engineer.md)
- [SRE Engineer](personas/sre-engineer.md)
- [AI/Agent Developer](personas/ai-agent-developer.md)
- [Go Mentor](personas/go-mentor.md) (always)
- [Chief Architect](personas/chief-architect.md) (always, writes the synthesis)

### Step 3: Meru merge-blocking checklist

The Merge Specialist owns this list. Any unchecked item is a **blocker** unless the PR
description argues for the exception and the Chief Architect accepts it.

**Non-negotiables (AGENTS.md)**

- [ ] **No hosted-model code path.** No HTTP client, SDK (software development kit) or
      config key that could reach a hosted model. Model traffic goes only to Ollama on loopback. Grep the diff for new
      `http.Client`, `http.NewRequest`, provider SDK imports and non-loopback URLs.
- [ ] **No telemetry leaves the machine.** No update check, crash reporting or phone-home. OTLP (OpenTelemetry
      Protocol) export stays off until config sets an endpoint, and `merud` refuses a
      non-loopback one. Prompt/response text stays out of spans unless
      `capture_content = true`.
- [ ] **Deny-by-default.** A new MCP (Model Context Protocol) server or A2A (agent-to-agent)
      peer contributes zero tools until config allowlists them. `merud` connects only to
      loopback, except to A2A agents and Streamable HTTP MCP servers marked
      `network = true`.
- [ ] **One dispatch path.** Every external action (MCP tool call, A2A message) goes
      through `dispatch`, which checks the allowlist, confirms writes, writes a `tool_calls`
      row and records span and metrics. No second path.

**Shape and conventions**

- [ ] The PR description gives a reason for each new dependency in `go.mod` (standard library first).
- [ ] `cmd/meru` stays thin: no model, store or engine imports, no business logic.
- [ ] The `Engine` interface still has four methods (`Generate`, `Stream`, `Embed`, `Info`).
- [ ] Metric attributes are bounded sets (model, tier, server, tool, route, outcome). No IDs,
      paths or text on metrics; those belong on spans.
- [ ] ARCHITECTURE.md or the config reference documents each new `config.toml` key, with
      its default and meaning. No concrete model names in code.
- [ ] `merud` can rebuild anything it writes to SQLite from files (JSONL transcripts,
      config, indexed sources). SQLite is a projection, never the only copy.
- [ ] Comments explain the code for a Go beginner (see Go Mentor).
- [ ] PR prose follows the `writing` skill: code comments, docs, coding notes, commit
      messages and the PR description (see Go Mentor).
- [ ] `docs/coding-notes/` has a new or updated explainer for new or changed code.
- [ ] No AI or assistant attribution in commits, the PR body, code comments or docs
      (`Co-Authored-By: Claude`, "Generated with", etc.). Check with
      `gh pr view {number} --json commits,body`.

### Step 4: Run the checks

```bash
gh pr checkout {number}

go build ./...
go vet ./...
gofmt -l .                  # must print nothing
go test -race ./...

# Optional tools: run if installed, otherwise report "not installed" (not a failure)
if command -v staticcheck >/dev/null; then staticcheck ./...; else echo "staticcheck: not installed"; fi
if command -v govulncheck >/dev/null; then govulncheck ./...; else echo "govulncheck: not installed"; fi

git checkout main
```

If there is no `go.mod` yet, mark the Go checks N/A. A failing build, vet, gofmt, test or
govulncheck finding in reachable code is a blocker.

### Step 5: Review

Adopt each selected persona in turn and fill in its section using the format in its
persona file. Cite `file:line` for every finding. Skip sections that don't apply; don't
pad with "N/A".

### Step 6: Write review.md

```markdown
# PR Review: #{number} - {title}

*Date: {date} · URL: {url} · Author: {author}*

## Summary

{2-4 sentences: what the PR does and why.}

| File | Area | + | - |
| --- | --- | --- | --- |
| {file} | {cmd/merud, internal/agent, docs, ...} | {n} | {n} |

## Checks

| Check | Status | Details |
| --- | --- | --- |
| `go build ./...` | {PASS/FAIL/N/A} | |
| `go vet ./...` | {PASS/FAIL/N/A} | |
| `gofmt -l .` | {PASS/FAIL/N/A} | {files listed, if any} |
| `go test -race ./...` | {PASS/FAIL/N/A} | {packages, failures, races} |
| `staticcheck ./...` | {PASS/FAIL/not installed} | |
| `govulncheck ./...` | {PASS/FAIL/not installed} | {reachable vulns} |

## Meru Checklist

| Item | Status | Details |
| --- | --- | --- |
| No hosted-model code path | {PASS/FAIL} | |
| No telemetry off the machine; OTLP loopback-only; no content in spans by default | {PASS/FAIL/N/A} | |
| Deny-by-default tools and agents; loopback-only `merud` | {PASS/FAIL/N/A} | |
| All external actions through `dispatch` + `tool_calls` | {PASS/FAIL/N/A} | |
| New dependencies justified | {PASS/FAIL/N/A} | {module: reason} |
| CLI thin / Engine still four methods | {PASS/FAIL/N/A} | |
| Metric attributes bounded | {PASS/FAIL/N/A} | |
| Config keys documented | {PASS/FAIL/N/A} | |
| SQLite rebuildable from files | {PASS/FAIL/N/A} | |
| Beginner-friendly comments | {PASS/FAIL} | |
| Prose follows the `writing` skill | {PASS/FAIL} | |
| `docs/coding-notes/` updated | {PASS/FAIL/N/A} | |
| No AI attribution | {PASS/FAIL} | |

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

- [ ] {Required action before merge}
```

### Step 7: Report

Tell the user the verdict, list blockers, and give the path to `review.md`.

## Principles

- **Simple wins every time.** A smaller diff with fewer moving parts beats a clever one.
  Flag abstractions, interfaces or packages the change doesn't need yet.
- **Build to the milestone.** Code for a later ROADMAP milestone is scope creep.
- **The owner is learning Go.** Phrase findings so they teach: say what's wrong, why it
  matters in Go, and show the idiomatic fix.
- **Credit good work** alongside the problems.

### Severity

- **Blocker:** failing checks, a broken non-negotiable, a security hole, a data-loss risk,
  or a contract change to ARCHITECTURE.md that the PR doesn't argue for.
- **Should fix:** missing tests, unclear errors, missing comments or coding-notes,
  unbounded attributes, leaked goroutines.
- **Consider:** naming, style, small simplifications.

### Verdicts

All personas use the same three: **APPROVE**, **APPROVE WITH CHANGES** (no blockers,
small fixes), **REQUEST CHANGES** (any blocker).
