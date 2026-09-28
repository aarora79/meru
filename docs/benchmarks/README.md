# Meru benchmark

> [!IMPORTANT]
> **These results come from the author's private data.** The 50 questions ask about the
> author's own files, notes, mail, calendar and repositories, and their answers quote them, so
> the dataset isn't published and never enters git. What you see here are charts, tables and
> summaries only. You can't rerun this dataset, and your numbers on your own data will differ.
> To measure Meru on your data, build a dataset of your own with the steps below, or ask the
> author by opening an issue on [GitHub](https://github.com/aarora79/meru/issues).

[results.md](results.md) holds the latest results: how many questions each model set got
right, by kind of question, and how fast it answered.

## What it measures

Meru can switch between named model sets (`meru model`), each with its own answer model. The
benchmark runs the same questions through each set and compares two things:

- **Accuracy:** the share of questions a set answers right. `meru check` grades each answer
  against ground truth written by hand: words the answer must hold, the tools it must call, the
  files it must cite, and for some questions what it must not claim.
- **Speed:** time to the answer's first token (TTFT), time to its last token (TTLT), the
  model's writing time per output token (TPOT), and the tokens read and written. The times count
  from when `merud` got the question, so they include routing, search and tool rounds.

Each set answers every question three times, because a local model doesn't give the same
answer each time. The results show the lowest and highest of the three runs next to the total.
Cost isn't measured: every model runs on the author's own machine.

The questions come in six kinds:

| Kind | What it tests | Example |
| --- | --- | --- |
| `t0-direct` | the model alone, and that it calls no tool it doesn't need | "Is 91 a prime number?" |
| `t1-retrieval` | search of the indexed folders, with the right file cited | "What do my notes say about the CAP theorem?" |
| `t2-one-tool` | picking one right tool and filling in its arguments: files, web search, Obsidian, Gmail, Calendar, a local command | "What's on my calendar next Saturday?" |
| `t3-multi-tool` | chaining tools in one turn: Gmail search, a mail attachment, Calendar, web search and fetch, Obsidian, `gh` | "Find the receipt my hotel emailed me, open its PDF, and tell me how long I had between checkout and my flight on my calendar." |
| `t4-multi-turn` | a follow-up question that leans on the one before | "And what year was it published?" |
| `t5-honesty` | a task no tool may do, where the answer must not claim it did | "Book a table for two tonight." |

## Build a dataset of your own

This needs no code: a text file of questions and a few commands.

1. **Set Meru up with your own data.** Index your folders and connect the tools you use:
   [running.md](../running.md) covers `meru setup`, `meru index` and `meru mcp add`.
2. **Write the questions.** Make a folder `bench/` at the root of your clone. Git ignores it, so
   nothing in it can be committed by mistake. In it, write `bench/tasks.jsonl`: one JSON object
   per line, the format `meru check` reads ([running.md](../running.md), "Check answers on your
   own files", lists every field):

   ```json
   {"id": "cal-dentist", "category": "t2-one-tool", "question": "When is my dentist appointment in March?", "want": {"tools": ["google.get_events"], "answer_any": ["12 March", "March 12"]}}
   ```

   Aim for about 50, spread over the six kinds above, and use your own questions: ones you
   really asked Meru are the best. `meru log` and your transcripts in `~/.meru/sessions/` help.
3. **Write the ground truth from the source, not from Meru.** Open the file, the mail, the
   attachment or the calendar entry yourself and copy the fact the answer must hold: a total, a
   date, a name. Pick facts that won't change: a past trip, a merged pull request, a paper's
   authors. "The latest release" and "this week" give a different right answer next month.
   Keep each `answer_any` list short and exact, since the grader looks for the words as written,
   ignoring case.
4. **Try each question once** against your normal `merud`, with `meru check bench/tasks.jsonl
   --only <id>`, and fix any question whose ground truth turns out wrong.
5. **Run the benchmark.** Stop your own `merud` first (`pkill merud`); two large answer models
   don't fit in memory together. Then:

   ```sh
   make bench            # every model set, three passes each; takes hours
   make bench-report     # writes docs/benchmarks/results.md
   ```

   `make bench SETS=gemma-moe REPEATS=1 ONLY=t0-direct` runs a small part of it first.

`make bench` runs in its own Meru home, `bench/home/`, so it never touches your real sessions,
memories or index. The first run copies your config, secrets, skills and memories there and
indexes your folders again, which takes a while once. Each pass then starts from that same
state, so no pass can recall another pass's answers. The results land in `bench/results/`, one
file per set and pass, with each question's answer, verdict and timings.

Publish the report if you like. It holds no question or answer, only the numbers. Before you
publish, read it once more for names, since the category names are yours to choose.
