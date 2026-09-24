# skills

**Code:** `internal/skills/` (`doc.go`, `frontmatter.go`, `skills.go`, `builtin.go`,
`builtin/`)
**Milestone:** v0.4
**Architecture:** [Skills](../../ARCHITECTURE.md#skills) and
[Built-in skills](../../ARCHITECTURE.md#built-in-skills)

## What it does

A skill is a folder under `~/.meru/skills/` holding one `SKILL.md` file: a short
header with a `name` and a `description`, then Markdown instructions. This package
reads those folders into a `Registry`. `merud` puts each skill's name and description
in the system prompt, and reads the instructions only when a turn picks the skill.
The package also carries Meru's two built-in skills, `writing` and `explainer`,
inside the binary, and copies them to disk on first run.

## The picture

```mermaid
flowchart LR
    subgraph binary["merud binary"]
        embedded["builtin/writing/SKILL.md<br/>builtin/explainer/SKILL.md"]
    end
    embedded -- "InstallBuiltins<br/>(only if the folder is missing)" --> disk["~/.meru/skills/&lt;name&gt;/SKILL.md"]
    embedded -- "Reset(name)" --> disk
    you["your own skills"] --> disk
    disk -- "Load" --> reg["Registry"]
    reg -- "List: name + description" --> prompt["system prompt"]
    reg -- "Body(name): read on demand" --> turn["the turn that picked the skill"]
```

## Walk through the code

### frontmatter.go

The header at the top of a `SKILL.md` is YAML, a text format for settings.
Full YAML is large, and skill headers use a small corner of it, so Meru reads that
corner by hand instead of adding a YAML library:

```go
fields, body, err := parseFrontmatter(data)
// fields["name"] == "explainer"
// fields["metadata.author"] == "aarora79"
```

`parseFrontmatter` checks that the file opens with a `---` line and finds the next
`---` line, which ends the header. Everything after it is the body. `parseFields`
then reads the header one `key: value` line at a time and collects any indented
lines below each key. What it does with those lines depends on the value:

| Header | Result |
| --- | --- |
| `description: one line` | the text as written |
| `description: starts here` plus indented lines | the lines joined with spaces |
| `description: >` plus indented lines | the lines folded into one paragraph |
| `description: \|` plus indented lines | the lines with their line breaks kept |
| `metadata:` plus indented `key: value` lines | flattened: `metadata.author`, `metadata.version` |
| `version: "1.5"` or `'1.5'` | the text without the quotes |
| `tools:` plus an indented list | the list lines kept as text |

It stops with an error that names the line for a missing or unclosed header, a key
with spaces in it, a missing space after `:`, a key that appears twice, or a tab used
for indentation (YAML forbids tabs). A strict parser means a typo shows up as a
warning at load time, not as a skill that quietly loses its description.

### skills.go

`Load(dir)` lists the folders in `dir` and calls `loadSkill` on each. `loadSkill`
reads `SKILL.md` through `readSkillFile`, which stops reading one byte past the
256 KiB limit:

```go
data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
if len(data) > maxFileBytes {
    return nil, "", fmt.Errorf("%s is over the %d KiB limit", fileName, maxFileBytes>>10)
}
```

Then it checks the header. The name must match the folder, be at most 64 bytes, and
use lowercase letters and digits joined by hyphens. That pattern can't hold `/`,
`..` or a space, so a skill name is always a safe folder name. The description must
be present and at most 1,024 bytes, and the body must not be empty.

A bad skill doesn't fail the load. `Load` records the reason and moves on, and
`Warnings()` hands the list back so `merud` can log it and `meru skills list` can
show it. One broken folder shouldn't take away every other skill.

The `Registry` never changes after `Load` returns, so goroutines can read it with no
lock. To pick up a new skill, call `Load` again and swap in the new registry.

- `List()` returns `[]Summary` (name and description), sorted by name so the system
  prompt reads the same on every run.
- `Get(name)` and `Has(name)` look a skill up. `Get` copies the `Extra` map, so a
  caller that changes it can't change the registry.
- `Body(name)` reads the file again and returns the instructions. Reading at use
  time means an edit shows up without a reload. If the edit renamed the skill,
  `Body` refuses and asks for a reload.

### builtin.go

The built-in skills sit in `internal/skills/builtin/<name>/SKILL.md`, and one line
puts them inside the binary:

```go
//go:embed builtin
var builtinFS embed.FS
```

The `//go:embed` line tells the compiler to copy the `builtin` folder into the
program. [go-basics/embed.md](go-basics/embed.md) explains it.

`InstallBuiltins(dir)` runs at first start. For each built-in it checks whether
`<dir>/<name>` exists (as a folder, a file or a link), and skips it if so. That rule
is "your copy wins" from ARCHITECTURE.md: Meru never overwrites a skill you edited.
When the folder is missing, `installOne` writes the files into a hidden temporary
folder and renames it into place. A crash halfway through leaves a hidden folder that
`Load` ignores, never a half-written skill that later runs would take for your copy.

`Reset(dir, name)` is for `meru skills reset`. It accepts only names Meru ships, so
`../writing` or `poster-making` fail with `ErrNotBuiltin`. It refuses when the skill
folder is a symbolic link, because writing through it would change files somewhere
else. It overwrites only the files Meru ships, so a notes file you added stays.
Each file goes through `writeFileAtomic`, which writes a temporary file and renames it
over the old one, so a reader sees the old file or the new one, never half of each.

Folders get mode `0700` and files `0600`: only you can read them.

### The built-in files

`builtin/writing/SKILL.md` and `builtin/explainer/SKILL.md` are byte-for-byte copies
of `.claude/skills/writing/SKILL.md` and `.claude/skills/explainer/SKILL.md`, which
come from the owner's `my-ai-assets` repo. Nobody edits them here. To update one,
copy the new version into both places. `TestBuiltinsMatchRepo` fails when the two
copies differ.

The explainer skill tells the model to build a printable poster with the
`poster-making` skill. Meru doesn't ship `poster-making` (AGENTS.md keeps it a repo
tool), so inside Meru that step has no skill to load. The model should skip the
poster, and the skill's HTML page and Markdown twin still come out. The file stays
as copied; if the step causes trouble, fix it upstream in `my-ai-assets`.

## Go ideas used here

- **embed** — compiling files into the binary. More in
  [go-basics/embed.md](go-basics/embed.md).
- **Named results and deferred cleanup** — `installOne` and `writeFileAtomic` name
  their `err` result, and a deferred function removes the temporary file or folder
  when `err` is set. More in [go-basics/defer.md](go-basics/defer.md).
- **Errors you can test for** — `ErrNotFound` and `ErrNotBuiltin` are wrapped with
  `%w`, so callers use `errors.Is`. More in [go-basics/errors.md](go-basics/errors.md).
- **Table-driven tests** — `TestParseFrontmatter` and `TestLoad` list their cases in
  a slice and run each as a subtest. More in [go-basics/testing.md](go-basics/testing.md).

## Try it

```sh
go test -race ./internal/skills/...
go test -run TestBuiltinsMatchRepo -v ./internal/skills/
```

The second command proves the shipped skills match the repo copies.

## Why it's built this way

- **No YAML library.** A general YAML parser is thousands of lines and accepts far
  more than a skill header needs. The hand parser is about 260 lines with its comments, reads every
  skill in this repo, and rejects the rest with a line number.
- **The body loads on demand.** Keeping only names and descriptions in the prompt
  lets you add skills without growing every prompt (ARCHITECTURE.md, "Skills").
- **Warnings, not failure.** Skills are files you edit by hand. A typo in one should
  cost you that one skill, with a message saying why.
- **Copy on first run, never overwrite.** The simpler option, reading the built-ins
  straight from the binary, would leave you no way to edit them.
