# How do I change a setting?

Meru keeps its settings in `~/.meru/config.toml`.

**See every key** with its default and a comment on what it does:

```sh
meru config template
```

The keys that are on by default appear with their values. What's off, such as
the catalog servers, the sample commands and an A2A agent, sits in comments:
delete the `# ` at the start of a block's lines to turn it on. A file with only
the keys you change works too; every other key keeps its default.

**Apply a change** by restarting `merud` (see
[start-stop-merud.md](start-stop-merud.md)). It checks every value at startup
and refuses to start on a typo, an unknown key or a bad value, naming the key.

**Some changes need no file edit.** The desktop app's Settings, and `/mcp`,
`/folders`, `/skills` and `/model` in `meru chat`, write `config.toml` for you,
keep your comments, and apply at once.

**Settings people change most:**

| To | Set |
| --- | --- |
| use the larger models | `profile = "full"` |
| search more folders | `[index] folders` |
| give long answers more time | `[agent] turn_timeout = "10m"` |
| turn a built-in tool off | take it out of `[builtin] tools` |
| make a tool ask first | add it to `confirm` |
| log more | `[log] level = "debug"` |
| give plain text selection back in `meru chat` | `[chat] mouse_copy = false` |

**Start over** from the template. This replaces your file, so copy it first:

```sh
cp ~/.meru/config.toml ~/.meru/config.toml.bak
meru config template > ~/.meru/config.toml
```

**Try settings without touching your own** by giving a second Meru its own
folder:

```sh
merud -config /tmp/meru-test/config.toml
meru -socket /tmp/meru-test/merud.sock "hello"
```

More detail: [running.md, Change settings](../running.md#6-change-settings).
