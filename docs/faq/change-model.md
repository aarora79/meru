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

**Context.** `merud` sizes the answer model's context window for you: as many
tokens as the model can read, cut to what its cache fits in an eighth of your
Mac's memory, and never less than `[ollama] context_length`, 32,768 by default.
`qwen3.6:35b` gets its full 262,144 tokens on a 64 GB Mac. `merud.log` names
the window at startup and on each switch: `answer model window`. No Ollama
setting is needed.

**Let easy turns use the fast model.** With this in `config.toml`, a turn
the router is sure needs no files, no tools and no picture, and that asks for
no facts, such as a greeting, thanks or text to reword, gets the fast model, which starts writing sooner; everything else gets the answer
model, and a fast answer that ends early is asked again of it:

```toml
[router]
answers    = "routed"
easy_above = 0.8      # how sure the router must be
```

Each answer names the model that wrote it. `"main"`, the default, sends every
question to the answer model.

**A larger model on request.** `[models] large` names a bigger model, on this
computer or an [endpoint](answer-endpoints.md). It answers only when you ask:
"Ask the large model" under an answer that ended early, `/large` in `meru
chat`, or `meru -large "…"`.

**Changing the embedding model** (`[models] embed`) makes `merud` embed every
file again: set it, `ollama pull` it, and restart `merud`.

More detail: [running.md, Models we tried for answers](../running.md#models-we-tried-for-answers).
