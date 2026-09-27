# Watch Meru on a dashboard

`merud` measures each question it answers: how long the first token took, which
route the router picked, how many tokens the main model read and wrote, and each
tool call. It sends these numbers as OpenTelemetry (OTel) metrics and traces to an
endpoint you run on this machine. This page shows how to run that endpoint, a
local Grafana stack, and how to read the numbers in a browser or from the command
line.

Export is off until you turn it on. The metrics and traces never leave this
machine: `merud` refuses an endpoint that isn't a loopback address.

[ARCHITECTURE.md, "Observability"](../ARCHITECTURE.md#observability) lists every
metric and span and explains why each exists.

## What you need

- Docker, or another tool that reads compose files.
- Ports 3000 and 4318 free on `127.0.0.1`. Check with
  `lsof -nP -iTCP:3000 -iTCP:4318 -sTCP:LISTEN`, which prints nothing when both are
  free.

## Set it up

**1. Start the stack.** From the root of the repo:

```sh
docker compose -f deploy/observability/compose.yaml up -d
```

This runs one container, `meru-lgtm`, from the `grafana/otel-lgtm` image. It holds
an OTel Collector, Prometheus, Tempo (traces), Loki (logs) and Grafana, with Meru's
two dashboards already loaded. The first start pulls the image, which takes about
3.4 GB on disk. The container restarts with Docker until you stop it.

**2. Point `merud` at it.** In `~/.meru/config.toml`, set `otlp_endpoint` in the
`[observability]` table:

```toml
[observability]
otlp_endpoint    = "http://127.0.0.1:4318"
metrics_interval = "10s"   # how often merud sends metrics
traces           = true    # send traces too
capture_content  = false   # keep question and answer text out of spans
```

**3. Restart `merud`.** It reads config only when it starts.

- Under launchd (macOS): `launchctl kickstart -k gui/$(id -u)/com.meru.merud`
- Under systemd (Linux): `systemctl --user restart merud`
- Started by hand: stop it with Ctrl-C and run `merud` again.

**4. Check that it works.** The start line in `~/.meru/merud.log` names the
endpoint:

```sh
grep 'merud starting' ~/.meru/merud.log | tail -1
```

Look for `otlp=http://127.0.0.1:4318` in that line; `otlp=off` means `merud` read
an empty endpoint. Then ask a question, such as `meru "hello"`, wait 10 seconds
for the next export, and open <http://127.0.0.1:3000>. Log in as `admin` with
password `admin`. Grafana opens on the Meru dashboard.

## Which dashboard to open

| You want | Open | Time range |
| --- | --- | --- |
| Today's usage: sessions, questions, tokens, active time, tool calls | **Meru usage**, the row of totals at the top | Today so far |
| Usage through the day, one bar per hour | **Meru usage**, the charts below the totals | Last 24 hours, its default; widen it to see a week |
| Speed right now: time to first token, tokens per second, turn time, routes | **Meru** | Last 1 hour, its default |
| Where one slow question spent its time | **Explore**, then **Tempo** (see [Traces](#see-one-questions-trace)) | around the question |

**Meru** shows:

- time to first token, p50 and p95, against the 1 s target;
- turn duration by route;
- input and output tokens per second, by tier;
- router decisions by route and outcome;
- cold model loads and load time;
- open client streams;
- Go runtime memory and goroutines.

**Meru usage** shows the numbers behind `meru usage`. Its top row gives totals for
the time range you pick: sessions, questions, tokens in, tokens out, active time
and tool calls. Below that row, one bar per hour shows sessions by source,
questions by route, the main model's input and output tokens, active time, files
read per question, and tool calls by outcome.

## When a panel says "No data"

- **You just turned export on.** Prometheus holds only what `merud` sent after
  you set `otlp_endpoint` and restarted it. It has nothing from before, so hours
  before that stay empty. `meru usage` reads the `turns` table, which `merud`
  rebuilds from your transcripts, so use it for all-time numbers.
- **An hour's bar lands when the hour ends.** Grafana draws each bar on the
  hour, and that bar covers the hour before, so the bar for 15:00 to 16:00
  shows at 16:00. The top row of Meru usage counts the current hour as it goes.
- **The range is shorter than an hour.** The hourly charts need a range that
  spans at least one full hour. Pick "Last 6 hours" or longer for them.

The totals also run a little under `meru usage`. Prometheus misses the first
count of each new series, such as the first question on a route from a new
client. The gap stops growing once each route and client has been used, but it
never closes.

## Query the metrics from the command line

Grafana passes queries through to Prometheus, so `curl` against port 3000 reaches
it, with no extra port open. Each query below uses PromQL, Prometheus's query
language, and `jq` to print the answer. Metric names in Prometheus swap the dots
for underscores and add a unit: `meru.turn.tokens` becomes
`meru_turn_tokens_total`.

Set the address and Grafana's login once:

```sh
P='http://127.0.0.1:3000/api/datasources/proxy/uid/prometheus'
AUTH='admin:admin'   # the stack's default Grafana login
```

List Meru's metrics:

```sh
curl -s -u "$AUTH" "$P/api/v1/label/__name__/values" \
  | jq -r '.data[] | select(startswith("meru_") or startswith("gen_ai_"))'
```

Tokens the main model read and wrote in the last 24 hours:

```sh
curl -s -u "$AUTH" -G "$P/api/v1/query" \
  --data-urlencode 'query=sum by (gen_ai_token_type) (increase(meru_turn_tokens_total{job="merud"}[24h]))' \
  | jq -r '.data.result[] | "\(.metric.gen_ai_token_type)\t\(.value[1] | tonumber | floor)"'
```

```text
output	146
input	19453
```

Questions answered in the last hour:

```sh
curl -s -u "$AUTH" -G "$P/api/v1/query" \
  --data-urlencode 'query=sum(increase(meru_turn_duration_seconds_count{job="merud", meru_outcome="ok"}[1h]))' \
  | jq -r '.data.result[0].value[1] | tonumber | round'
```

How often the router picked each route since `merud` started:

```sh
curl -s -u "$AUTH" -G "$P/api/v1/query" \
  --data-urlencode 'query=sum by (meru_route) (meru_route_decisions_total{job="merud"})' \
  | jq -r '.data.result[] | "\(.metric.meru_route)\t\(.value[1])"'
```

Time to first token at p95 over the last hour, in seconds:

```sh
curl -s -u "$AUTH" -G "$P/api/v1/query" \
  --data-urlencode 'query=histogram_quantile(0.95, sum by (le) (rate(gen_ai_server_time_to_first_token_seconds_bucket{job="merud"}[1h])))' \
  | jq -r '.data.result[0].value[1]'
```

Tool calls by tool and outcome over the last 7 days:

```sh
curl -s -u "$AUTH" -G "$P/api/v1/query" \
  --data-urlencode 'query=sum by (gen_ai_tool_name, meru_outcome) (increase(meru_tool_calls_total{job="merud"}[7d]))' \
  | jq -r '.data.result[] | "\(.metric.gen_ai_tool_name)\t\(.metric.meru_outcome)\t\(.value[1] | tonumber | round)"'
```

Questions per hour over the last 6 hours, as a series. `query_range` takes a
start, an end and a step, and returns one value per step:

```sh
curl -s -u "$AUTH" -G "$P/api/v1/query_range" \
  --data-urlencode 'query=sum(increase(meru_turn_duration_seconds_count{job="merud", meru_outcome="ok"}[1h]))' \
  --data-urlencode "start=$(( $(date +%s) - 21600 ))" \
  --data-urlencode "end=$(date +%s)" \
  --data-urlencode 'step=3600' \
  | jq -r '.data.result[0].values[] | "\(.[0] | strflocaltime("%H:%M"))\t\(.[1] | tonumber | round)"'
```

Without Grafana in the path, run `curl` inside the container, where Prometheus
listens on port 9090:

```sh
docker exec meru-lgtm curl -s -G http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(meru_tool_calls_total{job="merud"})'
```

Prometheus's [HTTP API documentation](https://prometheus.io/docs/prometheus/latest/querying/api/)
covers the other endpoints, and its
[PromQL guide](https://prometheus.io/docs/prometheus/latest/querying/basics/) the
query language.

## See one question's trace

Each question also sends a trace: one span for each stage, nested so you can see
which stage took the time.

1. In Grafana, open **Explore** and pick the **Tempo** data source.
2. To list recent questions, choose **Search**, set **Service Name** to `merud` and
   **Span Name** to `rpc.request`, then run the query.
3. To find the question behind a log line, copy the line's `trace_id`, choose
   **TraceQL**, paste the ID into the query box and run it.

The trace shows `rpc.request` at the top, `meru.turn` under it, and then the
session, the route and its model call, the prompt, the answer's model call and the
two transcript writes. Each `gen_ai.chat` span carries token counts and Ollama's
load, prompt and answer times; the answer's span has a `first_token` event. Spans
carry no question or answer text unless `capture_content = true`.

## What stays on this machine

- Both ports bind to `127.0.0.1`, so other machines can't reach them.
- The compose file turns off Grafana's usage reports, update checks, news feed,
  Gravatar lookups and external snapshots, and the usage reports that Loki, Tempo
  and Pyroscope send by default.
- `merud` refuses any `otlp_endpoint` that isn't a loopback address, and fails to
  start with a message that names `observability.otlp_endpoint`.
- Question and answer text stay out of spans unless you set
  `capture_content = true`.

## Where the data lives, and how to stop

Metrics, traces and Grafana's state live in the Docker volume
`meru-observability_lgtm-data`. Prometheus keeps metrics for 400 days
(`PROMETHEUS_EXTRA_ARGS` in the compose file), so the usage dashboard can show a
month's trend; its default of 15 days would cut that in half.

To stop sending, set `otlp_endpoint = ""` and restart `merud`. To stop the stack:

```sh
docker compose -f deploy/observability/compose.yaml down      # keep the data
docker compose -f deploy/observability/compose.yaml down -v   # delete it too
```

## Change a dashboard

The dashboards are JSON files in `deploy/observability/dashboards/`, and Grafana
reloads them from that folder about 10 seconds after a change. To edit one in
Grafana, change it, export it as JSON, and save it over its file. Grafana won't
save a provisioned dashboard in place, so a change you don't export is lost when
the container restarts.
