# How do I see which tools Meru may use and which ones ran?

**What the model may call:**

```sh
meru tools
```

```text
meru  builtin · connected
  remember
  write_file    asks first
  configure     always asks
  3 of 3 tools allowed

commands  command · connected
  cmd.git-log
    runs: git -C {repo} log --since=1.week --oneline
```

Each block is one source: an MCP server, another agent, Meru's built-in tools
or your local commands. A warning under a block names an `allow` entry the
source doesn't offer, most often a typo.

**What ran:**

```sh
meru log            # the last 20 tool calls, newest first
meru log -n 50      # the last 50
meru log -v         # with each call's result
```

Each line shows the time, the session, the tool, how the call ended, what you
chose if Meru asked, how long it took and its arguments. For a local command it
shows the exact program and arguments.

**For one answer:**

- `meru` prints each call on standard error as it runs, such as
  `→ grep {"pattern":"tomatoes"}`.
- In `meru chat`, `/used` lists the files, tool calls and memories behind the
  newest answer.
- In the desktop app, the side panel beside an answer shows the same, plus who
  the calls reached, such as "Only google was contacted." Settings, Activity
  lists every call.

**An answer says "Done." but nothing happened.** When an answer claims an
action and no tool ran, Meru adds a note saying nothing changed on your
computer. Believe the note, and check `meru log`. Meru's own tools write only
inside `~/meru-output` and run only your `[[commands]]`; none moves, renames or
deletes a file.

More detail: [running.md, See what tools ran](../running.md#see-what-tools-ran).
