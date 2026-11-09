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
a file at 1 MiB, or 40 MB for a page with its pictures.

**Make a card or a poster from a photo** with an HTML page. Say you want a
birthday card from a family photo:

1. Give Meru the photo. Attach it (see [attach-files.md](attach-files.md)),
   which copies it into `~/meru-output/uploads/`, or ask Meru to download it
   from an email, which saves it in `~/meru-output/attachments/`.
2. Ask for the card: "make a birthday card as an HTML page, with the photo I
   just gave you at the top".
3. The model writes the page with `write_file` and names the photo in an
   `<img>` tag. `write_file` puts the photo itself into the page, and its
   result says "Put 1 picture into the page". The answer links the page.
4. Open the page in a browser, print it, or send the file. It holds the photo,
   so it shows on a phone too.

The model can't draw or edit a picture's pixels. It lays out your photos with
HTML and CSS, so ask for a frame, a caption or a collage, not for a changed
photo. Only a photo inside `~/meru-output` goes into the page; one elsewhere,
such as in `~/Pictures`, needs attaching first. When the page doesn't show the
photo, ask Meru what `write_file` said: its result names each picture it left
out and why.

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
