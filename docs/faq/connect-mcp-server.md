# How do I connect Obsidian, Google or another MCP server?

An MCP (Model Context Protocol) server gives the model tools, such as searching
your notes or reading your mail. Meru's catalog knows two:

| Name | What the model gets |
| --- | --- |
| `obsidian` | list, read and search notes; appending asks first |
| `google` | Gmail, Calendar, Drive and Docs; sending mail and changing an event ask first |

**Add a catalog server:**

```sh
meru mcp list          # the catalog, and the servers you have
meru mcp add obsidian
```

Choose `d` (do it for me), and Meru asks for each thing the server needs. It
reads an API key without showing it and saves it in `~/.meru/secrets.toml`.
Then it starts the server for a moment, lists its tools, shows the block it
will add to `config.toml`, and writes it only after you say yes. `merud`
reloads at once.

In the desktop app, Settings, Connections, Add a connection does the same. In
`meru chat`, type `/mcp`.

**Google** needs a Google Cloud OAuth client and a server you start yourself.
Follow [google-setup.md](../google-setup.md) first.

**Add any other server:**

```sh
meru mcp add stdio notes -- /usr/local/bin/notes-mcp --vault ~/notes   # merud starts it
meru mcp add http tasks http://127.0.0.1:8123/mcp                      # you start it
meru mcp add http team https://mcp.example.com/mcp --remote            # on another machine
```

An address off this machine needs `--remote`, since every call then sends your
data there.

**Pick the tools.** Meru proposes `allow` for tools the server marks read-only
and `ask` for the rest. Type `-name` to leave a tool out, `+name` to allow it
without asking, or `?name` to make it ask. Every tool of a server you add by
hand starts off until you turn it on.

**Check it:**

```sh
meru mcp               # one row per server: connected, tools offered, allowed
meru tools             # every tool the model may call
```

A server that shows `not connected` gets one more try on your next question
that uses tools. Start it, then ask; no restart needed.

**Remove one:** `meru mcp remove notes`, or `d` twice on it in `/mcp`.

More detail: [running.md, meru mcp add](../running.md#meru-mcp-add).
