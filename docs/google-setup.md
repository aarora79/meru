# Set up Gmail, Calendar and Drive for Meru

This guide takes you from nothing to Meru reading your mail, calendar and Drive, in
one pass. It takes about 20 minutes. Do the steps in order and don't skip any.

Meru talks to Google through [workspace-mcp](https://github.com/taylorwilsdon/google_workspace_mcp),
a small server that runs on your computer, at `http://127.0.0.1:8000/mcp`. Meru
installs `workspace-mcp` 1.30.0 into `~/.meru/runtime`, starts it when a question
needs it and restarts it when it stops: Google is one of Meru's *connectors*
([how connectors work](architecture/connectors.md)). Your Google password never
touches Meru, and the keys you create below stay on your computer.

You will:

1. Make a Google Cloud project and turn on four Google APIs (application
   programming interfaces: the doors a program uses to reach your mail and files).
2. Tell Google who may sign in to your project: you.
3. Create an OAuth client, which gives you a client ID and a client secret. OAuth
   is the standard way to let a program act for you without your password.
4. Give Meru those values and turn the Google connector on.
5. Sign in once, at the link Meru shows you.

If you already run the server yourself, from an earlier version of this guide,
skip to [Move a server you run over to Meru](#move-a-server-you-run-over-to-meru).
If you would rather keep running it yourself, steps 1 to 5 still apply; then
follow [Run the server yourself](#run-the-server-yourself).

You need a Google account (a personal Gmail address works), a Mac or Linux
computer with Meru installed ([running.md](running.md)), and a web browser.

---

## Step 1: make a Google Cloud project

1. Open <https://console.cloud.google.com/projectcreate>. Sign in with the Google
   account whose mail you want Meru to read.
2. If Google asks you to accept the terms of service, accept them.
3. **Project name:** type `meru`. Leave **Location** as it is.
4. Click **Create**. Wait for the bell icon at the top right to say the project is
   ready, about 30 seconds.
5. At the top left, next to the Google Cloud logo, check that the project picker
   says `meru`. If it shows another project, click it and choose `meru`.

Google Cloud doesn't charge for this. The APIs below are free at the level one
person uses them, and you don't need to add a card.

## Step 2: turn on the four APIs

Open each link below. On each page, check that the project picker at the top says
`meru`, then click the blue **Enable** button. When the button changes to
**Manage**, that API is on.

| API | Link |
| --- | --- |
| Gmail API | <https://console.cloud.google.com/apis/library/gmail.googleapis.com> |
| Google Calendar API | <https://console.cloud.google.com/apis/library/calendar-json.googleapis.com> |
| Google Drive API | <https://console.cloud.google.com/apis/library/drive.googleapis.com> |
| Google Docs API | <https://console.cloud.google.com/apis/library/docs.googleapis.com> |

Turn on all four, even if you only want mail for now. The server asks for all four
when it starts.

## Step 3: set up the sign-in screen

Google shows a sign-in screen, the "consent screen", when a program asks for your
data. You set it up once.

1. Open <https://console.cloud.google.com/auth/overview>. Its title reads
   **Google Auth Platform**.
2. Click **Get started**.
3. **App information:**
   - **App name:** `Meru`
   - **User support email:** pick your own address from the list.
   - Click **Next**.
4. **Audience:** choose **External**, then **Next**. (**Internal** only appears for
   company Google Workspace accounts. If you see it and your mail is a company
   account, you may choose it and skip step 4 below.)
5. **Contact information:** type your own email address, then **Next**.
6. **Finish:** tick the box to agree to Google's user data policy, click
   **Continue**, then **Create**.

## Step 4: add yourself as a test user

A new project is in "Testing" mode. In that mode only the people on its test user
list can sign in, and Google blocks everyone else, you included, until you add
yourself.

1. In the left menu of Google Auth Platform, click **Audience**.
2. Scroll to **Test users** and click **Add users**.
3. Type the Gmail address you will sign in with. Click **Save**.

## Step 5: create the OAuth client and copy its ID and secret

1. In the left menu, click **Clients**, then **Create client**.
2. **Application type:** choose **Desktop app**. This matters: other types need
   extra settings and fail with `redirect_uri_mismatch`.
3. **Name:** `Meru desktop`. Click **Create**.
4. A box shows your **Client ID** and **Client secret**. Click **Download JSON**
   and keep the file somewhere safe, such as your password manager. Then copy both
   values into a note for Step 6.

   - The client ID ends in `.apps.googleusercontent.com`.
   - The client secret starts with `GOCSPX-`.

   Google may not show the secret again after you close this box. If you lose it,
   open the client, add a new secret, and use that one.

Treat the secret like a password. Don't paste it into a chat, an email or a file
you share.

## Step 6: turn on the Google connector

In the desktop app, open Settings, then Connections. On Google's card, type
your address, the client ID and the client secret, and press **Save and turn
on**. The secret field stays empty after you save and shows "saved": `merud`
keeps the secret in `~/.meru/secrets.toml` and never sends it back.

In a terminal, run this with your own address and client ID in place of the
angle brackets. `meru` then asks for the client secret without showing it:

```sh
meru mcp set google enabled=true email=<your Gmail address> client_id=<your client ID> client_secret
```

`merud` checks each value first: an address needs an `@`, and a client ID ends
in `.apps.googleusercontent.com`. Either way it writes the same two files you
could write by hand instead:

1. Add this table to `~/.meru/config.toml`, with your own address and client
   ID in place of the angle brackets:

   ```toml
   [connectors.google]
   enabled   = true
   email     = "<your Gmail address>"
   client_id = "<your client ID>"
   ```

2. Add the client secret to `~/.meru/secrets.toml`, which only you can read,
   under the name Meru looks for:

   ```sh
   touch ~/.meru/secrets.toml
   chmod 600 ~/.meru/secrets.toml
   open -e ~/.meru/secrets.toml     # Linux: nano ~/.meru/secrets.toml
   ```

   ```toml
   connector_google_client_secret = "<your client secret>"
   ```

3. Restart `merud` so it reads both files (see [running.md](running.md)).
   Settings and `meru mcp set` need no restart.

`merud` now downloads its own `uv` and Python into `~/.meru/runtime`, installs
`workspace-mcp` 1.30.0 there, starts it on port 8000 and checks it. You install
nothing yourself, and nothing lands in your own Python or Homebrew. The first
time takes a minute or two. Watch it with:

```sh
meru mcp status
```

The Settings card shows the same steps. The `google` line moves from `starting` ("Meru is installing Google 1.30.0 and
checking it.") to `needs config`, "Google needs you to sign in.", with a link.

If you ever ran the server yourself and it still runs, the line says "Google
can't start: another program listens on 127.0.0.1:8000." Stop that server;
Meru looks again every 30 seconds. Meru never stops a program it didn't start.

## Step 7: sign in to Google, once

Open the link from `meru mcp status`, the one after `Sign in:`. In the desktop
app, open Settings, then Connections, and click **Sign in to Google** on
Google's card. `meru chat` shows the same link in `/mcp`. Then:

1. Choose your Google account.
2. Google says **Google hasn't verified this app**. You'll always see this: the
   app is your own project from Step 1, and Google reviews only apps it publishes
   to others. Click **Advanced**, then **Go to Meru (unsafe)**.
3. Tick every box on the permissions page, or click **Select all**, then
   **Continue**.
4. The browser shows a page that says authentication succeeded. Close it.

Within 15 seconds `meru mcp status` says "Google is running." The server keeps
your sign-in in `~/.google_workspace_mcp/credentials/`, so you won't see a link
again until it expires (see [Signing in again every 7
days](#signing-in-again-every-7-days)). Each link works for ten minutes; Meru
shows a fresh one while it waits.

Check the whole setup:

```sh
meru tools
meru "what was the last email I sent?"
```

`meru tools` lists the `google` tools, and Meru answers from your mail. If Meru
doesn't know your email yet, run `meru setup user` and answer the email question
with the same Gmail address.

## Move a server you run over to Meru

If you followed an earlier version of this guide, `~/.meru/config.toml` has a
`google` entry under `[[mcp.servers]]`, and you run `workspace-mcp` yourself,
from a terminal or from the launchd job `com.meru.workspace-mcp`. That keeps
working as it is; `meru mcp status` calls it "set up by hand". To let Meru run
it instead:

1. If you start the server in a terminal, stop it there (Ctrl-C). A launchd
   job needs nothing from you: the next step stops it after you say yes.
2. Run:

   ```sh
   meru mcp adopt google
   ```

   Meru reads your address, client ID and secret from
   `~/.config/workspace-mcp/start.sh`, prints every change it will make, and
   asks before it makes any. If you have no `start.sh`, give the values
   yourself; it then asks for the secret without showing it:

   ```sh
   meru mcp adopt google --email <your Gmail address> --client-id <your client ID>
   ```

   The **Adopt** button on Google's card in Settings does the same when
   `start.sh` holds the values: it shows the plan in a dialog and changes
   nothing until you press Adopt there.

Adopt saves the secret in `~/.meru/secrets.toml`, turns your `google` entry into
comments between two marker lines, writes `[connectors.google]` after it with
your allow and confirm lists, stops the launchd job and renames its file to
`com.meru.workspace-mcp.plist.disabled`, and asks `merud` to reload. Meru then
runs the server on the same port, with the sign-in you already have, so you
don't sign in again.

To go back, run `meru mcp unadopt google`. It puts your entry back as it was,
takes `[connectors.google]` out, and starts the launchd job again if Adopt
stopped it. The secret stays in `~/.meru/secrets.toml`.

## Signing in again every 7 days

While your project is in "Testing" mode, Google ends each sign-in after 7 days.
When that happens, the Google connector says "Google needs you to sign in." at
its next start, with a new link: do Step 7 again. If you run the server
yourself, Meru's Google answers fail with an error such as `invalid_grant` or a
new sign-in link; do Step E below again.

To stop that, publish the project:

1. Open <https://console.cloud.google.com/auth/audience>.
2. Under **Publishing status**, click **Publish app**, then **Confirm**.

Google doesn't review a project only you use, so it stays unverified: you keep
seeing the "hasn't verified this app" screen at each sign-in, and Google limits the
project to 100 users. Neither matters for one person.

## Run the server yourself

This is the older way: you start `workspace-mcp` and keep it running, and Meru
only connects to it. Do steps 1 to 5 first, then these steps instead of steps 6
and 7.

### Step A: install uv

The server is a Python program, and `uv` downloads and runs it for you. You don't
need to install Python yourself.

On a Mac with Homebrew:

```sh
brew install uv
```

On any Mac or Linux computer:

```sh
curl -LsSf https://astral.sh/uv/install.sh | sh
```

Close the terminal and open a new one, then check:

```sh
uvx --version
```

It prints a version number. If it says `command not found`, open a new terminal
once more.

### Step B: save a start script

A small script holds your client ID and secret, so you don't type them each time
and they stay out of your shell history.

1. Make the folder and the file:

   ```sh
   mkdir -p ~/.config/workspace-mcp
   touch ~/.config/workspace-mcp/start.sh
   chmod 700 ~/.config/workspace-mcp/start.sh
   open -e ~/.config/workspace-mcp/start.sh     # Linux: nano ~/.config/workspace-mcp/start.sh
   ```

2. Paste this into the file. Replace the three values in angle brackets, and
   remove the brackets:

   ```sh
   #!/bin/sh
   # Starts the Google server Meru connects to. Keep this file private.
   export GOOGLE_OAUTH_CLIENT_ID="<your client ID>"
   export GOOGLE_OAUTH_CLIENT_SECRET="<your client secret>"
   export USER_GOOGLE_EMAIL="<your Gmail address>"
   export WORKSPACE_ATTACHMENT_DIR="$HOME/meru-output/attachments"
   exec uvx workspace-mcp==1.30.0 --transport streamable-http \
     --tool-tier extended --tools gmail calendar drive docs
   ```

3. Save and close the file.

What each line does:

| Line | Why |
| --- | --- |
| `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` | the client from Step 5 |
| `USER_GOOGLE_EMAIL` | the account every tool uses, so the model never has to guess it |
| `WORKSPACE_ATTACHMENT_DIR` | saves mail attachments where Meru can read them |
| `--transport streamable-http` | serves Meru at `http://127.0.0.1:8000/mcp` |
| `--tool-tier extended` | includes the attachment and thread tools Meru uses |
| `--tools gmail calendar drive docs` | only these four services, not the dozen others it knows |

### Step C: start the server

```sh
~/.config/workspace-mcp/start.sh
```

The first start downloads the server, which takes a minute. Then it prints a few
lines and stays running. Leave this terminal open: when you close it, the server
stops. Step F shows how to start it at login instead.

Check it from a second terminal:

```sh
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8000/mcp
```

Any number, such as `406` or `400`, means the server answers. `000` means it isn't
running; look at the first terminal for an error.

### Step D: connect Meru

With `merud` running (see [running.md](running.md)):

```sh
meru mcp add google
```

It checks that the server answers, lists the tools it offers, adds the `google`
entry to `~/.meru/config.toml`, and asks `merud` to load it; no restart needed. It
ends with a line such as `google · connected · offers 45, 10 allowed`. If you
added `google` before, `~/.meru/config.toml` already has the entry: skip this
command, and check that the entry's `allow` list ends with
`get_gmail_attachment_content`.

If Meru doesn't know your email yet, run `meru setup user` and answer the email
question with the same Gmail address.

### Step E: sign in to Google, once

Ask Meru something that needs your mail:

```sh
meru "what was the last email I sent?"
```

The first time, the server opens a Google sign-in page in your browser. If no
page opens, the answer includes the link, starting with
`https://accounts.google.com/`; open it yourself. Then:

1. Choose your Google account.
2. Google says **Google hasn't verified this app**. You'll always see this: the
   app is your own project from Step 1, and Google reviews only apps it publishes
   to others. Click **Advanced**, then **Go to Meru (unsafe)**.
3. Tick every box on the permissions page, or click **Select all**, then
   **Continue**.
4. The browser shows a page that says authentication succeeded. Close it.

Ask the question again. Meru now answers from your mail. The server keeps your
sign-in in `~/.google_workspace_mcp/credentials/`, so you won't see the link again
until it expires (see [Signing in again every 7 days](#signing-in-again-every-7-days)).

Check the whole setup:

```sh
meru tools
```

The `google` block says `connected` and shows no warnings.

### Step F (optional): start the server at login

So you don't keep a terminal open, let macOS start the server when you log in.

1. Make the file `~/Library/LaunchAgents/com.meru.workspace-mcp.plist` with this
   content. Replace `YOURNAME` with your macOS user name (run `whoami`):

   ```xml
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0">
   <dict>
     <key>Label</key><string>com.meru.workspace-mcp</string>
     <key>ProgramArguments</key>
     <array><string>/Users/YOURNAME/.config/workspace-mcp/start.sh</string></array>
     <key>EnvironmentVariables</key>
     <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/Users/YOURNAME/.local/bin:/usr/bin:/bin</string></dict>
     <key>RunAtLoad</key><true/>
     <key>KeepAlive</key><true/>
     <key>StandardOutPath</key><string>/Users/YOURNAME/.config/workspace-mcp/server.log</string>
     <key>StandardErrorPath</key><string>/Users/YOURNAME/.config/workspace-mcp/server.log</string>
   </dict>
   </plist>
   ```

2. Stop the server you started in Step C (Ctrl-C in its terminal), then load the
   job:

   ```sh
   launchctl load ~/Library/LaunchAgents/com.meru.workspace-mcp.plist
   ```

3. Check it with the `curl` line from Step C. The log is in
   `~/.config/workspace-mcp/server.log`.

To stop it for good: `launchctl unload ~/Library/LaunchAgents/com.meru.workspace-mcp.plist`.
On Linux, a `systemd --user` service that runs `start.sh` does the same job.

## When something goes wrong

| What you see | What it means | What to do |
| --- | --- | --- |
| `Error 403: access_denied` in the browser | you aren't a test user | Step 4: add the address you signed in with |
| `Error 400: redirect_uri_mismatch` | the client isn't a Desktop app | Step 5: create a new client of type **Desktop app**, and put its ID and secret in `start.sh` |
| `... API has not been used in project ... or it is disabled` | one API is off | Step 2: turn it on, wait a minute, ask again |
| `invalid_grant` or a new sign-in link after a week | the 7-day limit | Step 7 again (Step E if you run the server), or publish the app |
| `meru mcp status`: "Google needs you to sign in." | no sign-in saved yet, or it expired | Step 7: open the link after `Sign in:` |
| `meru mcp status`: "Google can't start: another program listens on 127.0.0.1:8000." | a server you started yourself, or another program, holds port 8000 | stop it; Meru looks again every 30 seconds. If it is your own Google server, see [Move a server you run over to Meru](#move-a-server-you-run-over-to-meru) |
| `meru mcp status`: "Google needs your email address." or another missing value | `[connectors.google]` lacks a key, or `secrets.toml` lacks `connector_google_client_secret` | Fix on Google's card in Settings, or `meru mcp fix google`, asks for what is missing |
| `meru mcp status`: "Google failed its check: …" | the server answers, but reading your calendar list fails, most often because an API is off | Step 2, then restart `merud` |
| `meru mcp adopt google` says another program listens on 127.0.0.1:8000 | the server you started in a terminal still runs | stop it (Ctrl-C there), then run adopt again |
| `address already in use` when you start the server yourself | something else holds port 8000 | stop the other program, or add `export WORKSPACE_MCP_PORT=8001` to `start.sh` and change the `url` in the `google` entry of `~/.meru/config.toml` to `http://127.0.0.1:8001/mcp`. The connector, and Adopt, need port 8000 |
| `meru tools` warns `google offers no such tool` | the server started without `--tool-tier extended` | fix the last line of `start.sh`, restart the server, and ask Meru a question that uses Google; that turn lists the tools again and the warning goes |
| `meru tools` shows `google` as `not connected` | the server you run isn't running | Step C, or check the log from Step F |
| Meru finds a mail but can't read its attachment | the server saves attachments elsewhere | check `WORKSPACE_ATTACHMENT_DIR` in `start.sh`, restart the server; see [Read a mail's attachment](running.md#read-a-mails-attachment) |
| `command not found: uvx` | uv isn't on your `PATH` | Step A; for launchd, add uv's folder to `PATH` in the plist |

To start over from sign-in, stop Meru's Google connector (its switch in
Settings, or `meru mcp set google enabled=false`) or the server you run, delete
`~/.google_workspace_mcp/credentials/`, start it again and sign in once more.

## What Meru may do with your account

Meru only calls the Google tools listed under `allow` in the `google` entry of
`~/.meru/config.toml`. Sending mail and changing a calendar event ask you first.
[The google entry](running.md#the-google-entry) lists every tool and whether it
asks.
