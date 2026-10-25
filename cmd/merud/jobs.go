// This file holds the scheduled jobs: building the scheduler from [[jobs]],
// running one job through the agent, and the jobs, job_run and job_cancel
// ops the clients' Jobs lists use. internal/jobs decides when; this file
// does the run. See ARCHITECTURE.md, "Scheduler".

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/jobs"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

// lateNote is how late a run must start before its prompt says so. A run
// on time starts within one tick of its slot.
const lateNote = 2 * time.Minute

// jobService owns the scheduler and runs each job.
type jobService struct {
	sched *jobs.Scheduler
	agent *agent.Agent
	hist  historyService
	log   *slog.Logger

	// mu guards jobs, the [[jobs]] entries by name.
	mu   sync.Mutex
	jobs map[string]config.Job
}

// newJobService builds the scheduler from cfg's [[jobs]], with each job's
// last run read from the store, so a restart doesn't run a job twice. The
// runs go through a and land in the chat folder hist keeps. It fails when
// the store can't be read; config has already checked every schedule.
func newJobService(ctx context.Context, cfg config.Config, a *agent.Agent, st *store.Store, hist historyService, log *slog.Logger) (*jobService, error) {
	last, err := st.LastJobRuns(ctx)
	if err != nil {
		return nil, err
	}
	s := &jobService{agent: a, hist: hist, log: log}
	s.sched = jobs.New(s.run, last, log)
	if err := s.set(cfg.Jobs); err != nil {
		return nil, err
	}
	if len(cfg.Jobs) > 0 {
		log.Info("jobs", "count", len(cfg.Jobs))
	}
	return s, nil
}

// set hands the scheduler the jobs in js. It fails for a schedule that
// doesn't parse, which config's check has already refused.
func (s *jobService) set(js []config.Job) error {
	entries := make([]jobs.Entry, 0, len(js))
	byName := make(map[string]config.Job, len(js))
	for _, j := range js {
		sch, err := j.Parse(time.Local)
		if err != nil {
			return fmt.Errorf("jobs.%s: %w", j.Name, err)
		}
		entries = append(entries, jobs.Entry{Name: j.Name, Schedule: sch, On: j.On()})
		byName[j.Name] = j
	}
	s.mu.Lock()
	s.jobs = byName
	s.mu.Unlock()
	s.sched.Set(entries)
	return nil
}

// run runs the job named name once, as the scheduler asks: an ask with
// source "job" through the agent, in a new chat in the Jobs folder, with
// nobody to approve a call. A run that starts more than lateNote late says
// so first, so the answer can say what it covers. It returns how the run
// ended and its chat, and records the metric, the span and one log line.
func (s *jobService) run(ctx context.Context, name string, how jobs.Start) jobs.Result {
	s.mu.Lock()
	j, ok := s.jobs[name]
	s.mu.Unlock()
	if !ok {
		return jobs.Result{Outcome: "error"}
	}
	start := time.Now()
	ctx, span := obs.Tracer().Start(ctx, "meru.job")
	defer span.End()

	// The Jobs folder must be in the folder list for the clients to show
	// it; the history service's lock covers every change to that list.
	s.hist.mu.Lock()
	err := s.hist.addFolder(config.JobsFolder)
	s.hist.mu.Unlock()
	if err != nil {
		s.log.WarnContext(ctx, "jobs folder not added", "err", err)
	}

	text := j.Prompt
	if how.Late > lateNote {
		text = fmt.Sprintf("(This scheduled run started %s late: the computer was asleep or Meru wasn't running.)\n\n%s",
			roughly(how.Late), text)
	}
	var res jobs.Result
	var said string // an error event's text
	emit := func(ev rpc.Event) error {
		switch ev.Type {
		case rpc.EventSession:
			res.Session = ev.Session
		case rpc.EventDone:
			res.Outcome = ev.Outcome
		case rpc.EventError:
			said = ev.Error
		}
		return nil
	}
	// A nil approve: nobody watches a job, so dispatch declines every call
	// that would ask.
	req := rpc.Request{Op: rpc.OpAsk, Source: rpc.SourceJob, Text: text, Job: j.Name, Skill: j.Skill}
	err = s.agent.Handle(ctx, req, emit, nil)
	switch {
	case err != nil && ctx.Err() != nil:
		res.Outcome = "cancelled"
	case err != nil || said != "":
		res.Outcome = "error"
	case res.Outcome == "":
		res.Outcome = "ok"
	}

	obs.RecordJobRun(context.WithoutCancel(ctx), j.Name, res.Outcome, how.Late, how.Manual)
	span.SetAttributes(
		attribute.String("meru.job.name", j.Name),
		attribute.String("meru.job.kind", jobKind(j)),
		attribute.Int64("meru.job.late_s", int64(how.Late.Seconds())),
		attribute.Bool("meru.job.manual", how.Manual),
		attribute.String("meru.job.outcome", res.Outcome),
		attribute.String("meru.session.id", res.Session),
	)
	args := []any{"job", j.Name, "outcome", res.Outcome, "session", res.Session,
		"late_ms", how.Late.Milliseconds(), "manual", how.Manual, "ms", time.Since(start).Milliseconds()}
	if err != nil && !errors.Is(err, context.Canceled) {
		args = append(args, "err", err)
	}
	s.log.InfoContext(ctx, "job ran", args...)
	return res
}

// jobKind says what a job does, for its span: "skill" or "prompt".
func jobKind(j config.Job) string {
	if j.Skill != "" {
		return "skill"
	}
	return "prompt"
}

// roughly writes a lateness the way a person would: "2 hours 10 minutes",
// "45 minutes".
func roughly(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	plural := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	switch {
	case h == 0:
		return plural(m, "minute")
	case m == 0:
		return plural(h, "hour")
	}
	return plural(h, "hour") + " " + plural(m, "minute")
}

// handleJobs answers OpJobs with one "jobs" event.
func (s *jobService) handleJobs(emit func(rpc.Event) error) error {
	return emit(rpc.Event{Type: rpc.EventJobs, Jobs: s.infos()})
}

// handleJobRun answers OpJobRun: it starts the job named in req.ID now and
// replies with the list, which shows it running. It fails for an unknown
// job or one already running.
func (s *jobService) handleJobRun(req rpc.Request, emit func(rpc.Event) error) error {
	if err := s.sched.RunNow(req.ID); err != nil {
		return err
	}
	// The loop takes the request within a moment; wait briefly so the list
	// shows the run.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !s.running(req.ID) {
		time.Sleep(20 * time.Millisecond)
	}
	return s.handleJobs(emit)
}

// handleJobCancel answers OpJobCancel: it stops the run of the job named in
// req.ID and replies with the list. It fails when the job has no run going.
func (s *jobService) handleJobCancel(req rpc.Request, emit func(rpc.Event) error) error {
	if err := s.sched.Cancel(req.ID); err != nil {
		return err
	}
	return s.handleJobs(emit)
}

// running reports whether the job named name has a run going.
func (s *jobService) running(name string) bool {
	for _, st := range s.sched.Status() {
		if st.Name == name {
			return st.Running
		}
	}
	return false
}

// infos returns every job for the clients, with its schedule in words.
func (s *jobService) infos() []rpc.JobInfo {
	s.mu.Lock()
	byName := s.jobs
	s.mu.Unlock()
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(time.RFC3339)
	}
	var out []rpc.JobInfo
	for _, st := range s.sched.Status() {
		j := byName[st.Name]
		when := j.Schedule
		if sch, err := j.Parse(time.Local); err == nil {
			when = sch.Describe()
		}
		out = append(out, rpc.JobInfo{
			Name: j.Name, Schedule: j.Schedule, When: when, Prompt: j.Prompt, Skill: j.Skill,
			Paused: !j.On(), Next: stamp(st.Next), Last: stamp(st.Last),
			LastOutcome: st.LastOutcome, LastSession: st.LastSession,
			Running: st.Running, Started: stamp(st.Started),
		})
	}
	return out
}
