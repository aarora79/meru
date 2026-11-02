# How do I answer with a model from another model server?

`[models] main` can name a model that runs on a model server other than
Meru's own Ollama: Ollama's OpenAI-style API, vLLM, llama.cpp's server or
LM Studio on this computer, a GPU box on your network, or a hosted service
you have a key for. The router, search and summaries always stay on the
local Ollama.

1. **Note the server's address and the model's name.** Ollama serves an
   OpenAI-style API at `http://127.0.0.1:11434/v1` with no setup.

2. **Add an entry** to `~/.meru/config.toml` and point `main` at it with `@`:

   ```toml
   [models]
   main = "@lab"

   [[endpoints]]
   name           = "lab"
   reach          = "near-edge"                 # see below
   api            = "openai"                    # or "anthropic", or "ollama"
   url            = "http://10.0.0.5:8000/v1"
   model          = "qwen3.6:35b-a3b-mxfp8"
   context_length = 32768                       # the most tokens it reads
   vision         = true                        # it can look at pictures
   ```

   `reach` says how far the server is:

   | reach | where | rule |
   | --- | --- | --- |
   | `local` | this computer | the URL must be on loopback |
   | `near-edge` | your own network | every address Meru connects to must be private |
   | `remote` | the internet | the URL must use https |

   For `openai` and `anthropic`, `url` ends at the API's version, such as
   `/v1`. Those APIs don't say how much a model reads or whether it sees
   pictures, so the entry does. Add `tools = false` for a model that can't
   call tools.

3. **If the server needs a key**, put the key in `~/.meru/secrets.toml` and
   name it in the entry ([Where do I put an API key?](secrets.md)):

   ```toml
   api_key = "secret:lab_key"
   ```

4. **Restart `merud`**, then ask `meru "which model are you using?"`.
   `merud.log` has an `answer endpoint` line for each entry, or a warning
   naming what to fix.

**What Meru asks.** Before a question goes to a `near-edge` or `remote`
endpoint, Meru asks, in the app, in `meru chat` and in `meru`:

> Send this question to @lab? It carries your question, 2 earlier turns of
> this chat, your profile and the memories Meru recalls for it, excerpts from
> your files, and the results of any tools this turn runs.
> **This time** · **This chat** · **No**

"This chat" lasts until `merud` restarts; Meru never remembers a yes beyond
that. No answers on your computer with the fast model, and says so under the
answer. An incognito chat, a scheduled job and `meru run --json` never ask:
they always answer on your computer. An answer from an endpoint says where it
came from, such as "Answered by @lab (near-edge)".

A model set can name an endpoint too, as `main = "@lab"`, so `/model` and
Settings switch to it like any other set. A switch sends the endpoint nothing
until you ask a question.

See [the design](../design/model-router.md) for how it works.
