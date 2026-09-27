# How do I drive Meru from a script?

**Just the answer:**

```sh
meru "summarize ~/notes/garden.md in three lines" > summary.txt
```

Tool lines and prompts go to standard error, so the file gets only the answer.
`meru` exits with 0 on success, 1 on an error and 130 on Ctrl-C.

**Every event, as JSON:**

```sh
meru run --json "what changed in my notes this week?"
```

Each line is one event: the route, the sources, each piece of the answer, each
tool call and its result, and last a `done` line with the timings and token
counts. Read them with `jq`:

```sh
meru run --json "what changed in my notes this week?" | jq -c 'select(.type == "done")'
```

To rebuild the answer, keep the `token` lines after the last `tool_result`.

**Nobody can approve a tool call** from a script, so Meru declines every call
that would ask first. Its `approval` line still comes out, so the script can
see which call it was. Give a script only tools that run without asking.

**A worked example.** [docs/examples/vault-digest.sh](../examples/vault-digest.sh)
writes a weekly digest of an Obsidian vault through the `obsidian` server.

More detail: [running.md, Drive Meru from a script](../running.md#drive-meru-from-a-script).
