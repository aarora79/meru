# How do I make Meru do something on a schedule?

In Meru.app, open Settings, Jobs, and click **Add a job**: give it a name, when
it runs and what Meru should do. As you type the schedule, the form says it in
words, such as "7:00 on weekdays", and when the next run is. In `meru chat`,
type `/jobs` and press `a`. Meru saves the job in `~/.meru/config.toml`, and it
takes effect at once. A job asks Meru a question at the times you set, in your
computer's time zone. In `config.toml` it reads:

```toml
[[jobs]]
name     = "morning-brief"
schedule = "0 7 * * 1-5"   # 7:00 on weekdays
prompt   = "Brief me on today's calendar, unread mail and anything due this week."
```

Each run is a chat in the **Jobs** folder, first in the chat list, named after
the job and the time, such as "morning-brief · Fri 24 Oct 07:00".

**The schedule** has five fields: minute, hour, day of month, month and day of
week. `*` means any; `1-5` is a range; `*/15` is every 15; `mon` and `jan` work
as names.

| You want | Write |
| --- | --- |
| 7:00 on weekdays | `0 7 * * 1-5` |
| every 15 minutes | `*/15 * * * *` |
| 9:00 on the 1st of each month | `0 9 1 * *` |
| 18:30 on Fridays | `30 18 * * fri` |

**Every other Tuesday** needs two more keys, since cron can't say it:

```toml
[[jobs]]
name        = "go-news"
schedule    = "0 9 * * 2"
every_weeks = 2
from        = "2026-10-27"   # the first day it may run
skill       = "web-research" # load this skill without the pick step
prompt      = "Summarize what changed in the Go release notes."
```

In the form, pick **Every other week** and the first day for this. A job you
write in `config.toml` by hand takes effect when `merud` restarts.

**See, run and stop jobs:** Settings, Jobs in Meru.app, or `/jobs` in `meru
chat`. Run now starts a job at once; Stop (`c`) ends a run, which keeps what it
wrote; Open last run (`o`) opens its chat. Edit (`e`) changes a job, and Remove
(`d` twice) takes it out; its past runs stay in the Jobs folder. While you have
a job, Meru keeps the Jobs folder, so remove the jobs before the folder.

**Pause a job** with Pause (`p`), which writes `enabled = false`. When you
resume it, it doesn't make up the runs it missed. A job whose schedule you
change counts from the change, too.

**If your Mac was asleep** at the time, the job runs once when it wakes, if no
more than 12 hours late, and says it ran late. A run more than 12 hours late is
skipped, so yesterday's brief doesn't arrive at night.

**Save the answer to a file** with `output`, a path inside `~/meru-output`:

```toml
output = "briefs/{date}.md"   # {date}, {time} and {job} fill in
```

A run replaces a file of the same name. The write goes through Meru's tool log,
so the run's chat shows it.

**Run a script** you declared under `[[commands]]`, with no model:

```toml
[[commands]]
name        = "backup-notes"
description = "Copies my notes to the backup disk"
argv        = ["~/bin/backup-notes.sh"]   # the script's own path; it needs #! on its first line

[[jobs]]
name     = "backup"
schedule = "30 23 * * *"
command  = "backup-notes"
```

The script's output is the run's answer. Meru never runs a shell line; it runs
the program you declared.

**Tool calls in a job.** Nobody watches a run, so a tool call that would ask you
is declined, and the answer says what it skipped, unless the job's `allow` names
the tool:

```toml
allow = ["write_file", "cmd.backup-notes"]   # full tool names, as /log shows them
```

Then a call that would ask once runs. A few stay declined in a job whatever
`allow` says: changing settings (`configure`), reading a web address nobody gave
Meru, and downloads, so an unattended job can't send your data to a site a page
told it about. A website you trusted (`/sites`) reads without asking, in a job
too.

**Allow only what a job needs.** A job that reads mail or web pages can be told
by them to do something, and what `allow` lists runs without asking. Allow a tool
that sends mail only if sending is the job's purpose.

More detail: [ARCHITECTURE.md, Scheduler](../../ARCHITECTURE.md#scheduler).
