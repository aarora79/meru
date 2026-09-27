# Why doesn't Meru find one of my files?

Work through these in order.

1. **Is its folder listed?** `meru index -status` prints the folders. A file
   outside all of them never gets in.
2. **Does Meru skip that kind of file?** It reads Markdown, plain text, HTML,
   PDF and source code. It skips:
   - symbolic links, and hidden files and folders;
   - secret files such as `.env`, `*.pem` and `id_rsa`;
   - build folders such as `node_modules` and `.venv`;
   - images, audio, video, archives and binaries;
   - files over 5 MB (`[index] max_file_mb`);
   - anything the folder's `.gitignore` or a `.meruignore` names.
3. **Find the reason in the log.** Stop `merud`, start it with `merud -v`, and
   search the log for the file's name:

   ```sh
   grep garden-plan ~/.meru/merud.log
   ```

   Each skipped path gets a line with its reason, such as `secret`, `ignored`
   or `too-large`.
4. **Rescan it:** `meru index ~/notes/garden-plan.md`.

**Bring back a file `.gitignore` leaves out** with a `.meruignore` in that
folder. It uses `.gitignore` syntax, and `!` brings a name back:

```text
# .meruignore
drafts/
*.log
!notes.log
```

**The file is indexed, but the answer ignores it.** The question may have
gone straight to the model. Pick My files in the desktop app, type
`/scope files` in `meru chat`, or name the folder in the question, such as "in
my notes, …". For a question that needs a whole file, ask Meru to read it, such
as "read ~/notes/garden-plan.md and list the dates"; the model then calls
`read_file`.

**On Linux, changes stop arriving.** Linux caps how many folders one user can
watch. Raise `fs.inotify.max_user_watches`; [running.md](../running.md#7-index-your-files)
has the commands.
