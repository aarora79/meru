// This file reads and writes usage-report.jsonl, the record of what the
// user saw and what merud sent. One JSON object per line, appended and never
// rewritten, the way a session transcript is.

package usagereport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// The kinds of line usage-report.jsonl holds.
const (
	EntrySeen      = "seen"      // a client showed the notice
	EntryDismissed = "dismissed" // the user closed the notice or chose
	EntrySent      = "sent"      // the collector took a report
	EntryFailed    = "failed"    // a send didn't reach the collector
)

// recentCount is how many lines Status returns, newest first: two weeks
// of daily sends.
const recentCount = 14

// Entry is one line of usage-report.jsonl. Body holds the exact bytes sent,
// as a string, so a reader can compare it with what left the machine.
type Entry struct {
	Type   string    `json:"type"`
	At     time.Time `json:"at"`
	Report int       `json:"report,omitempty"` // the Version a "seen" or "dismissed" line is for
	Client string    `json:"client,omitempty"` // "tui" or "desktop", on a "seen" line
	Body   string    `json:"body,omitempty"`   // on "sent" and "failed" lines
	Error  string    `json:"error,omitempty"`  // a short reason, on a "failed" line
}

// state is what the file says, read once per tick or status call. The send
// rule and Status both take a state, so the two can't read the file
// differently.
type state struct {
	first       time.Time // the first line; zero when the file is empty
	firstSeen   time.Time // the first "seen" line for the current Version
	dismissed   bool      // a "dismissed" line for the current Version exists
	lastSent    time.Time // the latest "sent" line
	sentEver    bool      // any "sent" line exists, for any version
	failedToday bool      // a "failed" line exists for now's UTC day
	recent      []Entry   // the last recentCount lines, newest first

	// sentDays is a set: the UTC days with a "sent" line, from the start of
	// the ISO week maxStreakWeeks-1 weeks before now's on. A map whose
	// values say "present" is how Go writes a set. Every key comes from
	// startOfDay, in UTC, so == on keys means the same day.
	sentDays map[time.Time]bool
	// lastVersion is the meru field of the newest "sent" body, any version,
	// or "" when no body says.
	lastVersion string
}

// sentSince reports whether a report went on or after day. lastSent is the
// newest report, so it alone answers.
func (st state) sentSince(day time.Time) bool {
	return st.sentEver && !st.lastSent.Before(day)
}

// weeksInARow returns the ISO weeks in a row with a report that end with
// the week starting thisWeek, which counts as one: the report being built
// belongs to it. It stops at the first earlier week with none, and at
// maxStreakWeeks.
func (st state) weeksInARow(thisWeek time.Time) int {
	n := 1
	for w := thisWeek.AddDate(0, 0, -7); n < maxStreakWeeks; w = w.AddDate(0, 0, -7) {
		if !st.sentIn(w, w.AddDate(0, 0, 7)) {
			break
		}
		n++
	}
	return n
}

// sentIn reports whether a report went on a day in [from, to).
func (st state) sentIn(from, to time.Time) bool {
	for d := range st.sentDays {
		if !d.Before(from) && d.Before(to) {
			return true
		}
	}
	return false
}

// readState reads the file at path and returns its state as of now. A line
// that doesn't parse is skipped, so a file damaged at its end still reads.
// A file with no readable "seen" line reads as "not seen", which stops
// sending rather than starting it. A missing file is an empty state, not an
// error.
func readState(path string, now time.Time) (state, error) {
	var st state
	f, err := os.Open(path) // #nosec G304 -- path is the Meru home plus FileName, set by New
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read %s: %w", path, err)
	}
	// defer runs f.Close() when readState returns, on every path.
	defer f.Close()

	today := startOfDay(now)
	horizon := weekStart(now).AddDate(0, 0, -7*(maxStreakWeeks-1))
	st.sentDays = map[time.Time]bool{}
	var all []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type == "" {
			continue
		}
		all = append(all, e)
		if st.first.IsZero() {
			st.first = e.At
		}
		switch e.Type {
		case EntrySeen:
			if e.Report == Version && st.firstSeen.IsZero() {
				st.firstSeen = e.At
			}
		case EntryDismissed:
			if e.Report == Version {
				st.dismissed = true
			}
		case EntrySent:
			st.sentEver = true
			if e.At.After(st.lastSent) || st.lastSent.IsZero() {
				st.lastSent = e.At
				st.lastVersion = bodyVersion(e.Body)
			}
			if day := startOfDay(e.At); !day.Before(horizon) {
				st.sentDays[day] = true
			}
		case EntryFailed:
			if !e.At.Before(today) {
				st.failedToday = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return st, fmt.Errorf("read %s: %w", path, err)
	}
	for i := len(all) - 1; i >= 0 && len(st.recent) < recentCount; i-- {
		st.recent = append(st.recent, all[i])
	}
	return st, nil
}

// bodyVersion returns the meru field of body, a report as sent, or "" when
// body doesn't parse or has none. Both v1 and v2 bodies have the field.
func bodyVersion(body string) string {
	var b struct {
		Meru string `json:"meru"`
	}
	if json.Unmarshal([]byte(body), &b) != nil {
		return ""
	}
	return b.Meru
}

// appendEntry adds e to the file at path as one line. The caller holds the
// Reporter's lock, so two lines never interleave. OpenFile's mode applies
// only when it creates the file, so Chmod sets 0600 on a file that already
// existed with a looser mode.
func appendEntry(path string, e Entry) (err error) {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path is the Meru home plus FileName, set by New
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// The named result err lets this deferred function report a failed
	// Close, which can be the first sign that a write didn't reach disk.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("write %s: %w", path, cerr)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
