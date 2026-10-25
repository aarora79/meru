// This file tests Parse, Next and Describe.

package jobs

import (
	"strings"
	"testing"
	"time"
	// The blank import embeds the time zone database in the test binary, so
	// the daylight saving tests read the same rules on every CI machine.
	_ "time/tzdata"
)

// newYork is a zone with daylight saving time, for the clock-change tests.
func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// at builds a time in loc from "2026-10-24 07:00".
func at(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParse(t *testing.T) {
	tests := []struct {
		expr    string
		weeks   int
		from    string
		wantErr string // "" for no error; else a part of the message
	}{
		{expr: "0 7 * * 1-5"},
		{expr: "*/15 * * * *"},
		{expr: "0,30 9-17 * * mon-fri"},
		{expr: "0 9 * jan,jul sun"},
		{expr: "5/15 * * * *"},
		{expr: "0 0 1 * *"},
		{expr: "0 9 * * 7"}, // Sunday as 7
		{expr: "0 9 * * 2", weeks: 2, from: "2026-10-27"},
		{expr: "0 7 * *", wantErr: "has 4 fields"},
		{expr: "60 7 * * *", wantErr: "minute: 60 is past 59"},
		{expr: "0 25 * * *", wantErr: "hour: 25 is past 23"},
		{expr: "0 7 0 * *", wantErr: "day of month: 0 is below 1"},
		{expr: "0 7 * foo *", wantErr: `month: "foo" isn't a number`},
		{expr: "0 7 * * 5-1", wantErr: "runs backward"},
		{expr: "*/0 * * * *", wantErr: "step after /"},
		{expr: "0 7 ,1 * *", wantErr: "empty item"},
		{expr: "0 0 31 2 *", wantErr: "never matches"},
		{expr: "0 9 * * 2", weeks: 2, wantErr: "every_weeks needs from"},
		{expr: "0 9 * * 2", weeks: 2, from: "27 Oct", wantErr: "isn't a date"},
		{expr: "0 9 * * 2", from: "2026-10-27", wantErr: "set every_weeks"},
		{expr: "0 9 * * 2", weeks: 53, from: "2026-10-27", wantErr: "isn't between 1 and 52"},
	}
	for _, tt := range tests {
		t.Run(tt.expr+" "+tt.wantErr, func(t *testing.T) {
			_, err := Parse(tt.expr, tt.weeks, tt.from, time.Local)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Parse: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("Parse error = %v, want one with %q", err, tt.wantErr)
			}
		})
	}
}

func TestNext(t *testing.T) {
	ny := newYork(t)
	tests := []struct {
		name  string
		expr  string
		after string
		want  string
	}{
		{"weekday at 7, from Friday evening", "0 7 * * 1-5", "2026-10-23 18:00", "2026-10-26 07:00"},
		{"the same minute isn't after", "0 7 * * 1-5", "2026-10-26 07:00", "2026-10-27 07:00"},
		{"every 15 minutes", "*/15 * * * *", "2026-10-24 10:07", "2026-10-24 10:15"},
		{"month end rolls over", "0 9 1 * *", "2026-10-31 10:00", "2026-11-01 09:00"},
		{"leap day", "0 9 29 2 *", "2026-03-01 00:00", "2028-02-29 09:00"},
		{"day of month or day of week", "0 9 15 * 1", "2026-10-13 10:00", "2026-10-15 09:00"},
		{"Sunday written 7", "0 9 * * 7", "2026-10-24 10:00", "2026-10-25 09:00"},
		// Clocks jump from 2:00 to 3:00 on 8 March 2026: 2:30 doesn't exist.
		{"spring forward skips the missing time", "30 2 * * *", "2026-03-07 03:00", "2026-03-09 02:30"},
		// Clocks fall back from 2:00 to 1:00 on 1 November 2026: 1:30 happens twice.
		{"fall back runs once", "30 1 * * *", "2026-11-01 00:00", "2026-11-01 01:30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.expr, 0, "", ny)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := s.Next(at(t, ny, tt.after))
			if want := at(t, ny, tt.want); !ok || !got.Equal(want) {
				t.Errorf("Next = %v, %v; want %v", got, ok, want)
			}
		})
	}

	// After the first 1:30 on the fall-back night, the next is the 2nd's.
	s, _ := Parse("30 1 * * *", 0, "", ny)
	first, _ := s.Next(at(t, ny, "2026-11-01 00:00"))
	if next, _ := s.Next(first); !next.Equal(at(t, ny, "2026-11-02 01:30")) {
		t.Errorf("after the first 1:30, Next = %v; want 2 November 01:30, not the repeated hour", next)
	}
}

func TestEveryWeeks(t *testing.T) {
	ny := newYork(t)
	// Tuesday 27 October 2026, every other week.
	s, err := Parse("0 9 * * 2", 2, "2026-10-27", ny)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	c := at(t, ny, "2026-10-01 00:00") // weeks before from never match
	for range 4 {
		c, _ = s.Next(c)
		got = append(got, c.Format("2006-01-02"))
	}
	want := []string{"2026-10-27", "2026-11-10", "2026-11-24", "2026-12-08"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("runs = %v, want %v", got, want)
	}

	// from is the first day it may run, and the weeks count from the
	// Monday of its week. From Thursday 29 October, the Tuesday two days
	// earlier is too soon, and the first run is two weeks after it.
	s, _ = Parse("0 9 * * 2", 2, "2026-10-29", ny)
	if c, _ := s.Next(at(t, ny, "2026-10-01 00:00")); c.Format("2006-01-02") != "2026-11-10" {
		t.Errorf("from a Thursday: first run %v, want 2026-11-10", c)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		expr  string
		weeks int
		from  string
		want  string
	}{
		{"0 7 * * 1-5", 0, "", "7:00 on weekdays"},
		{"0 9 * * 2", 2, "2026-10-27", "9:00 on Tuesdays, every other week from 27 October 2026"},
		{"0 9 * * 2", 3, "2026-10-27", "9:00 on Tuesdays, every 3 weeks from 27 October 2026"},
		{"*/15 * * * *", 0, "", "every 15 minutes every day"},
		{"30 * * * *", 0, "", "at :30 every hour every day"},
		{"0 10 * * sat,sun", 0, "", "10:00 on weekends"},
		{"0 8 * * mon,thu", 0, "", "8:00 on Mondays and Thursdays"},
		{"0 9 1 * *", 0, "", "9:00 on day 1 of the month"},
		{"0 9 1,15 * *", 0, "", "9:00 on days 1 and 15 of the month"},
		{"0 9 * jan,jul *", 0, "", "9:00 every day, in January and July"},
		{"0,30 9-17 * * *", 0, "", "at 0,30 9-17 * * *"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			s, err := Parse(tt.expr, tt.weeks, tt.from, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Describe(); got != tt.want {
				t.Errorf("Describe = %q, want %q", got, tt.want)
			}
		})
	}
}
