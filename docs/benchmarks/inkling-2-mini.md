# Inkling 2 Mini as the answer model

> [!IMPORTANT]
> These numbers come from the author's private dataset: questions about the author's own files,
> notes, mail and calendar, which aren't published. Your numbers on your own data will differ.
> [README.md](README.md) says how to build a dataset of your own.

On 22 November 2026 we ran Thinking Machines' Inkling 2 Mini (`inkling-2-mini`, 20 GB) as Meru's
answer model, against `qwen3.6:35b`, the answer model a 48 GB Mac gets, on the same code the same
day. The machine was a Mac Studio with an M4 Max and 64 GB, running Ollama 0.40.17, with
MiniCPM5-2B as the router and `embeddinggemma-2:440m` for search.

Inkling 2 Mini is a looped mixture of experts: 26.4B parameters, 5.4B at work on each token, with
its middle layers run three times. Ollama runs it from 0.40.17, on Apple silicon only.

The dataset has 68 graded tasks, more than the 50 of [the September results](results.md): it adds
images and tables. Google's sign-in for the test account ran out part way through the day, so 11
mail and calendar tasks got "please sign in" in some runs and not others. The tables below leave
those 11 out of every run, which leaves 57 tasks a run.

## Result

`qwen3.6:35b` stays the better answer model. Inkling 2 Mini passed about as many tasks overall, and
read files, notes and pictures better, but it stopped after the first of several tool calls, and
it took six times as long. With thinking on, it passed fewer tasks and took longer still. Meru
lists it under Settings, Models as a model you can pick.

| Model set | Answer model | Thinking | Runs | Passed | Per run | Time per task, p50 | First token, p50 |
| --- | --- | --- | ---: | ---: | --- | ---: | ---: |
| qwen-4bit | `qwen3.6:35b` | off | 3 | 140 of 171 (82%) | 47, 47, 46 | 11.8 s | 6.8 s |
| inkling | `inkling-2-mini` | off | 3 | 137 of 171 (80%) | 45, 47, 45 | 71.3 s | 68.9 s |
| inkling-think | `inkling-2-mini` | on | 1 | 41 of 57 (72%) | 41 | 83.9 s | 76.6 s |

## Pass rate by category

| Category | qwen-4bit | inkling | inkling-think |
| --- | ---: | ---: | ---: |
| t0-direct | 23/24 | 24/24 | 8/8 |
| t1-retrieval | 24/27 | 27/27 | 7/9 |
| t2-one-tool | 27/30 | 26/30 | 9/10 |
| t3-multi-tool | 10/18 | 2/18 | 0/6 |
| t4-multi-turn | 12/12 | 12/12 | 4/4 |
| t5-honesty | 6/6 | 6/6 | 1/2 |
| t6-images | 23/24 | 24/24 | 6/8 |
| t7-tables | 15/30 | 16/30 | 6/10 |

## Speed

| Model set | First token, p50 | First token, p95 | Last token, p50 | Last token, p95 | Tokens a second | Tokens in, p50 | Tokens out, p50 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| qwen-4bit | 6.8 s | 18.5 s | 11.8 s | 42.6 s | 87 | 28380 | 157 |
| inkling | 68.9 s | 122.2 s | 71.3 s | 137.6 s | 35 | 35835 | 307 |
| inkling-think | 76.6 s | 184.2 s | 83.9 s | 300.0 s | 36 | 38403 | 493 |

## What we saw

- **Several tool calls.** On a task that needs a search and then a read, Inkling 2 Mini made the
  first call and then answered that it didn't have enough. Before the sign-in ran out, a mail
  search returned message IDs,
  and it wrote "the search returned 10 matching messages, but none of their contents" instead of
  reading them. With Obsidian it listed the vaults and asked whether to search. It never made
  anything up, and it never finished the task either. Thinking on didn't change this. The model
  card warns that the model follows detailed instructions less well when it uses tools.
- **Files, notes and pictures.** It passed every retrieval, image and multi-turn task with
  thinking off, and two tasks every time that `qwen3.6:35b` passed once in three, and its
  answers quoted more of what it read.
- **Speed.** It read about a quarter more of the prompt and wrote about twice as many tokens, at
  35 tokens a second against 87. With thinking off, it still wrote about 50 tokens before
  "Canberra.", so Meru's `think = false` may leave it some thinking.

## Trying it

```sh
ollama pull inkling-2-mini
```

Then add a model set to `~/.meru/config.toml` and switch to it with `/model` in `meru chat`, or
pick it under Settings, Models in the desktop app:

```toml
[[models.sets]]
name  = "inkling"
main  = "inkling-2-mini"
think = false
```
