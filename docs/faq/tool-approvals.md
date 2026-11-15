# How do I choose which tools ask me first?

Each tool is Off, Ask, Always ask or Allow:

- **Off**: the model never sees it.
- **Ask**: Meru asks you before each call, and you can say yes for the rest of
  the chat.
- **Always ask**: Meru asks before every call, with no yes for the chat. Only an
  MCP server's tools have it; use it for a tool that sends, such as mail.
- **Allow**: it runs without asking.

`configure` and `schedule_job` always ask, whatever you set. Under each MCP tool,
Settings shows what its server says it does: "read-only", "may delete" or
"changes things". A hint is the server's claim, so keep a tool that sends at Ask
or Always ask whatever it says.

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

**After mail or the web, Meru asks every time.** Once a chat has read your mail,
a web page or another server's answer, a server's tool that asks offers only
once and deny, and an earlier `s` stops counting. Meru says why: "This chat has
read mail or the web, so Meru asks before each call." `remember` asks then too.
An email or a page can hold words written to steer the model, and this keeps a
yes you gave for one thing from carrying another. `write_file` and `edit_file`
keep their `s`. To make a tool ask every time in every chat, list it in its
server's `always_confirm`; the catalog's `send_gmail_message` comes that way.
[What stops an email or a web page from acting for me?](prompt-injection.md)
says more.

**Nobody to ask means no.** `meru run --json`, `meru check` and a question piped
from a script decline every call that would ask.

More detail: [running.md, When Meru wants to run a tool](../running.md#when-meru-wants-to-run-a-tool).
