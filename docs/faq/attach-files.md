# How do I give Meru a file or an image?

**In the desktop app**, click the paperclip, or drop files from Finder onto the
chat. **In `meru chat`**, type `/attach ~/plans/garden-plan.pdf`. A question
takes five files at most. One-shot `meru "..."` can't attach.

**A file** is copied into `~/meru-output/uploads/`, and the question gains a
line such as "Read this file: ~/meru-output/uploads/garden-plan.pdf". The model
reads it with `read_file`. That tool stays off until Meru indexes at least one
folder, so add one first (see [search-my-files.md](search-my-files.md)).

Meru refuses a folder, a symbolic link, a file over 50 MiB, a file it can't
read such as an archive, and a file whose name looks like it holds a key, such
as `.env` or `id_ed25519`. The line under the box names each one and why.

**An image** (PNG, JPEG, GIF or WebP, up to 20 MiB) goes to the answer model
with that one question. The model must be able to see: check with

```sh
ollama show <model>
```

and look for `vision` under Capabilities. If it's missing, Meru sends nothing
and says so; switch models (see [change-model.md](change-model.md)).

**A file already in an indexed folder** needs no attaching. Name it in the
question: "read ~/notes/garden-plan.md and list the dates".

Clear `~/meru-output/uploads/` whenever you like.
