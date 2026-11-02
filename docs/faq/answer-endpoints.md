# How do I answer with a model from another model server?

`[models] main` can name a model that runs on a model server other than
Meru's own Ollama: Ollama's OpenAI-style API, vLLM, llama.cpp's server,
LM Studio, or a second Ollama. The router, search and summaries stay on
the local Ollama. For now the server must run on this machine.

1. **Start the server** and note its address and the model's name. Ollama
   serves an OpenAI-style API at `http://127.0.0.1:11434/v1` with no setup.

2. **Add an entry** to `~/.meru/config.toml` and point `main` at it with `@`:

   ```toml
   [models]
   main = "@ollama-v1"

   [[endpoints]]
   name           = "ollama-v1"
   reach          = "local"
   api            = "openai"                    # or "anthropic", or "ollama"
   url            = "http://127.0.0.1:11434/v1"
   model          = "qwen3.6:35b-a3b-mxfp8"
   context_length = 32768                       # the most tokens it reads
   vision         = true                        # it can look at pictures
   ```

   For `openai` and `anthropic`, `url` ends at the API's version, such as
   `/v1`. Those APIs don't say how much a model reads or whether it sees
   pictures, so the entry does. Add `tools = false` for a model that can't
   call tools.

3. **If the server needs a key**, put the key in `~/.meru/secrets.toml` and
   name it in the entry ([Where do I put an API key?](secrets.md)):

   ```toml
   api_key = "secret:my_server_key"
   ```

4. **Restart `merud`**, then ask `meru "which model are you using?"`.
   `merud.log` has an `answer endpoint` line for each entry, or a warning
   naming what to fix.

A model set can name an endpoint too, as `main = "@ollama-v1"`, so `/model`
and Settings switch to it like any other set. A switch sends the endpoint
nothing until you ask a question.

**Why only this machine?** A question carries your recent turns, memories and
file excerpts. Meru will ask before a chat sends anything to another machine;
until that prompt exists, config refuses `reach = "near-edge"` and
`reach = "remote"`. See [the design](../design/model-router.md).
