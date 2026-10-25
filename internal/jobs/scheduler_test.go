// This file tests Scheduler with a fake clock and a fast tick: runs on
// time, after a sleep, never after a pause, Run now, Cancel, and one run at
// a time.

package jobs

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// clock is a fake time.Now the test moves by hand. A mutex guards it,
// since the loop reads it from its own goroutine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// recorder is a RunFunc that records each run and can hold one until told
// to go, or until its context ends.
type recorder struct {
	mu   sync.Mutex
	runs []string // "name late=..."
	hold chan struct{}
	ran  chan string
}

func newRecorder() *recorder { return &recorder{ran: make(chan string, 10)} }

func (r *recorder) run(ctx context.Context, name string, how Start) Result {
	r.mu.Lock()
	r.runs = append(r.runs, name+" late="+how.Late.Round(time.Minute).String())
	hold := r.hold
	r.mu.Unlock()
	r.ran <- name
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return Result{Outcome: "cancelled", Session: "s-" + name}
		}
	}
	return Result{Outcome: "ok", Session: "s-" + name}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

// started starts a Scheduler on a fake clock at start, with one job, and
// returns it, the clock, the recorder and a stop function.
func started(t *testing.T, start time.Time, last map[string]time.Time, entries ...Entry) (*Scheduler, *clock, *recorder, func()) {
	t.Helper()
	c := &clock{t: start}
	r := newRecorder()
	s := New(r.run, last, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now, s.tick = c.now, 5*time.Millisecond
	s.Set(entries)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Loop(ctx); close(done) }()
	return s, c, r, func() { cancel(); <-done }
}

// waitRun waits for the next run to start, or fails.
func waitRun(t *testing.T, r *recorder) string {
	t.Helper()
	select {
	case name := <-r.ran:
		return name
	case <-time.After(2 * time.Second):
		t.Fatal("no run started")
	}
	return ""
}

// noRun checks that no run starts for a few ticks.
func noRun(t *testing.T, r *recorder) {
	t.Helper()
	select {
	case name := <-r.ran:
		t.Fatalf("%s ran", name)
	case <-time.After(50 * time.Millisecond):
	}
}

func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	s, err := Parse(expr, 0, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSchedulerRunsOnTimeAndAfterSleep(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }
	brief := Entry{Name: "brief", Schedule: mustParse(t, "0 7 * * *"), On: true}
	s, c, r, stop := started(t, at(26, 6, 59), map[string]time.Time{"brief": at(25, 7, 0)}, brief)
	defer stop()

	noRun(t, r)
	c.set(at(26, 7, 0))
	if waitRun(t, r) != "brief" {
		t.Fatal("wrong job")
	}
	noRun(t, r) // once per slot

	// Asleep through the next morning, awake at 8:30: it runs late, once.
	c.set(at(27, 8, 30))
	waitRun(t, r)
	r.mu.Lock()
	got := r.runs[len(r.runs)-1]
	r.mu.Unlock()
	if got != "brief late=1h30m0s" {
		t.Errorf("late run = %q", got)
	}

	// Off all of the next day: 13 hours late is skipped.
	c.set(at(28, 20, 0))
	noRun(t, r)
	if st := s.Status(); len(st) != 1 || st[0].LastOutcome != "ok" || st[0].LastSession != "s-brief" {
		t.Errorf("Status = %+v", st)
	}
}

func TestSchedulerPauseDoesntCatchUp(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 26, h, 0, 0, 0, time.UTC) }
	on := Entry{Name: "hourly", Schedule: mustParse(t, "0 * * * *"), On: true}
	off := on
	off.On = false
	s, c, r, stop := started(t, at(6), map[string]time.Time{"hourly": at(6)}, off)
	defer stop()

	c.set(at(9)) // three slots pass while paused
	noRun(t, r)
	s.Set([]Entry{on}) // resume at 9:00: counts from now
	noRun(t, r)
	c.set(at(10))
	waitRun(t, r)
}

func TestSchedulerRunNowAndCancel(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	brief := Entry{Name: "brief", Schedule: mustParse(t, "0 7 * * *"), On: true}
	s, _, r, stop := started(t, now, map[string]time.Time{"brief": now}, brief)
	defer stop()

	if err := s.RunNow("nope"); err == nil {
		t.Error("RunNow on an unknown job worked")
	}
	if err := s.Cancel("brief"); err == nil {
		t.Error("Cancel with no run worked")
	}

	r.mu.Lock()
	r.hold = make(chan struct{}) // the run waits until cancelled
	r.mu.Unlock()
	if err := s.RunNow("brief"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, r)
	if err := s.RunNow("brief"); err == nil {
		t.Error("a second RunNow while it runs worked")
	}
	if st := s.Status(); !st[0].Running {
		t.Error("Status doesn't show the run")
	}
	if err := s.Cancel("brief"); err != nil {
		t.Fatal(err)
	}
	// The run returns "cancelled", and the job's last run stays.
	deadline := time.Now().Add(2 * time.Second)
	for s.Status()[0].Running {
		if time.Now().After(deadline) {
			t.Fatal("run didn't stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := s.Status()[0]; st.LastOutcome != "cancelled" || !st.Last.Equal(now) {
		t.Errorf("after Cancel: %+v", st)
	}
}

func TestSchedulerOneAtATime(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 26, h, 0, 0, 0, time.UTC) }
	a := Entry{Name: "a", Schedule: mustParse(t, "0 7 * * *"), On: true}
	b := Entry{Name: "b", Schedule: mustParse(t, "0 7 * * *"), On: true}
	_, c, r, stop := started(t, at(6), map[string]time.Time{"a": at(6), "b": at(6)}, a, b)
	defer stop()

	r.mu.Lock()
	hold := make(chan struct{})
	r.hold = hold
	r.mu.Unlock()
	c.set(at(7))
	waitRun(t, r)
	noRun(t, r) // the second waits its turn
	r.mu.Lock()
	r.hold = nil
	r.mu.Unlock()
	close(hold)
	waitRun(t, r)
	if r.count() != 2 {
		t.Errorf("runs = %d, want 2", r.count())
	}
}

func TestSchedulerEditCountsFromNow(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 26, h, 0, 0, 0, time.UTC) }
	daily := Entry{Name: "brief", Schedule: mustParse(t, "0 7 * * *"), On: true}
	s, c, r, stop := started(t, at(9), map[string]time.Time{"brief": at(7)}, daily)
	defer stop()

	// At 9:30 the job becomes hourly. Its 8:00 and 9:00 slots fell before
	// the edit, so neither runs; 10:00 does.
	c.set(at(9).Add(30 * time.Minute))
	hourly := daily
	hourly.Schedule = mustParse(t, "0 * * * *")
	s.Set([]Entry{hourly})
	noRun(t, r)
	c.set(at(10))
	waitRun(t, r)
}
