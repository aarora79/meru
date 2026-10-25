// This file holds schedule_job, the built-in tool that adds a scheduled
// job when the user asks for one in chat, such as "every other Tuesday at
// 9, summarize the Go release notes". It always asks, as configure does,
// and the approval card shows the schedule in words beside the cron
// fields, so the user checks what the model understood. merud's job
// service saves the job (UseJobs). See ARCHITECTURE.md, "Scheduler".

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// ScheduleJob is the schedule_job tool's name, as the model sees it.
const ScheduleJob = "schedule_job"

// AlwaysAsks reports whether the built-in tool name asks before every
// call, whatever [builtin] confirm says: configure and schedule_job, which
// change Meru itself. A job's own turns never see either.
func AlwaysAsks(name string) bool {
	return name == Configure || name == ScheduleJob
}

// UseJobs hands schedule_job the function that saves a new job: merud's
// job service, which writes it into config.toml and gives the scheduler
// the new list. add fails, saying why, for a job config.toml would refuse
// or a name another job has. merud calls it once, before the first turn;
// without it, schedule_job stays off.
func (t *Tools) UseJobs(add func(ctx context.Context, j config.Job) error) {
	t.addJob = add
}

// scheduleArgs is the JSON object the model sends to schedule_job.
type scheduleArgs struct {
	Name       string            `json:"name"`
	Schedule   string            `json:"schedule"`
	EveryWeeks int               `json:"every_weeks"`
	From       string            `json:"from"`
	Prompt     string            `json:"prompt"`
	Skill      string            `json:"skill"`
	Command    string            `json:"command"`
	Args       map[string]string `json:"args"`
	Output     string            `json:"output"`
}

// job turns the arguments into a config.Job. A job the model proposes has
// no allow list: what a job may do unwatched is the user's call, made in
// Settings or config.toml.
func (a scheduleArgs) job() config.Job {
	return config.Job{
		Name: strings.TrimSpace(a.Name), Schedule: strings.TrimSpace(a.Schedule), EveryWeeks: a.EveryWeeks,
		From: strings.TrimSpace(a.From), Prompt: strings.TrimSpace(a.Prompt), Skill: a.Skill,
		Command: a.Command, Args: a.Args, Output: a.Output,
	}
}

// parseScheduleArgs reads raw into scheduleArgs. It refuses an unknown key,
// as configure does: one is most likely a mistake, and config shouldn't
// change on arguments the model didn't mean.
func parseScheduleArgs(raw json.RawMessage) (scheduleArgs, error) {
	var a scheduleArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("schedule_job: the arguments aren't a valid JSON object for this tool: %v", err)
	}
	return a, nil
}

// when says j's schedule in words and its next run, such as "7:00 on
// weekdays; next run Mon 27 Oct 07:00", or why the schedule doesn't parse.
func when(j config.Job, now time.Time) (string, error) {
	sch, err := j.Parse(time.Local)
	if err != nil {
		return "", err
	}
	words := sch.Describe()
	if next, ok := sch.Next(now); ok {
		words += "; next run " + next.Format("Mon 2 Jan 2006 15:04")
	}
	return words, nil
}

// scheduleJob saves the job raw describes and returns the text for the
// model: what it saved, when the job runs, and where its runs go. It
// returns an error whose text the model reads when the arguments or the
// job don't pass; nothing is saved then.
func (t *Tools) scheduleJob(ctx context.Context, raw json.RawMessage) (string, error) {
	a, err := parseScheduleArgs(raw)
	if err != nil {
		return "", err
	}
	j := a.job()
	if j.Name == "" {
		return "", errors.New("schedule_job: give the job a name, such as morning-brief")
	}
	words, err := when(j, t.now())
	if err != nil {
		return "", fmt.Errorf("schedule_job: schedule %q: %v. Nothing was saved", j.Schedule, err)
	}
	if err := t.addJob(ctx, j); err != nil {
		return "", fmt.Errorf("schedule_job: %v. Nothing was saved", err)
	}
	return fmt.Sprintf("Scheduled the job %q: %s. Each run is a chat in the Jobs folder. "+
		"The user can run, pause, edit or remove it in Settings, Jobs, or with /jobs in meru chat. "+
		"A run declines any tool call that would ask first; the user can list tools it may use in the job's settings.",
		j.Name, words), nil
}

// AuditArgs adds the schedule in words to a schedule_job call's arguments,
// for the approval card and the audit log, so the user sees "9:00 on
// Tuesdays, every other week" beside "0 9 * * 2" before saying yes. It
// returns nil, leaving the arguments as they are, for any other tool and
// for arguments that don't parse. Dispatch calls it as a dispatch.Auditor.
func (t *Tools) AuditArgs(name string, raw json.RawMessage) json.RawMessage {
	if name != ScheduleJob {
		return nil
	}
	var shown map[string]any
	if err := json.Unmarshal(raw, &shown); err != nil {
		return nil
	}
	a, err := parseScheduleArgs(raw)
	if err != nil {
		return nil
	}
	words, err := when(a.job(), t.now())
	if err != nil {
		words = "the schedule doesn't parse: " + err.Error()
	}
	shown["when"] = words
	b, err := json.Marshal(shown)
	if err != nil {
		return nil
	}
	return b
}

// scheduleJobSpec is schedule_job's spec for the model.
func scheduleJobSpec() engine.ToolSpec {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": str("A short name: lower-case letters, digits, - and _, such as morning-brief."),
			"schedule": str("Five cron fields in this computer's time zone: minute hour day-of-month month day-of-week. " +
				"\"0 7 * * 1-5\" is 7:00 on weekdays, \"0 9 * * 2\" 9:00 on Tuesdays, \"*/30 * * * *\" every 30 minutes."),
			"every_weeks": map[string]any{"type": "integer", "minimum": 1,
				"description": "Run only every this many weeks: 2 for every other week. Needs from."},
			"from": str("With every_weeks: the first day it may run, as 2026-10-27."),
			"prompt": str("What Meru does on each run, written as the user would ask it, such as " +
				"\"Brief me on today's calendar and unread mail.\" Give a prompt or a command."),
			"skill":   str("Optional: a skill each run loads, such as web-research."),
			"command": str("Instead of a prompt: the name of a [[commands]] entry to run with no model."),
			"args": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
				"description": "With command: its parameters, by name."},
			"output": str("Optional: a file under the output folder that each answer also goes to, such as briefs/{date}.md; " +
				"{date}, {time} and {job} fill in."),
		},
		"required":             []string{"name", "schedule"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(s)
	return engine.ToolSpec{
		Name: ScheduleJob,
		Description: "Schedules a job: Meru asks a prompt, or runs a declared command, at the times given, " +
			"such as every weekday at 7. Use it when the user asks for something to happen on a schedule. " +
			"The user must approve every call, and sees the schedule in words. Check the time zone and the days " +
			"with datetime first if you aren't sure.",
		Parameters: b,
	}
}
