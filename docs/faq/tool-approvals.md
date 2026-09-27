# How do I choose which tools ask me first?

Each tool is Off, Ask or Allow:

- **Off**: the model never sees it.
- **Ask**: Meru asks you before each call.
- **Allow**: it runs without asking.

`configure`, and any tool that runs commands, always asks, whatever you set.

**In the desktop app**: Settings, Connections, then the switch beside each tool.
In `meru chat`: `/mcp`, then ← and → on a tool. Both apply at once.

**In `config.toml`**, then restart `merud`:

```toml
# An MCP server: allow lists the tools the model may use, confirm the ones that ask.
[[mcp.servers]]
name    = "obsidian"
allow   = ["obsidian_list_files_in_vault", "obsidian_simple_search", "obsidian_append_content"]
confirm = ["obsidian_append_content"]

# Meru's own tools.
[builtin]
confirm = ["write_file", "web_fetch"]

# A local command: add confirm = true to its entry.
[[commands]]
name        = "gh-issue-comment"
description = "Adds a comment to an issue in a GitHub repository"
argv        = ["gh", "issue", "comment", "{number}", "--repo", "{repo}", "--body", "{body}"]
confirm     = true
# … and its [commands.params] tables
```

A tool name in `confirm` must also be in `allow` or `tools`.

**Turn a built-in tool off** by taking its name out of `[builtin] tools`. The
full list is `configure`, `datetime`, `about_meru`, `remember`, `write_file`,
`read_file`, `list_folder`, `grep`, `search_files`, `web_search` and
`web_fetch`.

**When Meru asks**, you get the tool and its arguments:

```text
Run mail.send? [o]nce  [s]ession  [d]eny:
```

`o` runs this call, `s` runs every later call to that tool in this chat, and `d`
refuses. The box opens on deny, so a stray Enter runs nothing. In the desktop
app, **Edit first** puts a mail back in the box as a draft for you to change.

**Nobody to ask means no.** `meru run --json`, `meru check` and a question piped
from a script decline every call that would ask.

More detail: [running.md, When Meru wants to run a tool](../running.md#when-meru-wants-to-run-a-tool).
