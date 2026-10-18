# How do I let Meru read my Apple Notes?

On a Mac, Meru reads the notes in the Notes app through three built-in tools:
`search_notes` finds notes by their words, `list_notes` lists your folders and
newest notes, and `read_note` reads one note in full. They read the Notes app's
own database each time you ask, so a note you wrote a minute ago is there.
Meru keeps no copy of your notes, and it never changes one.

**Turn it on.** macOS lets `merud` read the Notes database only with Full Disk
Access. It never asks; it refuses until you grant it:

1. Open System Settings, then Privacy & Security, then Full Disk Access.
2. Click +, press ⌘⇧G, enter `~/.local/bin/merud` (or wherever your `merud`
   lives; `which merud` tells you), and click Open.
3. Make sure its switch is on. The list may show it as `merud` or `a.out`.

The next question about your notes works; you don't need to restart `merud`.
Without the grant, Meru answers with the same steps instead of guessing.

**Ask by name.** Say "Apple Notes" or "Notes app" in the question, such as
"search my Apple Notes for the garden plan" or "what's in the Notes app about
Lisbon?", so Meru offers the notes tools. "My notes" alone may mean the notes in
your indexed folders or an Obsidian vault.

**After each update, grant it again.** Meru's programs aren't signed by a
registered developer, so macOS ties the grant to the exact build. After every
Meru update, turn Full Disk Access for `merud` off and on again. If that doesn't
help, remove it with − and add it again with +.

**Turn it off.** Take `search_notes`, `list_notes` and `read_note` out of
`[builtin] tools` in `config.toml`, or turn off Full Disk Access for `merud`.

**If your config lists its tools.** A `config.toml` from before these tools came
keeps its own `[builtin] tools` list, which doesn't grow by itself. Add the three
names to it and restart `merud`:

```toml
[builtin]
tools = ["configure", "datetime", "about_meru", "remember", "write_file",
         "read_file", "list_folder", "grep", "search_files", "search_images",
         "view_image", "web_search", "web_fetch",
         "search_notes", "list_notes", "read_note"]
```

**What it can't do.** It doesn't create, edit or delete notes, read a locked
note, or read attachments and drawings. Search matches words, not meaning, and
your notes don't show up in the search of your files.

More detail: [ARCHITECTURE.md, Approving a tool call](../../ARCHITECTURE.md#approving-a-tool-call).
