# How do I add, change or turn off a skill?

A skill is a Markdown file of instructions for one kind of task. Meru ships
four: `writing`, `explainer`, `web-research` and `file-research`. For each
question, the fast model picks at most two skills, and only their instructions
join the prompt.

```sh
meru skills list              # each skill, marked [built-in] or [edited]
meru skills show writing      # print its SKILL.md
```

**Add your own.** Make a folder under `~/.meru/skills/` named for the skill,
with a `SKILL.md` inside:

```markdown
---
name: meeting-notes
description: Turn rough meeting notes into decisions and action items. Use when
  asked to tidy up or summarize notes from a meeting.
---

List the decisions first, then each action item with its owner and date.
```

The name takes lowercase letters and digits joined by `-`, and must match the
folder. The description is what the fast model reads when it picks, so say
when to use the skill. `merud` notices it on the next question; no restart.
If it doesn't load, `meru skills list` says why under `Skipped:`.

**Give a skill tools.** An `allowed-tools: web_search, web_fetch` line in the
front matter hands those tools to any question that picks the skill. It
brings only tools your config already allows.

**Change one** by editing its `SKILL.md`. `merud` never overwrites your copy,
even after an upgrade. To take the shipped version back:

```sh
meru skills reset --yes web-research
```

**Turn one off** in the desktop app (Settings, Skills), with `/skills` in
`meru chat`, or in `config.toml` followed by a restart:

```toml
[skills]
disabled = ["explainer"]
```

**Check which skill a question used.** The route badge in `meru chat` names
it, such as `direct · 0.91 · writing`.

More detail: [running.md, Skills](../running.md#skills).
