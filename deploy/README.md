# deploy

Files that run Meru as a service and show what it is doing:

| Path | What it does |
| --- | --- |
| `launchd/com.meru.merud.plist` | starts `merud` at login on macOS and restarts it if it exits |
| `systemd/merud.service` | the same on Linux, as a systemd user unit |
| `observability/compose.yaml` | a local Grafana stack that receives `merud`'s metrics and traces |
| `observability/dashboards/meru.json` | the Meru dashboard that stack loads |
| `observability/dashboards/meru-usage.json` | the Meru usage dashboard: one bar per day over 30 days |

All the steps below assume you built and installed `merud` with:

```sh
go install ./cmd/merud      # puts the binary in ~/go/bin/merud
```

If your binary lives somewhere else, change the path in the service file before you
install it.

## macOS: launchd

launchd reads agents from `~/Library/LaunchAgents/`. It doesn't expand `~` or
`$HOME`, so the plist uses `__HOME__` as a placeholder, and `sed` fills in your home
directory while copying it:

```sh
mkdir -p ~/.meru
sed "s|__HOME__|$HOME|g" deploy/launchd/com.meru.merud.plist \
  > ~/Library/LaunchAgents/com.meru.merud.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.meru.merud.plist
```

`merud` now starts each time you log in, and launchd starts it again if it exits.
Anything `merud` prints before its own log opens goes to `~/.meru/merud.out.log` and
`~/.meru/merud.err.log`.

Check it, restart it, or remove it:

```sh
launchctl print gui/$(id -u)/com.meru.merud      # state and last exit code
launchctl kickstart -k gui/$(id -u)/com.meru.merud
launchctl bootout gui/$(id -u)/com.meru.merud
```

## Linux: systemd

Install it as a user unit, so `merud` runs as you and reads your `~/.meru`:

```sh
mkdir -p ~/.config/systemd/user
cp deploy/systemd/merud.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now merud
```

systemd restarts `merud` after a crash, and leaves it stopped after
`systemctl --user stop merud`. A user unit starts when you log in. To start it at
boot, before anyone logs in, as on a home server or a cloud machine you reach over
SSH, turn on lingering once:

```sh
sudo loginctl enable-linger "$USER"
```

Check it and read its output:

```sh
systemctl --user status merud
journalctl --user -u merud -f
```

## Windows

Meru has no Windows service wrapper yet; that is future work. Until then, start
`merud.exe` by hand or from a Task Scheduler task that runs at log on.

## Observability stack

`observability/compose.yaml` runs one container, `grafana/otel-lgtm`, which holds an
OpenTelemetry Collector, Prometheus, Tempo, Loki and Grafana. It needs Docker or
another tool that reads compose files.

```sh
docker compose -f deploy/observability/compose.yaml up -d
```

Then point `merud` at it in `~/.meru/config.toml` and restart `merud`:

```toml
[observability]
otlp_endpoint    = "http://127.0.0.1:4318"
metrics_interval = "10s"
traces           = true
```

Open <http://127.0.0.1:3000> and log in as `admin` with password `admin`. Grafana
opens on the Meru dashboard. Its panels show:

- time to first token, p50 and p95, against the 1 s target for v0.1;
- turn duration by route;
- input and output tokens per second, by tier;
- router decisions by route and outcome;
- cold model loads and load time;
- open client streams;
- Go runtime memory and goroutines.

The "Meru usage" dashboard, in the same folder, shows the trends behind
`meru usage`, one bar per day over the last 30 days:

- sessions started, by source;
- questions answered, by route;
- the main model's input and output tokens;
- active time, the seconds `merud` spent answering;
- files read per question, by route;
- tool calls, by outcome.

Traces go to Tempo: in Grafana, choose Explore, then Tempo, and search for service
`merud` and span name `rpc.request`. To open the trace behind a line in
`merud.log`, paste the line's `trace_id` into the TraceQL box.
[docs/running.md](../docs/running.md), "See one question's trace", describes the
spans.

The stack keeps its data on this machine:

- Both ports bind to `127.0.0.1`, so other machines can't reach them.
- The compose file turns off Grafana's usage reports, update checks, news feed,
  Gravatar lookups and external snapshots, and the usage reports that Loki, Tempo
  and Pyroscope send by default.
- `merud` itself refuses any `otlp_endpoint` that isn't a loopback address.

Metrics, traces and Grafana's state live in the Docker volume `lgtm-data`.
Prometheus keeps metrics for 400 days (`PROMETHEUS_EXTRA_ARGS` in the compose file),
so the usage dashboard can show a month's trend; its default of 15 days would cut
that in half. Only data from after you set `otlp_endpoint` is there. For all-time
numbers, `meru usage` reads the `turns` table instead.

Stop the stack with `docker compose -f deploy/observability/compose.yaml down`. Add
`-v` to delete the stored metrics and traces too.

To edit a dashboard, change it in Grafana, export it as JSON, and save it over
its file in `observability/dashboards/`. Grafana reloads provisioned dashboards from
that folder.
