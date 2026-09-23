# meru — repo guide

**Meru** is a fully on-device personal AI assistant. Read [ARCHITECTURE.md](ARCHITECTURE.md)
before writing code here — it is the design contract, written deliberately ahead of the
implementation.

## Non-negotiables

1. **No hosted-model code path.** Not feature-flagged, not disabled — absent. If you find
   yourself adding an HTTP client to a model provider, stop and raise it instead.
2. **No telemetry, no update check, no crash reporting.** Ever.
3. **Deny-by-default for tools and network.** New MCP servers contribute zero tools until
   explicitly allowlisted in config.
4. **Every external action is logged** to `tool_calls`. Don't add a dispatch path that skips it.

## Shape

- `merud` is the resident daemon (models, store, MCP pool, scheduler).
- `meru` is a thin socket client. Keep it thin — no model or store logic in the CLI.
- The `Engine` protocol is four methods. Resist growing it.
- Files are the source of truth; `~/.meru/meru.db` is a rebuildable projection.

## Conventions

- Python 3.12, managed with `uv`. Don't chase the system Python — MLX wheels lag.
- Standard library first. Each new dependency needs a reason in the PR description.
- Type hints throughout; `ruff` for lint and format.
- Prefer readable rows over opaque blobs — memory and audit data must be greppable.

## Attribution

Do not add AI or assistant attribution to commits, PRs, code comments, or docs.
