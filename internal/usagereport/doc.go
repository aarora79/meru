// Package usagereport sends Meru's one anonymous usage report: seven coarse
// fields, at most once a UTC day, and only after meru chat or Meru.app has
// shown the user the notice (ARCHITECTURE.md, "Anonymous usage report").
//
// The files:
//
//   - report.go: Report, the whole body merud sends, and the buckets its
//     age and question fields come from.
//   - log.go: Entry, one line of ~/.meru/usage-report.jsonl, and readState,
//     which reads that file once for each decision.
//   - send.go: Reporter, its Loop, the rule that decides whether to send
//     now, and the one HTTP client.
//   - status.go: Status, what the clients show: the banner, the choice, the
//     next report and the recent lines of the file.
//
// The file usage-report.jsonl is the record. It holds a "seen" line when a
// client showed the notice, a "dismissed" line when the user closed it, and
// a "sent" or "failed" line for each send, with the exact body that went.
// Nothing about the report lives in meru.db.
//
// What it doesn't do: it reads no config file and writes none. merud passes
// the choice from [usage_report] in as Settings on each tick and changes the
// choice itself. It sends no ID, no text and no paths. It retries a failed
// send on the next tick and keeps no queue, so a missed day stays missed.
// Development builds, whose version isn't a release such as v0.7.3, never
// send.
package usagereport
