// Tests for the six fields report version 2 added: the ISO week, the streak,
// the first-of-week, month and ever flags, the previous version, and the
// active-days bucket.

package usagereport

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// utc is a short way to write a UTC time in a table.
func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func TestWeekStart(t *testing.T) {
	for _, tc := range []struct {
		at, want time.Time
	}{
		{utc(2026, 11, 9, 10), utc(2026, 11, 9, 0)},                                                 // a Monday stays
		{utc(2026, 11, 11, 23), utc(2026, 11, 9, 0)},                                                // Wednesday
		{utc(2026, 11, 15, 23), utc(2026, 11, 9, 0)},                                                // Sunday goes back six days
		{utc(2026, 11, 16, 0), utc(2026, 11, 16, 0)},                                                // the next Monday starts a new week
		{utc(2027, 1, 1, 12), utc(2026, 12, 28, 0)},                                                 // a week that starts in December
		{utc(2026, 1, 1, 12), utc(2025, 12, 29, 0)},                                                 // ISO week 1 of 2026 starts in 2025
		{time.Date(2026, 11, 16, 1, 0, 0, 0, time.FixedZone("EST", -5*3600)), utc(2026, 11, 16, 0)}, // read in UTC
	} {
		if got := weekStart(tc.at); !got.Equal(tc.want) {
			t.Errorf("weekStart(%v) = %v, want %v", tc.at, got, tc.want)
		}
	}
	if got := monthStart(utc(2026, 11, 30, 23)); !got.Equal(utc(2026, 11, 1, 0)) {
		t.Errorf("monthStart = %v", got)
	}
}

func TestBucketsV2(t *testing.T) {
	for n, want := range map[int]string{0: "0", 1: "1-2", 2: "1-2", 3: "3-5", 5: "3-5", 6: "6-7", 7: "6-7"} {
		if got := activeBucket(n); got != want {
			t.Errorf("activeBucket(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{1: "1", 2: "2-4", 4: "2-4", 5: "5-12", 12: "5-12", 13: "13+"} {
		if got := streakBucket(n); got != want {
			t.Errorf("streakBucket(%d) = %q, want %q", n, got, want)
		}
	}
}

// sentAt is a "sent" line at t with a body that names version.
func sentAt(t time.Time, version string) Entry {
	return Entry{Type: EntrySent, At: t, Body: `{"v":1,"meru":"` + version + `"}`}
}

// TestFieldsV2 builds the report from records an install could have and
// checks the six new fields. now is Wednesday 11 November 2026.
func TestFieldsV2(t *testing.T) {
	now := utc(2026, 11, 11, 10)
	seen := seenAt(now.AddDate(0, 0, -40))
	tests := []struct {
		name                         string
		lines                        []Entry
		streak, previous             string
		weekFirst, monthFirst, first bool
	}{
		{name: "never sent", lines: []Entry{seen},
			streak: "1", previous: "none", weekFirst: true, monthFirst: true, first: true},
		{name: "sent Monday this week", lines: []Entry{seen, sentAt(utc(2026, 11, 9, 1), "0.7.6")},
			streak: "1", previous: "0.7.6", weekFirst: false, monthFirst: false, first: false},
		{name: "sent last week only", lines: []Entry{seen, sentAt(utc(2026, 11, 5, 1), "0.7.6")},
			streak: "2-4", previous: "0.7.6", weekFirst: true, monthFirst: false, first: false},
		{name: "sent two weeks back, on 31 October", lines: []Entry{seen, sentAt(utc(2026, 10, 31, 1), "0.7.6")},
			streak: "1", previous: "0.7.6", weekFirst: true, monthFirst: true, first: false},
		{name: "three weeks in a row", lines: []Entry{seen, sentAt(utc(2026, 10, 27, 1), "0.7.5"), sentAt(utc(2026, 11, 3, 1), "0.7.6")},
			streak: "2-4", previous: "0.7.6", weekFirst: true, monthFirst: false, first: false},
		{name: "a gap stops the streak", lines: []Entry{seen, sentAt(utc(2026, 10, 20, 1), "0.7.5")},
			streak: "1", previous: "0.7.5", weekFirst: true, monthFirst: true, first: false},
		{name: "a failed send this week isn't a report", lines: []Entry{seen, sentAt(utc(2026, 11, 3, 1), "0.7.6"), {Type: EntryFailed, At: utc(2026, 11, 10, 1)}},
			streak: "2-4", previous: "0.7.6", weekFirst: true, monthFirst: false, first: false},
		{name: "a hand-edited body sends none", lines: []Entry{seen, {Type: EntrySent, At: utc(2026, 11, 3, 1), Body: `{"meru":"rm -rf /"}`}},
			streak: "2-4", previous: "none", weekFirst: true, monthFirst: false, first: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTest(t, "v0.7.7", now, 0)
			writeLines(t, r, tc.lines...)
			st, err := readState(r.path, now)
			if err != nil {
				t.Fatal(err)
			}
			body, err := r.build(context.Background(), Settings{Send: "on", Profile: "lite"}, st, now)
			if err != nil {
				t.Fatal(err)
			}
			var rep Report
			if err := json.Unmarshal(body, &rep); err != nil {
				t.Fatal(err)
			}
			if rep.V != 2 || rep.ActiveDays != "3-5" {
				t.Errorf("v %d, active_days %q; want 2 and the fake's 4 days as 3-5", rep.V, rep.ActiveDays)
			}
			if rep.Streak != tc.streak || rep.Previous != tc.previous ||
				rep.WeekFirst != tc.weekFirst || rep.MonthFirst != tc.monthFirst || rep.First != tc.first {
				t.Errorf("got streak %q previous %q week_first %v month_first %v first %v; want %q %q %v %v %v",
					rep.Streak, rep.Previous, rep.WeekFirst, rep.MonthFirst, rep.First,
					tc.streak, tc.previous, tc.weekFirst, tc.monthFirst, tc.first)
			}
		})
	}
}

// TestStreakCaps checks that twenty weeks in a row read as 13+.
func TestStreakCaps(t *testing.T) {
	now := utc(2026, 11, 11, 10)
	r := newTest(t, "v0.7.7", now, 0)
	writeLines(t, r, seenAt(now.AddDate(0, 0, -200)))
	for w := 1; w <= 20; w++ {
		writeLines(t, r, sentAt(weekStart(now).AddDate(0, 0, -7*w+1), "0.7.6"))
	}
	st, _ := readState(r.path, now)
	if got := streakBucket(st.weeksInARow(weekStart(now))); got != "13+" {
		t.Errorf("twenty weeks gave %q, want 13+", got)
	}
}

// TestUpgradeAsksAgain checks the consent rule across the version change: a
// record whose notice was seen and closed under version 1 shows the notice
// again, and holds the report until a client shows it.
func TestUpgradeAsksAgain(t *testing.T) {
	now := utc(2026, 11, 11, 10)
	r := newTest(t, "v0.7.7", now, 0)
	writeLines(t, r,
		Entry{Type: EntrySeen, At: now.AddDate(0, 0, -5), Report: 1},
		Entry{Type: EntryDismissed, At: now.AddDate(0, 0, -5), Report: 1},
		sentAt(now.AddDate(0, 0, -1), "0.7.6"))
	st, err := r.Status(context.Background(), Settings{Send: "on", Profile: "lite"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Banner == "" || st.Blocked != reasonNotSeen || st.Version != 2 {
		t.Errorf("status = banner %q, blocked %q, version %d; want the notice again and nothing sent", st.Banner, st.Blocked, st.Version)
	}
}
