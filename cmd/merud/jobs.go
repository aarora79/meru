// This file holds the scheduled jobs: building the scheduler from [[jobs]],
// running one job through the agent, and the jobs, job_run, job_cancel,
// job_set and job_remove ops the clients' Jobs screens use. internal/jobs decides when; this file
// does the run. See ARCHITECTURE.md, "Scheduler".

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aarora79/meru/internal/agent"
	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/jobs"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

// lateNote is how late a run must start before its prompt says so. A run
// on time starts within one tick of its slot.
const lateNote = 2 * time.Minute

// jobService owns the scheduler and runs each job.
type jobService struct {
	sched      *jobs.Scheduler
	agent      *agent.Agent
	dispatcher *dispatch.Dispatcher
	st         *store.Store
	hist       historyService
	// sessionsDir is where a command run's transcript goes, and commands
	// the [[commands]] entries, for each parameter's kind.
	sessionsDir string
	commands    []config.Command
	log         *slog.Logger
	// configPath is the config.toml job_set and job_remove change, and
	// editConfig runs a change under the lock every config.toml write in
	// merud takes.
	configPath string
	editConfig func(func() error) error

	// mu guards jobs, the [[jobs]] entries by name.
	mu   sync.Mutex
	jobs map[string]config.Job
}

// newJobService builds the scheduler from cfg's [[jobs]], with each job's
// last run read from the store, so a restart doesn't run a job twice. A
// prompt runs through a, a command and an output file through d, and every
// run lands in the chat folder hist keeps. Job changes go into the file at
// configPath through editConfig. It fails when the store can't be read;
// config has already checked every schedule.
func newJobService(ctx context.Context, cfg config.Config, configPath string, editConfig func(func() error) error,
	a *agent.Agent, d *dispatch.Dispatcher, st *store.Store, hist historyService, log *slog.Logger) (*jobService, error) {
	last, err := st.LastJobRuns(ctx)
	if err != nil {
		return nil, err
	}
	s := &jobService{agent: a, dispatcher: d, st: st, hist: hist, sessionsDir: hist.dir, commands: cfg.Commands, log: log,
		configPath: configPath, editConfig: editConfig}
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

// run runs the job named name once, as the scheduler asks: a prompt
// through the agent, or a command through dispatch, in a new chat in the
// Jobs folder. Nobody watches a run, so its approver answers from the
// job's allow list (allowFor). An ok run with output set also writes its
// answer to that file. It returns how the run ended and its chat, and
// records the metric, the span and one log line.
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

	var res jobs.Result
	var answer string
	if j.Command != "" {
		res, answer, err = s.runCommand(ctx, j, start)
	} else {
		res, answer, err = s.runPrompt(ctx, j, how)
	}
	if res.Outcome == "ok" && j.Output != "" {
		if oerr := s.writeOutput(ctx, j, res.Session, answer, start); oerr != nil {
			res.Outcome = "error"
			s.log.WarnContext(ctx, "job's output not written", "job", j.Name, "err", oerr)
		}
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

// allowFor returns the approver for j's runs: it says "once" to a call
// whose tool j's allow list names, and "deny" to any other. dispatch asks
// it only about calls that ask once; one that asks every time stays
// declined in a job whatever allow says.
func allowFor(j config.Job) rpc.ApproveFunc {
	return func(_ context.Context, a rpc.Approval) (rpc.Choice, error) {
		if slices.Contains(j.Allow, a.Name) && slices.Contains(a.Choices, rpc.ChoiceOnce) {
			return rpc.ChoiceOnce, nil
		}
		return rpc.ChoiceDeny, nil
	}
}

// runPrompt runs j's prompt through the agent as an ask with source "job".
// A run that starts more than lateNote late says so first, so the answer
// can say what it covers. It returns the result, the answer's text for
// output, and Handle's error.
func (s *jobService) runPrompt(ctx context.Context, j config.Job, how jobs.Start) (jobs.Result, string, error) {
	text := j.Prompt
	if how.Late > lateNote {
		text = fmt.Sprintf("(This scheduled run started %s late: the computer was asleep or Meru wasn't running.)\n\n%s",
			roughly(how.Late), text)
	}
	var res jobs.Result
	var said string // an error event's text
	var answer strings.Builder
	emit := func(ev rpc.Event) error {
		switch ev.Type {
		case rpc.EventSession:
			res.Session = ev.Session
		case rpc.EventToken:
			answer.WriteString(ev.Text)
		case rpc.EventDone:
			res.Outcome = ev.Outcome
		case rpc.EventError:
			said = ev.Error
		}
		return nil
	}
	req := rpc.Request{Op: rpc.OpAsk, Source: rpc.SourceJob, Text: text, Job: j.Name, Skill: j.Skill}
	err := s.agent.Handle(ctx, req, emit, allowFor(j))
	switch {
	case err != nil && ctx.Err() != nil:
		res.Outcome = "cancelled"
	case err != nil || said != "":
		res.Outcome = "error"
	case res.Outcome == "":
		res.Outcome = "ok"
	}
	return res, answer.String(), err
}

// maxCommandAnswer caps how much of a command's output becomes the run's
// answer, as the tool_calls log keeps it.
const maxCommandAnswer = 4000

// runCommand runs j's [[commands]] entry with no model: a new session in
// the Jobs folder, a user line that names the command, the call through
// dispatch, which logs it and runs it only if allowed, and the output as
// the answer. It writes the run's turns row itself, as the agent does for
// a prompt. It returns the result, the output, and an error when the
// session can't be written.
func (s *jobService) runCommand(ctx context.Context, j config.Job, start time.Time) (jobs.Result, string, error) {
	sess, err := transcript.New(s.sessionsDir)
	if err != nil {
		return jobs.Result{Outcome: "error"}, "", err
	}
	res := jobs.Result{Session: sess.ID()}
	if err := sess.SetMeta(transcript.Meta{Folder: config.JobsFolder}); err != nil {
		return jobs.Result{Outcome: "error"}, "", err
	}
	tool := "cmd." + j.Command
	traceID := trace.SpanFromContext(ctx).SpanContext().TraceID().String()
	if err := sess.Append(transcript.Line{TS: start, Type: transcript.TypeUser, Text: "Run " + tool, Job: j.Name, TraceID: traceID}); err != nil {
		return jobs.Result{Outcome: "error"}, "", err
	}
	args, err := s.commandArgs(j)
	if err != nil {
		return jobs.Result{Outcome: "error"}, "", err
	}
	out, how := s.dispatcher.Dispatch(ctx, dispatch.Call{
		ID: fmt.Sprintf("job-%d", start.UnixNano()), Name: tool, Args: args, Session: sess.ID(),
		Source: rpc.SourceJob, Caller: dispatch.CallerMeru, Append: sess.Append, Approve: allowFor(j),
	})
	answer := out.Text
	if r := []rune(answer); len(r) > maxCommandAnswer {
		answer = string(r[:maxCommandAnswer]) + "…"
	}
	switch {
	case how.Outcome == dispatch.OutcomeOK && !out.IsError:
		res.Outcome = "ok"
	case how.Outcome == dispatch.OutcomeCancelled:
		res.Outcome = "cancelled"
	default:
		res.Outcome = "error"
	}
	ms := time.Since(start).Milliseconds()
	if err := sess.Append(transcript.Line{Type: transcript.TypeAssistant, Text: answer, Ms: ms, TraceID: traceID}); err != nil {
		return res, answer, err
	}
	if s.st != nil {
		row := store.Turn{Session: sess.ID(), Time: start, Source: string(rpc.SourceJob), DurationMillis: ms,
			ToolCalls: 1, TraceID: traceID, Job: j.Name}
		if err := s.st.InsertTurn(context.WithoutCancel(ctx), row); err != nil {
			s.log.WarnContext(ctx, "turn row not written; the transcript still has the run", "err", err)
		}
	}
	return res, answer, nil
}

// commandArgs turns j's args into the JSON object the command reads. An
// "int" parameter goes as a number, as the command's schema wants; every
// other kind goes as a string.
func (s *jobService) commandArgs(j config.Job) (json.RawMessage, error) {
	kinds := map[string]string{}
	for _, c := range s.commands {
		if c.Name == j.Command {
			for name, p := range c.Params {
				kinds[name] = p.Type
			}
		}
	}
	args := map[string]any{}
	for name, v := range j.Args {
		if kinds[name] == "int" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("jobs.%s: args.%s: %q isn't a whole number", j.Name, name, v)
			}
			args[name] = n
			continue
		}
		args[name] = v
	}
	return json.Marshal(args)
}

// writeOutput writes answer to j's output file under [skills] output_dir,
// with one write_file call through dispatch in the run's chat, so the file
// lands in the audit log beside the run. The user asked for the file when
// they set output, so this one call is approved; write_file's own check
// keeps it inside output_dir. A run replaces a file of the same name.
func (s *jobService) writeOutput(ctx context.Context, j config.Job, session, answer string, start time.Time) error {
	path := strings.NewReplacer("{date}", start.Format("2006-01-02"), "{time}", start.Format("1504"), "{job}", j.Name).Replace(j.Output)
	args, err := json.Marshal(map[string]any{"path": path, "content": answer, "overwrite": true})
	if err != nil {
		return err
	}
	sess, err := transcript.Open(s.sessionsDir, session)
	if err != nil {
		return err
	}
	yes := func(context.Context, rpc.Approval) (rpc.Choice, error) { return rpc.ChoiceOnce, nil }
	out, how := s.dispatcher.Dispatch(ctx, dispatch.Call{
		ID: fmt.Sprintf("job-output-%d", start.UnixNano()), Name: builtin.WriteFile, Args: args, Session: session,
		Source: rpc.SourceJob, Caller: dispatch.CallerMeru, Append: sess.Append, Approve: yes,
	})
	if how.Outcome != dispatch.OutcomeOK || out.IsError {
		return fmt.Errorf("write %s: %s", path, out.Text)
	}
	return nil
}

// jobKind says what a job does, for its span: "command", "skill" or
// "prompt".
func jobKind(j config.Job) string {
	switch {
	case j.Command != "":
		return "command"
	case j.Skill != "":
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

// handleJobSet answers OpJobSet: it saves req.JobSpec in place of the job
// named req.ID, or as a new job when ID is empty, hands the scheduler the
// new list and replies with it. With req.DryRun it saves nothing and
// replies with the job alone, its schedule in words and its next run. It
// fails, saying why, for a job config.toml would refuse.
func (s *jobService) handleJobSet(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	if req.JobSpec == nil {
		return errors.New("job_set needs a job")
	}
	j := jobOf(*req.JobSpec)
	if req.DryRun {
		return s.checkJob(req.ID, j, emit)
	}
	err := s.editConfig(func() error {
		if err := catalog.SetJob(s.configPath, req.ID, j); err != nil {
			return err
		}
		return s.reload()
	})
	if err != nil {
		return err
	}
	verb := "job saved"
	if req.ID == "" {
		verb = "job added"
	}
	s.log.InfoContext(ctx, verb, "job", j.Name, "paused", !j.On())
	return s.handleJobs(emit)
}

// checkJob answers a dry run of OpJobSet: it puts j in place of the job
// named was, or after the rest, checks the jobs as config.toml's load
// would, and replies with j alone, with its schedule in words and its next
// run. It fails with each problem, or when config.toml doesn't load.
func (s *jobService) checkJob(was string, j config.Job, emit func(rpc.Event) error) error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("fix config.toml before changing a job: %w", err)
	}
	i := slices.IndexFunc(cfg.Jobs, func(x config.Job) bool { return was != "" && x.Name == was })
	if was != "" && i < 0 {
		return fmt.Errorf("there is no job named %q", was)
	}
	list := slices.Clone(cfg.Jobs)
	if i >= 0 {
		list[i] = j
	} else {
		list = append(list, j)
	}
	cfg.Jobs = list
	if err := config.CheckJobs(cfg); err != nil {
		return err
	}
	info := rpc.JobInfo{JobSpec: specOf(j)}
	if sch, err := j.Parse(time.Local); err == nil {
		info.When = sch.Describe()
		if next, ok := sch.Next(time.Now()); ok {
			info.Next = next.Format(time.RFC3339)
		}
	}
	return emit(rpc.Event{Type: rpc.EventJobs, Jobs: []rpc.JobInfo{info}})
}

// add saves j as a new job, for schedule_job, under the config lock, and
// hands the scheduler the new list. It fails for a name another job has
// or a job config.toml would refuse.
func (s *jobService) add(ctx context.Context, j config.Job) error {
	err := s.editConfig(func() error {
		if err := catalog.SetJob(s.configPath, "", j); err != nil {
			return err
		}
		return s.reload()
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "job added", "job", j.Name, "by", "schedule_job")
	return nil
}

// handleJobRemove answers OpJobRemove: it takes the job named req.ID out
// of config.toml, hands the scheduler the jobs left and replies with
// them. A run in progress goes on to its end. It fails for a job
// config.toml doesn't hold.
func (s *jobService) handleJobRemove(ctx context.Context, req rpc.Request, emit func(rpc.Event) error) error {
	err := s.editConfig(func() error {
		if err := catalog.RemoveJob(s.configPath, req.ID); err != nil {
			return err
		}
		return s.reload()
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "job removed", "job", req.ID)
	return s.handleJobs(emit)
}

// reload reads config.toml's jobs again and hands them to the scheduler.
// The caller holds the config lock.
func (s *jobService) reload() error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	return s.set(cfg.Jobs)
}

// jobOf turns a client's JobSpec into a config.Job; specOf goes the other
// way. Paused is enabled = false; a running job leaves Enabled unset, as
// a hand-written entry does.
func jobOf(sp rpc.JobSpec) config.Job {
	j := config.Job{
		Name: strings.TrimSpace(sp.Name), Schedule: strings.TrimSpace(sp.Schedule), EveryWeeks: sp.EveryWeeks,
		From: sp.From, Prompt: strings.TrimSpace(sp.Prompt), Skill: sp.Skill, Command: sp.Command,
		Args: sp.Args, Allow: sp.Allow, Output: sp.Output,
	}
	if sp.Paused {
		off := false
		j.Enabled = &off
	}
	return j
}

// specOf turns a config.Job into the JobSpec a client shows.
func specOf(j config.Job) rpc.JobSpec {
	return rpc.JobSpec{
		Name: j.Name, Schedule: j.Schedule, EveryWeeks: j.EveryWeeks, From: j.From, Prompt: j.Prompt,
		Skill: j.Skill, Command: j.Command, Args: j.Args, Allow: j.Allow, Output: j.Output, Paused: !j.On(),
	}
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
			JobSpec: specOf(j), When: when, Next: stamp(st.Next), Last: stamp(st.Last),
			LastOutcome: st.LastOutcome, LastSession: st.LastSession,
			Running: st.Running, Started: stamp(st.Started),
		})
	}
	return out
}
