# How do I change the model that answers?

The answer model, `[models] main`, writes every answer and picks the tools. The
`lite` profile uses MiniCPM5-2B, which is fast but makes things up more often
than a larger model.

1. **Pull the model** and try it in a terminal:

   ```sh
   ollama pull qwen3.6:35b
   ollama run qwen3.6:35b      # type /bye to leave
   ```

2. **Switch to it.** Pick one:
   - Desktop app: Settings, Models, "Use for answers" on its card. No restart.
   - `config.toml`, then restart `merud`:

     ```toml
     [models]
     main = "qwen3.6:35b"
     ```

3. **Check it** with `meru "which model are you using?"`. The model calls
   `about_meru` and names itself.

**Compare several models** with model sets. Name each in `config.toml` and
restart `merud` once:

```toml
[[models.sets]]
name  = "qwen-moe"
main  = "qwen3.6:35b-a3b-mxfp8"
think = false

[[models.sets]]
name  = "gemma-moe"
main  = "gemma4:26b-mxfp8"
think = false
```

```sh
meru model                  # the sets, the one in use, and what Ollama holds
meru model use gemma-moe    # switch until merud stops
meru model save             # keep the models in use as the default
```

In `meru chat`, `/model gemma-moe` and `/model save` do the same, and
`/usage by model` compares the sets you tried.

**Before you pick one**, check two things with `ollama show <model>`:

- `tools` under Capabilities. Without it, Meru can't read your mail, notes,
  the web or your files, and says so under each answer.
- `vision`, if you want to attach images.

**A larger model needs more context.** On macOS, run
`launchctl setenv OLLAMA_CONTEXT_LENGTH 32768`, then quit and start Ollama. It
lasts until the Mac restarts.

**Changing the embedding model** (`[models] embed`) makes `merud` embed every
file again: set it, `ollama pull` it, and restart `merud`.

More detail: [running.md, Models we tried for answers](../running.md#models-we-tried-for-answers).
