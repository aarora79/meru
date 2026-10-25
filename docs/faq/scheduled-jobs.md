# How do I make Meru do something on a schedule?

Add a job to `~/.meru/config.toml` and restart `merud`. A job asks Meru a
question at the times you set, in your computer's time zone:

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

**See, run and stop jobs:** Settings, Jobs in Meru.app, or `/jobs` in `meru
chat`. Run now starts a job at once; Stop (`c`) ends a run, which keeps what it
wrote; Open last run (`o`) opens its chat.

**Pause a job** with `enabled = false`. When you turn it back on, it doesn't
make up the runs it missed.

**If your Mac was asleep** at the time, the job runs once when it wakes, if no
more than 12 hours late, and says it ran late. A run more than 12 hours late is
skipped, so yesterday's brief doesn't arrive at night.

**Tool calls in a job.** Nobody watches a run, so every tool call that would ask
you is declined, and the answer says what it skipped. Tools that don't ask, such
as `web_search`, run as usual. Letting a job use tools that ask, running a
script, and saving the answer to a file come in a later release (#167).

More detail: [ARCHITECTURE.md, Scheduler](../../ARCHITECTURE.md#scheduler).
