# Set up Gmail, Calendar and Drive for Meru

This guide takes you from nothing to Meru reading your mail, calendar and Drive, in
one pass. It takes about 20 minutes. Do the steps in order and don't skip any.

Meru talks to Google through [workspace-mcp](https://github.com/taylorwilsdon/google_workspace_mcp),
a small server that runs on your computer. You start that server; Meru connects to
it at `http://127.0.0.1:8000/mcp`. Your Google password never touches Meru, and the
keys you create below stay on your computer.

You will:

1. Make a Google Cloud project and turn on four Google APIs (application
   programming interfaces: the doors a program uses to reach your mail and files).
2. Tell Google who may sign in to your project: you.
3. Create an OAuth client, which gives you a client ID and a client secret. OAuth
   is the standard way to let a program act for you without your password.
4. Save those in a start script and start the server.
5. Connect Meru and sign in once.

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
   values into a note for Step 7.

   - The client ID ends in `.apps.googleusercontent.com`.
   - The client secret starts with `GOCSPX-`.

   Google may not show the secret again after you close this box. If you lose it,
   open the client, add a new secret, and use that one.

Treat the secret like a password. Don't paste it into a chat, an email or a file
you share.

## Step 6: install uv

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

## Step 7: save a start script

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
   exec uvx workspace-mcp --transport streamable-http \
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

## Step 8: start the server

```sh
~/.config/workspace-mcp/start.sh
```

The first start downloads the server, which takes a minute. Then it prints a few
lines and stays running. Leave this terminal open: when you close it, the server
stops. Step 11 shows how to start it at login instead.

Check it from a second terminal:

```sh
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8000/mcp
```

Any number, such as `406` or `400`, means the server answers. `000` means it isn't
running; look at the first terminal for an error.

## Step 9: connect Meru

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

## Step 10: sign in to Google, once

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
until it expires (see the next section).

Check the whole setup:

```sh
meru tools
```

The `google` block says `connected` and shows no warnings.

## Signing in again every 7 days

While your project is in "Testing" mode, Google ends each sign-in after 7 days.
When that happens, Meru's Google answers fail with an error such as `invalid_grant`
or a new sign-in link. Do Step 10 again.

To stop that, publish the project:

1. Open <https://console.cloud.google.com/auth/audience>.
2. Under **Publishing status**, click **Publish app**, then **Confirm**.

Google doesn't review a project only you use, so it stays unverified: you keep
seeing the "hasn't verified this app" screen at each sign-in, and Google limits the
project to 100 users. Neither matters for one person.

## Step 11 (optional): start the server at login

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

2. Stop the server you started in Step 8 (Ctrl-C in its terminal), then load the
   job:

   ```sh
   launchctl load ~/Library/LaunchAgents/com.meru.workspace-mcp.plist
   ```

3. Check it with the `curl` line from Step 8. The log is in
   `~/.config/workspace-mcp/server.log`.

To stop it for good: `launchctl unload ~/Library/LaunchAgents/com.meru.workspace-mcp.plist`.
On Linux, a `systemd --user` service that runs `start.sh` does the same job.

## When something goes wrong

| What you see | What it means | What to do |
| --- | --- | --- |
| `Error 403: access_denied` in the browser | you aren't a test user | Step 4: add the address you signed in with |
| `Error 400: redirect_uri_mismatch` | the client isn't a Desktop app | Step 5: create a new client of type **Desktop app**, and put its ID and secret in `start.sh` |
| `... API has not been used in project ... or it is disabled` | one API is off | Step 2: turn it on, wait a minute, ask again |
| `invalid_grant` or a new sign-in link after a week | the 7-day limit | Step 10 again, or publish the app |
| `address already in use` when the server starts | something else holds port 8000 | stop the other program, or add `export WORKSPACE_MCP_PORT=8001` to `start.sh` and change the `url` in the `google` entry of `~/.meru/config.toml` to `http://127.0.0.1:8001/mcp` |
| `meru tools` warns `google offers no such tool` | the server started without `--tool-tier extended` | fix the last line of `start.sh`, restart the server, and ask Meru a question that uses Google; that turn lists the tools again and the warning goes |
| `meru tools` shows `google` as `not connected` | the server isn't running | Step 8, or check the log from Step 11 |
| Meru finds a mail but can't read its attachment | the server saves attachments elsewhere | check `WORKSPACE_ATTACHMENT_DIR` in `start.sh`, restart the server; see [Read a mail's attachment](running.md#read-a-mails-attachment) |
| `command not found: uvx` | uv isn't on your `PATH` | Step 6; for launchd, add uv's folder to `PATH` in the plist |

To start over from sign-in, stop the server, delete
`~/.google_workspace_mcp/credentials/`, start it again and do Step 10.

## What Meru may do with your account

Meru only calls the Google tools listed under `allow` in the `google` entry of
`~/.meru/config.toml`. Sending mail and changing a calendar event ask you first.
[The google entry](running.md#the-google-entry) lists every tool and whether it
asks.
