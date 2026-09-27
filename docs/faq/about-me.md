# How do I tell Meru about myself?

Meru puts what it knows about you in every prompt: your name, email, work,
city and how you like answers. Tell it once:

```sh
meru setup user
```

It asks one question at a time, and Enter skips any of them. Each answer
becomes a memory: a Markdown file under `~/.meru/memory/me/` or
`~/.meru/memory/preferences/` that you can read and edit.

Your email matters most if you use `google`: every Google tool takes your
address, and with it in your profile the model doesn't have to search for it.

**Other ways to add a memory:**

- In chat: "remember that I work on the registry team". The model saves it with
  its `remember` tool.
- In `meru chat`: `/me add I grow tomatoes`, or `/me prefer short answers`.
- In the desktop app: Settings, About you.
- From the terminal: `meru memory add me I have two kids`.

**See or forget memories:**

```sh
meru memory list                         # every memory, grouped by kind
meru memory list me                      # one kind
meru memory forget me/i-have-two-kids.md
```

In `meru chat`, `/me` lists them and `d` twice forgets the marked one. `/used`
shows which memories an answer drew on. In the desktop app, the side panel
beside an answer has a Forget button on each.

More detail: [running.md, Tell Meru about you](../running.md#tell-meru-about-you).
