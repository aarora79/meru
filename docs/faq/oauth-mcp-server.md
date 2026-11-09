# How do I sign in to an MCP server on the internet, such as GitHub's or Slack's?

Some MCP servers run on the internet and want you to sign in, the way a website
does. Meru signs in with OAuth: you approve it in your browser, and `merud` keeps
the token in `~/.meru/oauth/<name>.json`, a file only you can read. The token
never goes into a log, a chat or an error.

## The steps for any server

1. **Add an entry** to `~/.meru/config.toml` with the server's URL,
   `remote = true` and `auth = "oauth"`, and the tools you want in `allow`:

   ```toml
   [[mcp.servers]]
   name   = "notes-online"
   url    = "https://mcp.example.com/mcp"
   remote = true
   auth   = "oauth"
   allow  = ["search", "read_page"]
   ```

   With no `[mcp.servers.oauth]` table, Meru registers itself with the server,
   which most servers allow. GitHub and Slack don't; their sections below say
   what to add.

2. **Restart `merud`** (`launchctl kickstart -k gui/$(id -u)/com.meru.merud`, or
   `systemctl --user restart merud` on Linux). `meru mcp` shows the server as
   `sign_in`.

3. **Sign in.** Any of these opens the server's page in your browser:
   - the desktop app: **Settings → Connections**, the server's card, **Sign in**,
     or the **Sign in** button above an answer;
   - `meru chat`: `/signin notes-online`;
   - a terminal: `meru mcp login notes-online`.

   Approve Meru on the page. The tab says "Meru is signed in", and the server
   shows as connected. Meru waits up to ten minutes.

4. **Check the tools** with `meru tools`. It warns about any name in `allow` the
   server doesn't offer, since names in a service's docs can differ from the
   real ones.

A sign-in lasts until the service ends it. Then the server shows `sign_in`
again, and a question that needs it says so, with the button or the command.

## GitHub

GitHub's server is `https://api.githubcopilot.com/mcp/`. It needs an OAuth app of
your own.

1. On GitHub, open **Settings → Developer settings → OAuth Apps → New OAuth App**.
   Set the **Authorization callback URL** to `http://127.0.0.1/callback`; GitHub
   takes any port on loopback. Copy the **Client ID**, and generate a **client
   secret**.
2. Put the secret in `~/.meru/secrets.toml`:

   ```sh
   printf 'github_client_secret = "PASTE_SECRET"\n' >> ~/.meru/secrets.toml
   chmod 600 ~/.meru/secrets.toml
   ```

3. Add the entry:

   ```toml
   [[mcp.servers]]
   name   = "github"
   url    = "https://api.githubcopilot.com/mcp/"
   remote = true
   auth   = "oauth"
   allow  = ["get_me", "search_repositories", "search_issues", "list_issues", "issue_read",
             "list_pull_requests", "pull_request_read", "get_file_contents"]

     [mcp.servers.oauth]
     client_id     = "PASTE_CLIENT_ID"
     client_secret = "secret:github_client_secret"
     issuer        = "https://github.com/login/oauth"
     scopes        = ["repo", "read:org", "read:user"]
   ```

These eight tools only read. A tool that writes, such as `issue_write` or
`create_pull_request`, belongs in `confirm` as well, so Meru asks before each
call. You can also skip the sign-in and read GitHub through `gh`; see
[github.md](github.md).

## Slack

Slack's server is `https://mcp.slack.com/mcp`. It needs a Slack app in your
workspace, with Slack's MCP server turned on for it.

1. At [api.slack.com/apps](https://api.slack.com/apps), choose **Create New App →
   From a manifest**, pick your workspace, and paste this on the JSON tab:

   ```json
   {
     "display_information": { "name": "Meru" },
     "oauth_config": {
       "redirect_urls": ["http://localhost:3118/callback"],
       "scopes": {
         "user": ["search:read.public", "search:read.private", "search:read.im", "search:read.mpim",
                  "search:read.users", "channels:history", "groups:history", "im:history",
                  "mpim:history", "channels:read", "groups:read", "im:read", "mpim:read",
                  "users:read", "canvases:read"]
       }
     },
     "settings": { "token_rotation_enabled": false }
   }
   ```

2. **Turn on the MCP server for the app.** It starts off. Find the switch named
   for the Slack MCP server in the app's **Agents** section, or under **Features →
   Agents & AI Apps**. Until it is on, Slack answers every request with "Bad
   Request".
3. Don't install the app from Slack's page: Meru's sign-in asks for your
   approval itself.
4. On **Basic Information → App Credentials**, copy the **Client ID**, two runs of
   digits joined by a dot, and the **Client Secret**. Put the secret in
   `secrets.toml` as `slack_client_secret`, as for GitHub above. Run that line
   once: a name that appears twice stops `merud` from reading the file.
5. Add the entry. `redirect` must match the app's redirect URL, port included:

   ```toml
   [[mcp.servers]]
   name   = "slack"
   url    = "https://mcp.slack.com/mcp"
   remote = true
   auth   = "oauth"
   allow  = ["slack_search_public", "slack_search_public_and_private", "slack_search_channels",
             "slack_search_users", "slack_read_channel", "slack_read_thread", "slack_read_canvas",
             "slack_list_channel_members", "slack_list_user_channels"]

     [mcp.servers.oauth]
     client_id     = "PASTE_CLIENT_ID"
     client_secret = "secret:slack_client_secret"
     issuer        = "https://mcp.slack.com"
     redirect      = "http://localhost:3118/callback"
     scopes        = ["search:read.public", "search:read.private", "search:read.im", "search:read.mpim",
                      "search:read.users", "channels:history", "groups:history", "im:history",
                      "mpim:history", "channels:read", "groups:read", "im:read", "mpim:read",
                      "users:read", "canvases:read"]
   ```

Slack offers only the tools your scopes cover. With these scopes, every one of
them reads; none sends a message.

## When it goes wrong

| What you see | What to do |
| --- | --- |
| The browser says the client ID is invalid | `client_id` is wrong or empty. Copy it again, and restart `merud`. |
| "the server refused the sign-in" | Check the client secret in `secrets.toml` and the app's scopes. |
| "port 3118 is in use" | Close what holds the port, or change `redirect` and the app's redirect URL to match. |
| "you didn't finish signing in within 10 minutes" | Sign in again. |
| "Bad Request" from Slack | Turn on the MCP server in the Slack app (step 2), then restart `merud`. |
| `merud` stops with "has already been defined" | A name appears twice in `secrets.toml`. Keep one line. |

[running.md](../running.md) and [ARCHITECTURE.md](../../ARCHITECTURE.md#mcp)
have the full story.
