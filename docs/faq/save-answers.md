# How do I save an answer, a chat or a file Meru makes?

Everything Meru writes goes under `~/meru-output/`, and it asks you before each
file.

| To save | Desktop app | `meru chat` | Lands in |
| --- | --- | --- | --- |
| the newest answer | Save to a note | `/save` | `~/meru-output/notes/` |
| the whole chat | Share as file | `/save chat` | `~/meru-output/chats/` |
| one code block | Copy on the block | `/copy 1`, or Ctrl-Y for the last one | the clipboard |

**Ask for a file** in so many words, such as "make an explainer page about how
DNS works and save it". The model writes it with `write_file`, which never
writes outside `~/meru-output`, never replaces a file without asking, and caps
a file at 1 MiB.

**One-shot answers** go wherever you send them:

```sh
meru "draft a note to Sam about the garden plan" > ~/notes/sam.md
```

**Every conversation is kept anyway**, as a transcript under
`~/.meru/sessions/YYYY/MM/`. Reopen one with `/chats` in `meru chat`, or from the
list on the left of the desktop app.

**Change the folder, or stop the prompt,** in `config.toml`, then restart
`merud`:

```toml
[skills]
output_dir = "~/meru-output"

[builtin]
confirm = ["write_file"]   # take write_file out to stop the prompt
```
