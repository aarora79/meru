# How do I see how much I use Meru?

**In the terminal:**

```sh
meru usage
```

```text
                  1h   today     week   month     30d     all
sessions           1       2        5      12      14      30
tasks              4       9       31      88      97     212
tokens in        18k     41k     150k    420k    468k    1.4M
tokens out      2.1k    5.3k      19k     61k     66k    180k
active time   2m 14s  5m 01s  20m 10s  1h 01m  1h 07m  3h 05m
```

`/usage` in `meru chat` and the Usage button in the desktop app's rail show the same
table, and charts under it: tasks, tokens and time to first token per day,
the slowest tasks of the week, and tool calls by outcome. A task is one
question or request that Meru answered. In the desktop app each slow task
links to its chat. `/usage by model` adds a row per answer model: tasks, time to the first token, speed, tool calls and bad calls.

**In Prometheus.** Add this to `~/.meru/config.toml` and restart `merud`:

```toml
[observability]
prometheus = "127.0.0.1:9464"
```

`merud` then serves its metrics at <http://127.0.0.1:9464/metrics>, on this
machine only. Point a Prometheus you run at that address; the Usage page lists
queries to copy, such as tasks per day and the tool calls that failed today.

Nothing leaves the machine: `merud` refuses an address that isn't on this
machine, and the page holds counts and names, never your questions.

More detail: [observability.md](../observability.md).
