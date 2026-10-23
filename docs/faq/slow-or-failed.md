# How do I find out why an answer was slow or failed?

Start with the log, `~/.meru/merud.log`. Each question writes one `turn` line:

```text
level=INFO msg=turn session=2026-09-24T020539-1f53 route=tools source=tui outcome=ok ms=796 ttft_ms=594 tokens_in=85 tokens_out=142 trace_id=be9e7312…
```

**What `outcome=` means:**

| Outcome | What happened | What to do |
| --- | --- | --- |
| `ok` | it answered | nothing |
| `timeout` | the question ran past `[agent] turn_timeout` | press Continue, or raise it, such as `"10m"` |
| `cut_off` | the model hit `[agent] max_output_tokens` | press Continue, or raise it |
| `window_full` | the tool results filled `[ollama] context_length`, so Meru stopped offering tools and answered from what it had | press Continue; if it keeps happening, raise `context_length`, such as `65536`, if your Mac has the memory |
| `gave_up` | the model only called tools, or wrote nothing twice | ask in other words, or `/new` in `meru chat` |
| `bad_output` | Ollama couldn't read the model's tool call, twice | ask again; if it keeps happening, update Ollama or switch models |

**An answer stopped short.** Under an answer that ends `cut_off` or `timeout`,
Meru.app shows a Continue button and `meru chat` a line pointing at
`/continue`. Continue picks the answer up where it stopped. Meru hands the
model your question and the tool calls it already made, with the start of
each result, so it doesn't read the same pages again. Only the chat's newest
answer can continue.

A turn whose tool results fill most of the model's context window
(`[ollama] context_length`, 32,768 tokens by default) gets its oldest results
shortened first, so this happens less often. With debug logging, the line
"prompt near the context window" shows when that ran.

**See where the time went.** Restart `merud` with `-v`, or set
`[log] level = "debug"`, and ask again. Each stage then writes a line: the
route, the prompt size, each call to Ollama with its load, prompt and answer
times, and tokens per second. Every line of one question shares its
`trace_id`:

```sh
grep be9e7312 ~/.meru/merud.log
```

A large `thinking_chunks` count means the model spent the wait reasoning before
its first word. Thinking is off unless `[models] think = true`, or a model set
you switched to leaves `think` out; set `think = false` on that set.

**The first answer is slow** because Ollama is still loading the answer model.
`merud` loads it in the background at startup; the log says
`answer model warm` when it's done.

**Common errors:**

| You see | Do this |
| --- | --- |
| `connect to merud … (is merud running?)` | start `merud` |
| `merud` can't reach Ollama | open the Ollama app, then `curl http://127.0.0.1:11434/api/version` |
| `model … not found` | `ollama pull` the model it names |
| `merud` refuses a config value | fix the key it names; `meru config template` shows every key and its default |
| `secrets … other users can read it` | `chmod 600 ~/.meru/secrets.toml` |

The log never holds your questions or answers unless you set
`capture_content = true` under `[observability]`.

More detail: [running.md, Troubleshooting](../running.md#12-troubleshooting).
