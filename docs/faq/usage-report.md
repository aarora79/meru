# What does Meru send about me, and how do I turn it off?

Meru sends one anonymous count a day, and nothing else about itself. It sends
nothing until `meru chat` or the desktop app has shown you a notice about it,
and the first count waits until the next day.

## What goes

One report, about 100 bytes, at most once a day:

```json
{"v":1,"meru":"0.7.5","os":"darwin","arch":"arm64","profile":"lite","age":"month","questions":"1-5"}
```

| Field | What it holds | Values |
| --- | --- | --- |
| `v` | the report's format | `1` |
| `meru` | the Meru release | such as `0.7.5` |
| `os` | your system | `darwin` (macOS), `linux`, `windows` |
| `arch` | your chip | `arm64`, `amd64` |
| `profile` | the model set | `lite`, `full` |
| `age` | how long ago Meru first showed you the notice | `week`, `month`, `year`, `older` |
| `questions` | how many questions Meru answered yesterday, leaving out scheduled jobs | `0`, `1-5`, `6-20`, `21+` |

It holds no ID, no question or answer, no file or folder name, no setting and no
time of day. Each install sends at most one a day, so the project counts the
installs that reported on a day without telling one install from another.

## Why

Meru is free and has no accounts, so the project has no other way to know how
many people run it, on which systems, and which releases they keep. That number
decides where the work goes, such as Linux or Windows support.

## See what went

Every report Meru sends goes into `~/.meru/usage-report.jsonl` exactly as sent:

```sh
cat ~/.meru/usage-report.jsonl
```

The Usage screen in the desktop app, `/usage` in `meru chat` and `meru report`
show the same, with the next report and when it goes. The totals from every
install are public at <https://meru-report.meru-workers.workers.dev/>.

## Turn it off

Any one of these works:

- In the desktop app, click **Turn off** on the notice, or the switch on the
  Usage screen.
- In `meru chat`, type `/report` and press `o`.
- In a terminal: `meru report off`.
- In `~/.meru/config.toml`, set:

  ```toml
  [usage_report]
  send = "off"
  ```

`merud` reads the choice at its next check, within half an hour, with no
restart.

**`DO_NOT_TRACK`.** Meru honors `DO_NOT_TRACK=1`, but only in `merud`'s own
environment. `merud` runs under launchd on a Mac and systemd on Linux, which
don't pass on the variables your shell sets, so `export DO_NOT_TRACK=1` in
`.zshrc` doesn't reach it. It does reach `meru setup`, which then writes
`send = "off"` for you. To set it for `merud`, add it to the service file, or use
one of the ways above.

## Turn it on

`meru report on`, **Share** on the notice, or `send = "on"` in `config.toml`.

## Send today's now

Meru sends each day's report between midnight and 2 a.m. UTC, or soon after your
computer wakes. To send today's at once, click **Send today's now** on the Usage
screen, press `t` in `/report`, or run `meru report send`. It sends only if none
went today, so it never adds a second count for the day, and it says why when it
can't send.

## When Meru never sends

- You upgraded from a release before v0.7.5 and haven't said yes. Your config has
  no `send` line, and Meru asks in `meru chat` and the app.
- You never open `meru chat` or the app. A one-shot `meru "..."` or
  `meru run --json` doesn't count as showing the notice, so a computer that only
  runs scripts never sends.
- You built Meru from source. Only release builds send.

## Who sees it

The report goes to a small program the project runs on Cloudflare (its code is in
`collector/` in the repository). It stores the date and the seven fields, with
no IP address, and keeps them 400 days. It keeps no logs. Cloudflare's network
sees your IP address when the report arrives, as any website you visit does; the
project never stores it.

Meru still sends no crash reports and never checks for updates. Its own
measurements, the ones the Usage screen charts, stay on your computer.
