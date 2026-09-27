# How do I see how much I use Meru?

**In the terminal:**

```sh
meru usage
```

```text
                  1h   today     week   month     30d     all
sessions           1       2        5      12      14      30
questions          4       9       31      88      97     212
tokens in        18k     41k     150k    420k    468k    1.4M
tokens out      2.1k    5.3k      19k     61k     66k    180k
active time   2m 14s  5m 01s  20m 10s  1h 01m  1h 07m  3h 05m
```

`/usage` in `meru chat` and Settings, Usage in the desktop app show the same
table. `/usage by model` adds a row per answer model: questions, time to the
first token, speed, tool calls and bad calls.

**On a dashboard.** `merud` can send its metrics and traces to a Grafana stack
that runs in one Docker container on this machine. From your clone of the repo:

```sh
docker compose -f deploy/observability/compose.yaml up -d
```

Add this to `~/.meru/config.toml` and restart `merud`:

```toml
[observability]
otlp_endpoint = "http://127.0.0.1:4318"
```

Open <http://127.0.0.1:3000> (user `admin`, password `admin`). The **Meru**
dashboard shows speed: time to first token, turn time and tokens per second.
**Meru usage** shows the numbers behind `meru usage`, with one bar per hour
over the last day.

Nothing leaves the machine: `merud` refuses an endpoint that isn't on this
machine, and your questions and answers stay out of the traces unless you set
`capture_content = true`.

More detail: [observability.md](../observability.md).
