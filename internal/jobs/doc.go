// Package jobs reads the schedules of [[jobs]] and decides when each job
// runs (ARCHITECTURE.md, "Scheduler"). A schedule is a five-field cron
// expression in this computer's time zone, such as "0 7 * * 1-5" for 7:00
// on weekdays, plus an optional every-N-weeks rule for schedules cron can't
// say, such as every other Tuesday.
//
// The files:
//
//   - schedule.go: Parse, Next and Describe.
//   - due.go: Due, which decides whether a job runs now, given its last run,
//     and how late it is: a run missed while the Mac slept runs once if it
//     is no more than CatchUp late.
//   - scheduler.go: Scheduler, the loop that asks Due about each job every
//     Tick, starts runs one at a time, and runs, stops and lists them for
//     merud's ops.
//
// What it doesn't do: it knows no model, store or config file. merud gives
// it the jobs, each one's last run from the store, and a function that runs
// a job through the agent; it records each run itself. With nothing else in
// sight, the tests drive it with any clock they like. It has no seconds
// field, no @reboot or @daily names, and no time zone but the computer's
// own.
package jobs
