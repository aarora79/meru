// This file holds Due: whether a job should run now, given its schedule and
// its last run, and how late that run would be.

package jobs

import "time"

// CatchUp is how late a missed run may start. A Mac asleep at 7:00 runs the
// morning brief when it wakes at 8:30; one that was off all day doesn't run
// yesterday's brief at 9 p.m. Twelve hours covers a night asleep and a
// restart, and skips a run that would no longer be useful.
const CatchUp = 12 * time.Hour

// Decision is what Due decided for one job at one moment.
type Decision struct {
	// Run is true when the job should start now.
	Run bool
	// Slot is the scheduled time this run stands for: the latest one that
	// has passed. Late is how long ago it was, zero or a few seconds for a
	// run on time.
	Slot time.Time
	Late time.Duration
	// Skipped, when not zero, is the first scheduled time Due let pass
	// because every missed slot was more than CatchUp late. The caller logs
	// it, and moves the job's last run to now so Due doesn't report it again.
	Skipped time.Time
}

// Due decides whether a job with schedule s, last run or last skipped at
// last, should run at now.
//
// When several slots passed since last, as after a night asleep for a job
// that runs every hour, the job runs once, for the latest: the work a run
// does, such as reading today's mail, is the same for all of them. When the
// latest passed slot is more than CatchUp ago, the job doesn't run; Skipped
// says which slot was missed. The caller sets last to Slot when it starts a
// run, and to now after a skip.
func Due(s Schedule, last, now time.Time) Decision {
	first, ok := s.Next(last)
	if !ok || first.After(now) {
		return Decision{}
	}
	// Look for the latest slot within CatchUp of now, starting no earlier
	// than last. Slots before that window are too late to run.
	from := last
	if window := now.Add(-CatchUp); window.After(from) {
		// One second earlier, so a slot exactly CatchUp ago still counts.
		from = window.Add(-time.Second)
	}
	var latest time.Time
	for t, ok := s.Next(from); ok && !t.After(now); t, ok = s.Next(t) {
		latest = t
	}
	if latest.IsZero() {
		return Decision{Skipped: first}
	}
	return Decision{Run: true, Slot: latest, Late: now.Sub(latest)}
}
