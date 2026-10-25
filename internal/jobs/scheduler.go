// This file holds Scheduler: the loop that asks Due about each job every
// Tick, starts the runs, and answers the ops that list, start and stop
// them.

package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Tick is how often the loop looks at the jobs. It compares wall-clock
// times each tick, so a run that fell due while the computer slept starts
// within one tick of waking, whatever Go's timers do across sleep.
const Tick = 30 * time.Second

// Entry is one job as the scheduler sees it: its name, its schedule, and
// whether it is on. merud builds the entries from [[jobs]].
type Entry struct {
	Name     string
	Schedule Schedule
	On       bool
}

// Result is how one run ended, as RunFunc reports it: an outcome such as
// "ok", "error", "timeout" or "cancelled", and the run's chat.
type Result struct {
	Outcome string
	Session string
}

// Start says how a run began: Late is how long after its scheduled time
// it starts, and Manual is true for Run now, which has no scheduled time.
type Start struct {
	Late   time.Duration
	Manual bool
}

// RunFunc runs the job named name and reports how it went. ctx ends when
// the user stops the run or merud stops.
type RunFunc func(ctx context.Context, name string, start Start) Result

// Status is one job's state for the clients.
type Status struct {
	Name string
	// Next is the next scheduled run, zero for a paused job.
	Next time.Time
	// Last is when the latest run started, and LastOutcome and LastSession
	// how it ended and its chat; empty before the first run since merud
	// started, apart from Last, which the store remembers.
	Last        time.Time
	LastOutcome string
	LastSession string
	// Running is true while a run goes or waits its turn; Started is when
	// it started.
	Running bool
	Started time.Time
}

// inFlight is one run in progress: when it started and how to stop it.
type inFlight struct {
	started time.Time
	cancel  context.CancelFunc
}

// Scheduler decides when each job runs and runs it through a RunFunc. Loop
// owns the work; Set, RunNow, Cancel and Status may be called from any
// goroutine.
type Scheduler struct {
	run RunFunc
	log *slog.Logger
	// now is time.Now; tests give a fake clock. tick is Tick; tests shorten it.
	now  func() time.Time
	tick time.Duration
	// manual carries Run now requests to Loop, which starts every run, so
	// every run's context comes from Loop's.
	manual chan string
	// one lets one run go at a time: runs share the warm answer model.
	one chan struct{}

	// mu guards the fields below it.
	mu      sync.Mutex
	entries []Entry
	last    map[string]time.Time
	results map[string]Result
	running map[string]*inFlight
}

// New returns a Scheduler that runs jobs with run. last holds each job's
// last run, from the store; a job with none counts from when it is Set, so
// adding a job at 7:05 doesn't run its 7:00 slot.
func New(run RunFunc, last map[string]time.Time, log *slog.Logger) *Scheduler {
	if last == nil {
		last = map[string]time.Time{}
	}
	return &Scheduler{
		run: run, log: log, now: time.Now, tick: Tick,
		manual: make(chan string, 1),
		one:    make(chan struct{}, 1),
		last:   last, results: map[string]Result{}, running: map[string]*inFlight{},
	}
}

// Set replaces the jobs, as when config changes. A job new to the
// scheduler, with no last run, counts from now. A job switched back on
// after a pause counts from now too, so it doesn't catch up the slots it
// missed while paused.
func (s *Scheduler) Set(entries []Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	wasOn := map[string]bool{}
	for _, e := range s.entries {
		wasOn[e.Name] = e.On
	}
	for _, e := range entries {
		on, known := wasOn[e.Name]
		if _, ok := s.last[e.Name]; !ok || (known && !on && e.On) {
			s.last[e.Name] = now
		}
	}
	s.entries = entries
}

// Loop runs until ctx ends: every tick it starts each job that is due, and
// it starts each Run now request as it comes. When ctx ends it stops every
// run and waits for them to return.
func (s *Scheduler) Loop(ctx context.Context) {
	var wg sync.WaitGroup
	// defer runs wg.Wait when Loop returns: every run goroutine sees ctx
	// end and returns first.
	defer wg.Wait()
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		s.startDue(ctx, &wg)
		// select waits for whichever comes first: the end, a tick, or a
		// Run now request.
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case name := <-s.manual:
			s.start(ctx, &wg, name, Start{Manual: true})
		}
	}
}

// startDue starts every job Due says should run now, and moves the last
// run of each job whose missed slots were all too late.
func (s *Scheduler) startDue(ctx context.Context, wg *sync.WaitGroup) {
	now := s.now()
	s.mu.Lock()
	type due struct {
		name string
		late time.Duration
	}
	var starts []due
	for _, e := range s.entries {
		if !e.On || s.running[e.Name] != nil {
			continue
		}
		d := Due(e.Schedule, s.last[e.Name], now)
		switch {
		case d.Run:
			s.last[e.Name] = d.Slot
			starts = append(starts, due{e.Name, d.Late})
		case !d.Skipped.IsZero():
			s.last[e.Name] = now
			s.log.InfoContext(ctx, "job skipped", "job", e.Name, "slot", d.Skipped,
				"reason", fmt.Sprintf("more than %s late", CatchUp))
		}
	}
	s.mu.Unlock()
	for _, d := range starts {
		s.start(ctx, wg, d.name, Start{Late: d.late})
	}
}

// start runs one job in its own goroutine, after any run ahead of it. A job
// that already runs or waits doesn't start again.
func (s *Scheduler) start(ctx context.Context, wg *sync.WaitGroup, name string, how Start) {
	// context.WithCancel gives the run its own way to stop, for Cancel,
	// under ctx, so merud stopping stops it too.
	rctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.running[name] != nil {
		s.mu.Unlock()
		cancel()
		return
	}
	s.running[name] = &inFlight{started: s.now(), cancel: cancel}
	s.mu.Unlock()

	// wg.Go starts the function in a goroutine and counts it, so Loop can
	// wait for it.
	wg.Go(func() {
		defer cancel()
		res := Result{Outcome: "cancelled"}
		select {
		case s.one <- struct{}{}:
			res = s.run(rctx, name, how)
			<-s.one
		case <-rctx.Done():
			// Stopped while waiting its turn.
		}
		s.mu.Lock()
		delete(s.running, name)
		s.results[name] = res
		s.mu.Unlock()
	})
}

// RunNow asks Loop to run the job now, outside its schedule. It doesn't
// move the job's last run. It fails for an unknown job, or one that runs
// or waits already.
func (s *Scheduler) RunNow(name string) error {
	s.mu.Lock()
	known := false
	for _, e := range s.entries {
		known = known || e.Name == name
	}
	busy := s.running[name] != nil
	s.mu.Unlock()
	switch {
	case !known:
		return fmt.Errorf("no job named %q", name)
	case busy:
		return fmt.Errorf("%s is running already", name)
	}
	// The channel holds one request. A select with a default sends only
	// when there's room, so a request never waits on a busy loop.
	select {
	case s.manual <- name:
		return nil
	default:
		return errors.New("another job is starting; try again in a moment")
	}
}

// Cancel stops the job's run in progress, or one waiting its turn. The run
// ends "cancelled"; the job's schedule stays as it was. It fails when the
// job has no run going.
func (s *Scheduler) Cancel(name string) error {
	s.mu.Lock()
	r := s.running[name]
	s.mu.Unlock()
	if r == nil {
		return errors.New(name + " isn't running")
	}
	r.cancel()
	return nil
}

// Status returns every job's state, by name.
func (s *Scheduler) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]Status, 0, len(s.entries))
	for _, e := range s.entries {
		st := Status{Name: e.Name, Last: s.last[e.Name]}
		if e.On {
			from := now
			if st.Last.After(now) {
				from = st.Last
			}
			st.Next, _ = e.Schedule.Next(from)
		}
		if r, ok := s.results[e.Name]; ok {
			st.LastOutcome, st.LastSession = r.Outcome, r.Session
		}
		if r := s.running[e.Name]; r != nil {
			st.Running, st.Started = true, r.started
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
