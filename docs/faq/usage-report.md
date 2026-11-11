# What does Meru send about me, and how do I turn it off?

Meru sends one anonymous count a day, and nothing else about itself. It sends
nothing until `meru chat` or the desktop app has shown you a notice about it,
and the first count waits until the next day.

## What goes

One report, about 250 bytes, at most once a day. This is a whole one:

```json
{"v":2,"meru":"0.7.7","os":"darwin","arch":"arm64","profile":"lite","age":"month","questions":"1-5",
 "active_days":"3-5","streak":"2-4","previous":"0.7.6","week_first":true,"month_first":false,"first":false}
```

It holds no ID, no question or answer, no file or folder name, no setting and no
time of day. Each field is a word from a short list, a version number, or true
or false. Meru works out every one of them on your computer, from its own record
of what it sent (`~/.meru/usage-report.jsonl`) and its count of your questions.

### Each field

- **`v`**, the report's format: `2`. A report with a new field gets a new
  number, and Meru shows you its notice again before it sends one. Meru v0.7.6
  sent version `1`, the first seven fields below.
- **`meru`**, the Meru release, such as `0.7.7`. It tells the project which
  releases people keep. A build from source never sends.
- **`os`**, your system: `darwin` (macOS), `linux` or `windows`. It decides where
  the work goes, such as Linux or Windows support.
- **`arch`**, your chip's family: `arm64` (Apple silicon and other ARM chips) or
  `amd64` (Intel and AMD). It names no chip model.
- **`profile`**, the model set: `lite` or `full`. It shows whether people run the
  small default models or the large ones. It names no model.
- **`age`**, how long ago Meru first showed you the notice: `week` (under 7
  days), `month` (under 31), `year` (under 366) or `older`. It tells new installs
  from ones that have run for months.
- **`questions`**, how many questions Meru answered yesterday (UTC), leaving out
  scheduled jobs: `0`, `1-5`, `6-20` or `21+`. It shows how much an install is
  used on a day.
- **`active_days`**, on how many of the last seven days (UTC) you asked at least
  one question: `0`, `1-2`, `3-5` or `6-7`. It shows how often installs are
  used across a week, not on one day.
- **`streak`**, how many weeks in a row this install has sent a report, this week
  included: `1`, `2-4`, `5-12` or `13+`. A week is an ISO week, Monday to Sunday
  in UTC. It shows how many installs keep running Meru.
- **`previous`**, the Meru version this install reported last, such as `0.7.6`,
  or `none` before its first report. Next to `meru`, it shows which releases
  people upgrade from, and how fast.
- **`week_first`**, `true` when this is the install's first report of the ISO
  week, `false` for the rest of that week.
- **`month_first`**, `true` when this is the install's first report of the
  month (UTC), `false` for the rest of it.
- **`first`**, `true` on the install's first report ever, `false` after.

The last three let the project count installs over a week, a month and all time
with no ID, as the next section shows.

## How the counts work

The collector stores one row per report: the UTC date it arrived and the fields
above. With no ID, it can't tell which rows came from the same install. The three
flags make the counts work anyway: each install sets `week_first` on exactly one
report a week, `month_first` on one a month, and `first` on one ever. So counting
the rows with a flag set counts each install once.

An install counts as **active** in a day, week or month when it sent a report in
it, and as **new** on the day of its first report.

| Count | How | The SQL over `reports` |
| --- | --- | --- |
| Active installs today | every report today | `SELECT COUNT(*) FROM reports WHERE day = date('now')` |
| Active installs this week | reports with `week_first` this ISO week | `SELECT SUM(week_first) FROM reports WHERE date(day, 'weekday 0', '-6 days') = date('now', 'weekday 0', '-6 days')` |
| Active installs this month | reports with `month_first` this month | `SELECT SUM(month_first) FROM reports WHERE substr(day, 1, 7) = strftime('%Y-%m', 'now')` |
| New installs today | reports with `first` today | `SELECT SUM(first) FROM reports WHERE day = date('now')` |
| New installs this week | reports with `first` this ISO week | `SELECT SUM(first) FROM reports WHERE date(day, 'weekday 0', '-6 days') = date('now', 'weekday 0', '-6 days')` |
| New installs this month | reports with `first` this month | `SELECT SUM(first) FROM reports WHERE substr(day, 1, 7) = strftime('%Y-%m', 'now')` |
| Total installs ever | every report with `first`, kept in a counter | `SELECT n FROM counters WHERE name = 'installs'` |
| Installs still running after some weeks | this week's `week_first` reports by `streak` | `SELECT streak, COUNT(*) FROM reports WHERE week_first = 1 AND date(day, 'weekday 0', '-6 days') = date('now', 'weekday 0', '-6 days') GROUP BY streak` |

`date(day, 'weekday 0', '-6 days')` is SQLite's way to say "the Monday that
starts this day's week": `weekday 0` moves to the Sunday that ends it. The total
lives in a counter because the collector deletes rows older than 400 days, and a
sum over the rows would shrink.

The [public page](https://meru-report.meru-workers.workers.dev/) and
[`/totals`](https://meru-report.meru-workers.workers.dev/totals) show all of these
for each recent day, week and month. They show each field's counts on its own,
never crossed with another field: on a quiet week, a row that crossed version,
system, streak and active days could narrow down to one install.

What the counts can't do:

- **Reinstalling counts as a new install.** So does deleting
  `usage-report.jsonl`, since nothing lasting ties the old install to the new one.
- **Two computers count as two installs,** even for one person.
- **An install that's off or asleep for a whole week isn't in that week's count,**
  which is right: it wasn't active.
- **Reports from Meru v0.7.6 count only by day.** Version 1 has no flags.

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

- You upgraded from a release before v0.7.6 and haven't said yes. Your config has
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
