// This file holds Report, the body merud sends, and the functions that turn
// the install's age and yesterday's question count into the report's coarse
// buckets.

package usagereport

import (
	"regexp"
	"time"
)

// Version is the report's format. Raise it whenever a field or a value
// changes. A new version makes both clients show the notice again, and
// merud sends nothing under it until one has. The collector accepts only
// the versions it knows (collector/worker.js).
const Version = 1

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
