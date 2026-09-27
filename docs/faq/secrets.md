# Where do I put an API key?

In `~/.meru/secrets.toml`, one `name = "value"` line per key, readable only by
you:

```toml
obsidian_api_key = "..."
```

```sh
chmod 600 ~/.meru/secrets.toml     # merud refuses the file otherwise
```

`config.toml` names the key and never holds it. Write `secret:<name>` where the
value goes:

```toml
[[mcp.servers]]
name = "obsidian"
env  = { OBSIDIAN_API_KEY = "secret:obsidian_api_key" }

[[a2a.agents]]
name    = "research"
headers = { Authorization = "secret:research_token" }
```

**Three ways to add a key:**

- `meru mcp add <name>` asks for it without showing it on screen, and writes
  the file for you.
- In the desktop app: Settings, Connections, Add your own MCP server, then tick
  Secret beside the variable.
- Edit the file yourself, then restart `merud`.

**What Meru does with it.** It passes the key only to the server that names it.
It strips every value in the file from transcripts, the tool log, `merud.log`
and traces. The model never sees a key: asked in chat to connect a server that
needs one, Meru sends you to `meru mcp add`.

**Tools with their own login** don't use this file. `gh` keeps its token in your
keychain, and the Google server keeps its own tokens. A local command gets
nothing from `secrets.toml`.

More detail: [running.md, Credentials and secrets](../running.md#credentials-and-secrets).
