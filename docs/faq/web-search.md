# How do I turn on web search?

Meru searches the web through SearXNG, a search engine that runs in Docker. It
needs no account and no API key.

1. **Have Docker running.** Docker Desktop, OrbStack or colima all work;
   `docker info` should answer.

2. **Turn on the SearXNG connector.** In the desktop app, open Settings, then
   Connections, and flip the switch on the Web search card. In a terminal, run:

   ```sh
   meru mcp set searxng enabled=true
   ```

   Either one writes this table to `~/.meru/config.toml`, which you can also add
   by hand and then restart `merud`:

   ```toml
   [connectors.searxng]
   enabled = true
   ```

3. **Let it start.** `merud` pulls the SearXNG image pinned in this Meru release,
   writes `~/.meru/searxng/settings.yml` with JSON on, and starts the container
   `meru-searxng` on `127.0.0.1:8888` only, so no other machine on your network
   can use it. The first start takes a minute or so.

4. **Check it.** `meru mcp status` should say "Web search is running in the
   container meru-searxng.", and `meru tools` lists `web_search` under `meru`.
   If it says something else, the sentence tells you what to fix, such as
   "Web search can't start: Docker isn't running." Once you've fixed it, press
   Fix on the card, or run `meru mcp fix searxng`, and `merud` looks again at
   once.

Then ask: `meru "search the web for the latest Go release"`. To keep a question
on the web alone, pick Web in the desktop app or type `/scope web` in `meru
chat`.

**Already run SearXNG yourself?** Leave it on `127.0.0.1:8888` with JSON on.
`merud` sees it answering, uses it, and never starts, stops or pulls anything:
the status says "Web search uses the SearXNG already running at
http://127.0.0.1:8888."

**What leaves your machine:** the search words, which go to the engines SearXNG
asks. Your question, files and answers stay here. `merud`'s own check once a
minute asks SearXNG for an empty search, which reaches no engine.

**Reading pages.** `web_fetch` reads a whole page or downloads a file into
`~/meru-output/downloads/`. It is on by default. It asks you first for an
address that no search result or question of yours gave, and before every
download.

**Turn it off.** For search, flip the Web search card's switch off, or run
`meru mcp set searxng enabled=false`; `merud` stops its container. For page
reads, set `web_fetch` to Off under the Web search tools in the same section.

**Searches come back empty** when an engine is rate-limiting you. Wait, or turn
that engine off in `~/.meru/searxng/settings.yml` and run
`docker restart meru-searxng`.

More detail: [running.md, Web search](../running.md#web-search).
