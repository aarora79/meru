// This file holds Report, the body merud sends, and the functions that turn
// the install's age, yesterday's question count, the days it was used and its
// weeks of reports into the report's coarse buckets, and the UTC week and
// month the flags count in.

package usagereport

import (
	"regexp"
	"time"
)

// Version is the report's format. Raise it whenever a field or a value
// changes. A new version makes both clients show the notice again, and
// merud sends nothing under it until one has. The collector accepts only
// the versions it knows (collector/worker.js). Version 2 added the six
// fields after Questions, so the collector can count installs across days
// with no ID (ARCHITECTURE.md, "Anonymous usage report").
const Version = 2

// Report is the whole body merud sends. Every field holds a value from a
// short, fixed list, so the collector can refuse anything else.
//
// The `json:"os"` text after each field is a struct tag: it tells
// encoding/json the key to write for that field.
type Report struct {
	V         int    `json:"v"`         // always Version
	Meru      string `json:"meru"`      // the release, such as "0.7.5"
	OS        string `json:"os"`        // "darwin", "linux" or "windows"
	Arch      string `json:"arch"`      // "arm64" or "amd64"
	Profile   string `json:"profile"`   // "lite" or "full"
	Age       string `json:"age"`       // see ageBucket
	Questions string `json:"questions"` // see questionBucket

	// The six fields below came with Version 2. Each comes from this
	// install's own record and turns table, so none needs an ID.
	ActiveDays string `json:"active_days"` // see activeBucket
	Streak     string `json:"streak"`      // see streakBucket
	Previous   string `json:"previous"`    // the version this install last reported, or "none"
	WeekFirst  bool   `json:"week_first"`  // the first report of this ISO week
	MonthFirst bool   `json:"month_first"` // the first report of this UTC month
	First      bool   `json:"first"`       // the first report ever
}

// noPrevious is Previous when the install has sent no report before.
const noPrevious = "none"

// numberPattern matches a release number without its "v", such as "0.7.6",
// as a sent body's meru field holds it.
var numberPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// previousOf returns v, the meru field of this install's last sent report,
// when it is a release number, and noPrevious otherwise. The record is the
// user's own file, so a hand-edited body can hold anything; only a number
// leaves the machine.
func previousOf(v string) string {
	if numberPattern.MatchString(v) && len(v) <= 16 {
		return v
	}
	return noPrevious
}

// releasePattern matches a release version as `make release` stamps it,
// such as "v0.7.5", and captures the number without the "v".
var releasePattern = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)

// releaseNumber returns "0.7.5" for the release version "v0.7.5". For any
// other version, such as a development build's "(devel) 5325b3e" or a
// fork's "v0.7.5-acme", it returns false, and merud never sends.
func releaseNumber(version string) (string, bool) {
	m := releasePattern.FindStringSubmatch(version)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ageBucket says how long ago first was, the time of the first line in
// usage-report.jsonl: "week" under 7 days, "month" under 31, "year" under
// 366, and "older" after that. Four buckets are enough to tell a new
// install from one that has run for months, and too few to tell one
// install from another.
func ageBucket(first, now time.Time) string {
	days := now.Sub(first).Hours() / 24
	switch {
	case days < 7:
		return "week"
	case days < 31:
		return "month"
	case days < 366:
		return "year"
	default:
		return "older"
	}
}

// questionBucket puts n, yesterday's question count, into one of four
// buckets: "0", "1-5", "6-20" or "21+".
func questionBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 5:
		return "1-5"
	case n <= 20:
		return "6-20"
	default:
		return "21+"
	}
}

// startOfDay returns midnight UTC at the start of t's UTC day. The report
// counts days in UTC, so a user who travels doesn't send twice in one day.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// activeBucket puts n, the days in the last seven with any question, into
// "0", "1-2", "3-5" or "6-7".
func activeBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 2:
		return "1-2"
	case n <= 5:
		return "3-5"
	default:
		return "6-7"
	}
}

// streakBucket puts n, the ISO weeks in a row with a report, this one
// included, into "1", "2-4", "5-12" or "13+".
func streakBucket(n int) string {
	switch {
	case n <= 1:
		return "1"
	case n <= 4:
		return "2-4"
	case n <= 12:
		return "5-12"
	default:
		return "13+"
	}
}

// maxStreakWeeks is how far back the streak looks: past it, the bucket is
// "13+" whatever came before, so the record needs no older days.
const maxStreakWeeks = 13

// weekStart returns 00:00 UTC on the Monday of t's ISO week. Weekday counts
// Sunday as 0, and an ISO week starts on Monday, so Sunday goes back six
// days and Monday none. A week that starts in December and ends in January
// stays one week, as ISO 8601 counts it.
func weekStart(t time.Time) time.Time {
	d := startOfDay(t)
	back := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -back)
}

// monthStart returns 00:00 UTC on the first of t's UTC month.
func monthStart(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}
