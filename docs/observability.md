# Read Meru's metrics

`merud` measures each question it answers: how long the first token took, which
route the router picked, how many tokens the main model read and wrote, and each
tool call. This page shows the three places to read those numbers: the Usage page,
which needs nothing installed; a `/metrics` page for a Prometheus you run; and an
OpenTelemetry (OTel) push to a collector you run, which also carries traces.

The numbers never leave this machine. `merud` refuses a `/metrics` address or a
push endpoint that isn't a loopback address.

[ARCHITECTURE.md, "Observability"](../ARCHITECTURE.md#observability) lists every
metric and span and explains why each exists.

## The Usage page

Click Usage at the foot of the desktop app's rail, or type `/usage` in
`meru chat`. Both draw from `meru.db`, which `merud` rebuilds from your session transcripts, so they need
nothing else running. A task is one question or request that Meru answered.
The desktop app's Usage screen has two tabs. **Charts**, open first, shows, from
the top:

- the usage table: tasks, chats, tokens, time answering, files read and tool
  calls for the last hour, today, this week, this month, the last 30 days and all
  time;
- four bar charts over 30 days, two to a row, each with a labelled y axis:
  tasks per day; time to first token per day, with the middle task (p50) and
  the slow end (p95); tokens in per day; and tokens out per day;
- the ten slowest tasks of the week, each a link to its chat;
- tool calls over 30 days by outcome, failures in red;
- use by answer model, as `/usage by model` shows it.

**Prometheus queries** shows how to read the same numbers in Prometheus, with
queries to copy.

`meru chat` shows the same data in text: bar lines of tasks and tokens in for the
last 14 days, today's tokens and time to first token, the five slowest tasks and
the tools whose calls failed.
`meru usage` prints the usage table alone.

## Serve `/metrics` for Prometheus

**1. Turn the page on.** Add this to `~/.meru/config.toml`, under
`[observability]`, and restart `merud`:

```toml
[observability]
prometheus = "127.0.0.1:9464"
```

`merud` writes a `metrics page` line with the address to `merud.log` when it starts.
If another program holds the port, `merud` stops with a message that names
`observability.prometheus`; pick another port.

**2. Check it.**

```sh
curl -s http://127.0.0.1:9464/metrics | grep -E '^(meru|gen_ai)_' | head
```

The page holds every metric in ARCHITECTURE.md's table, in Prometheus's text
format. Names swap the dots for underscores, add the unit, and end a counter in
`_total`: `meru.tool.calls` becomes `meru_tool_calls_total`, and
`meru.turn.duration` becomes `meru_turn_duration_seconds`. A metric appears once it
has a value, so ask Meru a question first.

**3. Point a Prometheus at it.** Install Prometheus however you like, for example
with `brew install prometheus`, and add a scrape job to its `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: merud
    scrape_interval: 15s
    static_configs:
      - targets: ["127.0.0.1:9464"]
```

Start it bound to this machine, so other machines can't read it:

```sh
prometheus --config.file=prometheus.yml --web.listen-address=127.0.0.1:9090
```

Prometheus's own page at <http://127.0.0.1:9090> takes the queries below in its
**Graph** tab.

**Counters start again from zero when `merud` restarts.** `increase()` and `rate()`
spot the drop and count across it, so use them rather than the raw counter.

## Queries

The Usage screen's Prometheus queries tab offers these six, each with a Copy button:

| What it answers | PromQL |
| --- | --- |
| Tasks per day | `sum(increase(meru_turn_duration_seconds_count[1d]))` |
| Time to first token, p95 over the last hour | `histogram_quantile(0.95, sum by (le) (rate(gen_ai_server_time_to_first_token_seconds_bucket[1h])))` |
| Turn time by route, p95 | `histogram_quantile(0.95, sum by (le, meru_route) (rate(meru_turn_duration_seconds_bucket[1h])))` |
| Tool calls that failed today | `sum by (gen_ai_tool_name, meru_outcome) (increase(meru_tool_calls_total{meru_outcome!="ok"}[1d]))` |
| Tokens per model today | `sum by (gen_ai_request_model, gen_ai_token_type) (increase(gen_ai_client_token_usage_sum[1d]))` |
| Model loads today | `sum(increase(meru_engine_load_duration_seconds_count[1d]))` |

From the command line, ask Prometheus's HTTP API and print the answer with `jq`:

```sh
P='http://127.0.0.1:9090'
```

Tokens the main model read and wrote in the last 24 hours:

```sh
curl -s -G "$P/api/v1/query" \
  --data-urlencode 'query=sum by (gen_ai_token_type) (increase(meru_turn_tokens_total{job="merud"}[24h]))' \
  | jq -r '.data.result[] | "\(.metric.gen_ai_token_type)\t\(.value[1] | tonumber | floor)"'
```

```text
output	146
input	19453
```

How often the router picked each route since `merud` started:

```sh
curl -s -G "$P/api/v1/query" \
  --data-urlencode 'query=sum by (meru_route) (meru_route_decisions_total{job="merud"})' \
  | jq -r '.data.result[] | "\(.metric.meru_route)\t\(.value[1])"'
```

Tool calls by tool and outcome over the last 7 days:

```sh
curl -s -G "$P/api/v1/query" \
  --data-urlencode 'query=sum by (gen_ai_tool_name, meru_outcome) (increase(meru_tool_calls_total{job="merud"}[7d]))' \
  | jq -r '.data.result[] | "\(.metric.gen_ai_tool_name)\t\(.metric.meru_outcome)\t\(.value[1] | tonumber | round)"'
```

Tasks per hour over the last 6 hours, as a series. `query_range` takes a
start, an end and a step, and returns one value per step:

```sh
curl -s -G "$P/api/v1/query_range" \
  --data-urlencode 'query=sum(increase(meru_turn_duration_seconds_count{job="merud", meru_outcome="ok"}[1h]))' \
  --data-urlencode "start=$(( $(date +%s) - 21600 ))" \
  --data-urlencode "end=$(date +%s)" \
  --data-urlencode 'step=3600' \
  | jq -r '.data.result[0].values[] | "\(.[0] | strflocaltime("%H:%M"))\t\(.[1] | tonumber | round)"'
```

Prometheus's [HTTP API documentation](https://prometheus.io/docs/prometheus/latest/querying/api/)
covers the other endpoints, and its
[PromQL guide](https://prometheus.io/docs/prometheus/latest/querying/basics/) the
query language.

## Traces

A trace follows one question, with a span for each stage, nested so you can see
which stage took the time. Traces go out only by OTLP, the OpenTelemetry Protocol,
to a collector you run on this machine, such as an OpenTelemetry Collector or
Jaeger listening for OTLP over HTTP on port 4318:

```toml
[observability]
otlp_endpoint = "http://127.0.0.1:4318"
traces        = true
```

The same push sends the metrics too, so a collector can store them instead of, or
as well as, a Prometheus scraping `/metrics`. Both report the same numbers.

The trace shows `rpc.request` at the top, `meru.turn` under it, and then the
session, the route and its model call, the prompt, the answer's model call and the
two transcript writes. Each `gen_ai.chat` span carries token counts and Ollama's
load, prompt and answer times; the answer's span has a `first_token` event. Every
line of the turn in `merud.log` carries the same `trace_id`, so a log line leads to
its trace and back.

If nothing listens at `otlp_endpoint`, each push fails. `merud` logs that once an
hour, not on every push. Set `otlp_endpoint = ""` and restart `merud` to stop it.

## What stays on this machine

- `merud` refuses a `prometheus` address or an `otlp_endpoint` that isn't a
  loopback address, and fails to start with a message that names the key.
- `/metrics` answers only a request whose `Host` header names this machine
  (`127.0.0.1`, `localhost` or `::1`). A web page that points its own domain name
  at 127.0.0.1 sends that name, and gets 403, so it can't read the page through
  your browser.
- Any program on this machine can read `/metrics`. It holds counts, times, and
  names of models, routes, servers, tools and outcomes; never a question, a path
  or a session ID.
- Question and answer text stay out of spans unless you set
  `capture_content = true`.

## Moving off the old Grafana stack

Releases up to v0.4.11 shipped a Grafana stack in `deploy/observability/`. The
compose file is gone, so `docker compose down` can't find it. Remove the
container and its data by name:

```sh
docker rm -f meru-lgtm
docker volume rm meru-observability_lgtm-data
```

Then set `otlp_endpoint = ""` in `~/.meru/config.toml`, or point it at a collector
you run, and restart `merud`.
