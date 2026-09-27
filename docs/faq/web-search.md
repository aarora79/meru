# How do I turn on web search?

Meru searches the web through SearXNG, a search engine you run in Docker. It
needs no account and no API key.

1. **Start SearXNG** on this machine only:

   ```sh
   mkdir -p ~/srv/searxng/core-config && cd ~/srv/searxng
   curl -fsSL -O https://raw.githubusercontent.com/searxng/searxng/master/container/docker-compose.yml \
        -O https://raw.githubusercontent.com/searxng/searxng/master/container/.env.example
   cp -i .env.example .env && printf 'SEARXNG_HOST=127.0.0.1\nSEARXNG_PORT=8888\n' >> .env
   docker compose up -d
   ```

   The two lines added to `.env` keep it on `127.0.0.1:8888`, so no other
   machine on your network can use it.

2. **Turn on JSON**, which Meru needs:

   ```sh
   cat >> ~/srv/searxng/core-config/settings.yml <<'EOF'

   search:
     formats:
       - html
       - json
   EOF
   docker compose restart
   ```

3. **Check it.** This should print a line that starts `{"query":`:

   ```sh
   curl -s 'http://127.0.0.1:8888/search?q=test&format=json' | head -c 200
   ```

4. **Restart `merud`.** Its log says `web search ready`, and `meru tools` lists
   `web_search` under `meru`.

Then ask: `meru "search the web for the latest Go release"`. To keep a question
on the web alone, pick Web in the desktop app or type `/scope web` in `meru
chat`.

**What leaves your machine:** the search words, which go to the engines SearXNG
asks. Your question, files and answers stay here.

**Reading pages.** `web_fetch` reads a whole page or downloads a file into
`~/meru-output/downloads/`. It is on by default. It asks you first for an
address that no search result or question of yours gave, and before every
download.

**Turn it off.** For search, set `searxng_url = ""` under `[web]`. For page
reads, take `web_fetch` out of `[builtin] tools`. Restart `merud` after either.

**Searches come back empty** when an engine is rate-limiting you. Wait, or turn
that engine off in `settings.yml`.

More detail: [running.md, Web search](../running.md#web-search).
