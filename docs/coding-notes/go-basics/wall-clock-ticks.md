# Wall-clock ticks

A program that must do something once a day, or at 7:00, has two ways to wait:
set a timer for the moment, or wake up often and look at the clock. Meru does
the second, in the scheduler (`internal/jobs`) and the usage report
(`internal/usagereport`).

## The problem with one long timer

```go
// Wait until midnight, then send. This looks right and isn't.
time.Sleep(time.Until(midnight))
send()
```

Go's timers count time that passes while the program runs. They read the
computer's monotonic clock, which on macOS stops while the computer sleeps. A
laptop that sleeps from 23:00 to 08:00 wakes with the timer still owing an hour,
so the send happens at 09:00, not at wake. Change the clock, cross a daylight
saving change or move time zones, and a long timer drifts further.

## A short tick and a date compare

```go
// time.NewTicker sends the time on ticker.C every 30 minutes.
ticker := time.NewTicker(30 * time.Minute)
// defer runs ticker.Stop() when the function returns, so the ticker
// doesn't keep running after the loop ends.
defer ticker.Stop()
for {
	select {
	case <-ctx.Done():
		return
	case now := <-ticker.C:
		// Ask the question again on every tick: has today's work been done?
		if lastSent.Before(startOfDay(now)) {
			send()
		}
	}
}
```

Each tick is short, so the most it can be late after a sleep is one tick. The
decision doesn't depend on when the ticker fired: it reads the wall clock
(`now`) and compares dates, which survives sleep, clock changes and restarts. A
record of what was done (a "sent" line in a file, a last run in the store) makes
the check safe to repeat: a second tick the same day finds the work done and
does nothing.

The `select` waits for whichever case is ready first, so the loop also stops as
soon as `merud` shuts down. See [select](select.md) and [context](context.md).

## Testing it

A loop like this is easy to test, because the decision is a plain function of
the time and the record. The tests call the decision with any `now` they like,
such as 01:30 UTC or the day after a send, instead of waiting for a ticker.

## Where Meru uses it

- `internal/jobs/scheduler.go`: every 30 seconds, for jobs due at a minute.
- `internal/usagereport/send.go`: every 30 minutes, for one report a UTC day.
