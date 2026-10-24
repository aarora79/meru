// This file tests Due: on-time runs, missed runs while asleep, and the
// catch-up limit.

package jobs

import (
	"testing"
	"time"
)

func TestDue(t *testing.T) {
	ny := newYork(t)
	weekday7, err := Parse("0 7 * * 1-5", 0, "", ny)
	if err != nil {
		t.Fatal(err)
	}
	hourly, err := Parse("0 * * * *", 0, "", ny)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		s        Schedule
		last     string
		now      string
		wantRun  bool
		wantSlot string
		wantLate time.Duration
		wantSkip string
	}{
		{name: "not yet", s: weekday7, last: "2026-10-26 07:00", now: "2026-10-26 18:00"},
		{name: "on time", s: weekday7, last: "2026-10-26 07:00", now: "2026-10-27 07:00",
			wantRun: true, wantSlot: "2026-10-27 07:00"},
		{name: "asleep until 8:30 runs late", s: weekday7, last: "2026-10-26 07:00", now: "2026-10-27 08:30",
			wantRun: true, wantSlot: "2026-10-27 07:00", wantLate: 90 * time.Minute},
		{name: "exactly twelve hours late still runs", s: weekday7, last: "2026-10-26 07:00", now: "2026-10-27 19:00",
			wantRun: true, wantSlot: "2026-10-27 07:00", wantLate: 12 * time.Hour},
		{name: "thirteen hours late is skipped", s: weekday7, last: "2026-10-26 07:00", now: "2026-10-27 20:00",
			wantSkip: "2026-10-27 07:00"},
		{name: "a weekend asleep skips Monday, nothing else to run", s: weekday7, last: "2026-10-23 07:00", now: "2026-10-26 22:00",
			wantSkip: "2026-10-26 07:00"},
		{name: "many missed hourly runs collapse to the latest", s: hourly, last: "2026-10-26 23:00", now: "2026-10-27 06:20",
			wantRun: true, wantSlot: "2026-10-27 06:00", wantLate: 20 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Due(tt.s, at(t, ny, tt.last), at(t, ny, tt.now))
			if d.Run != tt.wantRun {
				t.Fatalf("Run = %v, want %v (%+v)", d.Run, tt.wantRun, d)
			}
			if tt.wantRun && (!d.Slot.Equal(at(t, ny, tt.wantSlot)) || d.Late != tt.wantLate) {
				t.Errorf("Slot %v Late %v; want %s and %v", d.Slot, d.Late, tt.wantSlot, tt.wantLate)
			}
			if tt.wantSkip != "" && !d.Skipped.Equal(at(t, ny, tt.wantSkip)) {
				t.Errorf("Skipped = %v, want %s", d.Skipped, tt.wantSkip)
			}
			if tt.wantSkip == "" && !d.Skipped.IsZero() {
				t.Errorf("Skipped = %v, want none", d.Skipped)
			}
		})
	}
}
