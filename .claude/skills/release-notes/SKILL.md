---
name: release-notes
description: "Write the release notes for a new Meru version. Gathers the commits, pull requests, closed issues and upgrade steps since a previous release, writes docs/release-notes/vX.Y.Z.md, and opens a pull request for it. make release then uses that file as the GitHub release's text. Asks the user to confirm the base version to compare against."
license: Apache-2.0
metadata:
  source: "Adapted from the release-notes skill in agentic-community/mcp-gateway-registry (Apache-2.0)"
  version: "1.0"
---

# Release notes

Use this skill when the user wants release notes for a new version of Meru. It
gathers every change since an earlier release, writes the notes in the format
below, and opens a pull request. It doesn't tag or publish: `make release`
does that after the pull request merges, and it uses the notes file as the
GitHub release's text ([docs/releasing.md](../../../docs/releasing.md)).

All prose follows the `writing` skill. The notes are for people who run Meru
on their own Mac: say what changed for them and what they do to upgrade, in
plain words.

## Input

A version tag in Meru's form, `vX.Y.Z`, such as `v0.4.7`. Meru's tags keep the
`v`, since `make release` refuses any other shape. If the user gives `0.4.7`,
add the `v` and say so.

## Output

- `docs/release-notes/vX.Y.Z.md`, one file per release, named like its tag.
- A pull request that adds it. Once it merges, `make release VERSION=vX.Y.Z`
  cuts the release with these notes.

For a release already published, the skill also updates the GitHub release's
text to the merged file (Step 7).

## Workflow

### Step 0: Check that main is ready (gate)

Before writing anything, confirm the code the release will ship passes its
checks:

1. `git fetch origin` and check that CI passed on the newest commit of `main`:
   `gh run list --branch main --limit 5`. Any failed run on that commit stops
   the release; report it and help fix it first.
2. Ask the user, with AskUserQuestion, whether `make check` passed on this
   commit. Offer "Yes", "Run it now" (Recommended) and "Skip it". A failing
   `make check` stops the release. If they skip it, warn once that the notes
   describe untested code, and go on only if they confirm.

`make release` runs `make check` again before it builds, so this gate catches a
failure before the notes pull request, not instead of the release's own check.

### Step 1: Settle the version

1. Take the version from the user, or ask for it.
2. Put it in `vX.Y.Z` form.
3. Check that no notes file has it (`ls docs/release-notes/`). Check the tag
   with `git tag -l vX.Y.Z`: for a new release it must not exist yet. If it
   does, ask whether they want notes for that published release (Step 7) or a
   new version.

### Step 2: Confirm the base version

The notes cover the changes since an earlier release.

1. List the tags, newest first:
   ```bash
   git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'
   ```
2. Ask the user, with AskUserQuestion, which release to compare against. Offer
   the newest tag before the new version as the recommended option, and the two
   or three before it as others: the user may want notes that span releases.

### Step 3: Gather the changes

Run these side by side. `{base}` is the base tag; `{head}` is `origin/main` for
a new release, or the new tag for a published one.

```bash
# Every commit, and the merge commits that name pull requests
git log {base}..{head} --oneline
git log {base}..{head} --oneline --no-merges
git log {base}..{head} --oneline --grep="Merge pull request"

# Each merged pull request: title, milestone, the issues it closes
for pr in $(git log {base}..{head} --oneline --grep="Merge pull request" | grep -oE "#[0-9]+" | tr -d '#' | sort -u); do
  gh pr view $pr --json number,title,milestone,closingIssuesReferences \
    --jq '"\(.number) | \(.title) | \(.milestone.title // "none") | closes: \(.closingIssuesReferences | map("#\(.number)") | join(","))"'
done

# Issues closed since the base tag, including any closed by hand
BASE_DATE=$(git log -1 --format=%cI {base})
gh issue list --state closed --limit 200 --json number,title,closedAt \
  --jq ".[] | select(.closedAt >= \"$BASE_DATE\") | \"\(.number) | \(.title)\""

# Contributors: authors on main, and each pull request's commit authors
git log {base}..{head} --format="%aN" | sort | uniq -c | sort -rn
for pr in $(git log {base}..{head} --oneline --grep="Merge pull request" | grep -oE "#[0-9]+" | tr -d '#' | sort -u); do
  gh pr view $pr --json number,author,commits \
    --jq '"PR #\(.number) | opener: \(.author.login) | authors: \([.commits[].authors[].name] | unique | join(", "))"'
done

# What an upgrade touches
git diff {base}..{head} -- internal/config/template.toml    # config keys added, changed or removed
git diff {base}..{head} -- internal/config/recommend.go     # the model each Mac gets
git diff {base}..{head} --stat -- internal/rpc/ internal/transcript/ internal/store/
git diff {base}..{head} -- go.mod                            # dependency changes
```

### Step 4: Sort the changes

1. **Major features**: new things a user can do or see. Each gets its own
   section with a short description and its pull request.
2. **Breaking changes**: anything that needs the user to act when they
   upgrade. For Meru, look for:
   - a config key removed or renamed in `template.toml`: `merud` refuses to
     start on an unknown key, so an old `config.toml` stops it;
   - a new embedding model, which makes `merud` re-embed every indexed file;
   - a change to the socket protocol in `internal/rpc` that an older `meru`,
     `meru chat` or Meru.app can't speak, so every client must update with
     `merud`;
   - a change to the transcript or database format in `internal/transcript` or
     `internal/store`;
   - a change of default model in `recommend.go` or the profiles, which changes
     what a new install downloads.
3. **New config keys**: each key added to `template.toml`, with its default and
   what it does. This takes the place of the environment variables table in the
   original skill.
4. **Model changes**: when `recommend.go` or a profile changed, give the new
   table of answer model by Mac memory.
5. **Bug fixes**, **security fixes**, **documentation**, and **dependency
   updates** from `go.mod` and Dependabot.
6. **Closed issues**: from each pull request's `closingIssuesReferences`, plus
   issues closed by hand in the window. One row per issue.
7. **Contributors**: the union of the authors on main and each pull request's
   commit authors. Take every GitHub login from a real pull request
   (`.author.login`, or `.commits[].authors[].login` for a co-author), and check
   a doubtful one with `gh api users/<login>`, where a 404 means a wrong guess.
   Never build a login from a display name: the owner, Amit Arora, is
   `aarora79`.

### Step 5: Write the notes

Write `docs/release-notes/vX.Y.Z.md` in this shape. Leave out a section that has
nothing in it, except Breaking changes, which always says whether there are any.

```markdown
# Release vX.Y.Z - {short title naming the main change}

**{Month} {Year}**

{One or two sentences: what this release gives someone who runs Meru.}

---

## Upgrading from {base}

### Breaking changes

{Each change, what breaks, and what to do. With none: "There are no breaking
changes in this release."}

### New config keys

| Key | Default | What it does |
|-----|---------|--------------|
| `[section] key` | {default} | {description} |

{Say that a config.toml without the key gets the default, when that is so.}

### Upgrade instructions

**The one-line installer.** Run it again; it keeps your config.toml:

```sh
curl -fsSL https://github.com/aarora79/meru/releases/latest/download/install.sh | bash
```

**The Mac installer.** [Download the disk image](https://github.com/aarora79/meru/releases/latest/download/Meru-macos-arm64.dmg),
open it, and run "Install Meru.app". It leaves your config and memories alone.

**From source.**

```sh
cd meru
git pull origin main
git checkout vX.Y.Z
go install ./cmd/merud ./cmd/meru
launchctl kickstart -k gui/$(id -u)/com.meru.merud   # or: pkill merud; merud &
```

{Add any step this release needs: a new key to set, a model to pull, a
re-index.}

---

## Major features

### {Feature}

{What it does and why it matters, with key points as bullets.}

[PR #{n}](https://github.com/aarora79/meru/pull/{n})

---

## What's new

### {Category}
- {Change} (#{n})

---

## Bug fixes

- {Fix} (#{n})

---

## Closed issues

| Issue | Title | Closed by |
|-------|-------|-----------|
| #{n} | {title} | PR #{n}, or "by hand" |

{With none: "No issues were closed in this release."}

---

## Pull requests included

| PR | Title |
|----|-------|
| #{n} | {title} |

{Every merged pull request in the window, newest first.}

---

## Contributors

- **{Full name}** ([@{login}](https://github.com/{login}))

---

**Full changelog:** [{base}...vX.Y.Z](https://github.com/aarora79/meru/compare/{base}...vX.Y.Z)
```

### Step 6: Show the draft

Tell the user where the file is, and give a short count: major features, pull
requests, bug fixes, closed issues, breaking changes and contributors. Ask them
to read it and confirm, or say what to change.

### Step 7: Pull request, then release

Once the user confirms:

1. Add a line for the version at the top of the list in
   `docs/release-notes/README.md`.
2. Commit on a branch and open a pull request:
   ```bash
   git checkout -b release-notes-vX.Y.Z
   git add docs/release-notes/
   git commit -m "Add the vX.Y.Z release notes"
   git push -u origin release-notes-vX.Y.Z
   gh pr create --title "Add the vX.Y.Z release notes" --body "..."
   ```
3. Merge it only when the user says to, after CI passes.
4. **A new release:** tell the user the notes are on `main` and the release is
   ready: `make release VERSION=vX.Y.Z`, which tags, publishes and uses the
   file as the release's text. Don't run it unless they ask.
5. **A release already published:** after the merge, make the GitHub release's
   text match the file:
   ```bash
   gh release edit vX.Y.Z --repo aarora79/meru --notes-file docs/release-notes/vX.Y.Z.md
   ```

## Rules

- **Run the Step 0 gate first.** Failed CI or a failed `make check` stops the
  release notes; only the user's explicit choice skips `make check`.
- **Always confirm the base version** with the user (Step 2).
- **Never tag, move a tag or publish** from this skill. `make release` owns
  tags and releases, and a published tag stays where it is.
- **Breaking changes come first** in the upgrade section. When none, say so.
- **Check `template.toml`** for every release: a removed or renamed key stops
  `merud` from starting on an old config.
- **Follow the repo's writing rules** (AGENTS.md): the `writing` skill, no AI
  or assistant credit anywhere, no emojis, no cloud vendor names, nothing
  offered for sale, and no personal data from the owner's files or benchmark.
- **Publish benchmark numbers only as totals**, as `docs/benchmarks/results.md`
  does; never a task's text.
- **Never build a GitHub login from a display name.** Take it from a pull
  request, and check a doubtful one with `gh api users/<login>`.

## Example

```
User: write the release notes for v0.4.7
Claude: [gate] CI passed on main; ran make check: pass.
        Compare against v0.4.6 (Recommended), v0.4.5 or v0.4.4?
User: v0.4.6
Claude: [gathers changes, writes docs/release-notes/v0.4.7.md]
        1 major feature, 1 pull request, no breaking changes, 1 contributor.
        Please review.
```
