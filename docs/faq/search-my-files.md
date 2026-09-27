# How do I make Meru search my files?

Meru searches only the folders you name, and none by default.

**Add a folder** in any of three ways:

- In the desktop app: Settings, Folders, then pick the folder. Meru indexes it
  at once.
- In `meru chat`: `/folders add ~/notes`.
- In `~/.meru/config.toml`, then restart `merud`:

  ```toml
  [index]
  folders = ["~/notes", "~/repos/meru"]
  ```

The first scan of a big folder takes a while, because every chunk goes through
the embedding model. After that, `merud` watches the folders and re-reads a file
about half a second after you save it.

**Check what it holds:**

```sh
meru index -status
```

```text
Folders:    ~/notes
Index:      12 files, 87 chunks, 87 vectors
Scanning:   no
```

**Ask about your files.** An answer that used them cites each file by number:

```text
$ meru "when does the garden project sow tomatoes?"
The garden project sows tomatoes on 12 April [1].

Sources:
[1] ~/notes/garden.md, "Planting", lines 3–5
```

To keep a question on your files alone, pick My files under the box in the
desktop app, or type `/scope files` in `meru chat`.

**Rescan by hand:**

```sh
meru index               # every folder
meru index ~/notes/work  # one folder or file inside a listed folder
```

**Remove a folder** in Settings, Folders, with `d` twice on it in `/folders`,
or by taking it out of `[index] folders` and restarting `merud`.

**Start over.** `~/.meru/meru.db` holds nothing you can't rebuild. Stop
`merud`, delete the file and start `merud`; it indexes everything again.

A file that never shows up: see [file-not-found.md](file-not-found.md).

More detail: [running.md, Index your files](../running.md#7-index-your-files).
