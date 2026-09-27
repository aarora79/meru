# How do I let Meru read GitHub?

Meru reads GitHub through `gh`, GitHub's own command-line tool, declared as
local commands. `gh` keeps its login in your keychain, so Meru never holds a
GitHub token.

1. **Install `gh` and sign in once:**

   ```sh
   brew install gh
   gh auth login        # GitHub.com, HTTPS, log in with the browser
   gh auth status       # should say "Logged in to github.com"
   ```

2. **Find its path** with `command -v gh`. If `merud` runs under `launchd`, it
   won't find `gh` in `/opt/homebrew/bin` or `~/.local/bin`, so use the full
   path as the first element of each `argv`.

3. **Copy the commands.** `meru config template` prints six, commented out,
   under "GitHub with the gh CLI". Copy the ones you want into
   `~/.meru/config.toml` and delete the `# ` at the start of each line:

   | Tool | What the model gets |
   | --- | --- |
   | `cmd.gh-prs` | a repository's 20 newest pull requests |
   | `cmd.gh-pr` | one pull request with its text, reviews and comments |
   | `cmd.gh-issues` | a repository's 20 newest issues |
   | `cmd.gh-issue` | one issue with its text and comments |
   | `cmd.gh-runs` | the 10 latest GitHub Actions runs and how each ended |
   | `cmd.gh-repos` | up to 30 repositories of one user or organization |

4. **Restart `merud`** and check with `meru tools`.

Then ask:

```sh
meru "which pull requests are open in dana-reyes/garden-planner?"
meru "did the last CI run in dana-reyes/garden-planner pass?"
```

All six only read. To add a command that writes, such as commenting on an
issue, give it `confirm = true` so each call asks you first.
[running.md, GitHub](../running.md#github) has an example. Never declare
`gh api` with a path the model fills in: it could then reach any endpoint,
deletes included.

See also [allow-commands.md](allow-commands.md).
