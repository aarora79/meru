// This file holds [[jobs]]: the Job type and its checks. merud's scheduler
// runs each job on its schedule (ARCHITECTURE.md, "Scheduler").

package config

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/aarora79/meru/internal/jobs"
)

// MaxJobs caps [[jobs]]. Each job's name is an attribute of the
// meru.scheduler.job_runs metric, and a metric's attributes must come from
// a small set.
const MaxJobs = 50

// JobsFolder is the chat folder every job's runs go in.
const JobsFolder = "Jobs"

// jobName is a job's name: lower-case letters, digits, "-" and "_", up to
// 40 characters, starting with a letter or digit.
var jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// maxPromptChars caps a job's prompt, as a typed question is capped in
// practice by the box it's typed in.
const maxPromptChars = 4000

// Job is one [[jobs]] entry: when it runs and what it does. Prompt, with an
// optional Skill, is the action; Command, Args, Allow and Output come in a
// later step of #167, and config refuses them until then.
type Job struct {
	// Name names the job in the clients, the chat titles and the metric.
	Name string `toml:"name"`
	// Schedule is a five-field cron expression in this computer's time
	// zone, such as "0 7 * * 1-5" for 7:00 on weekdays.
	Schedule string `toml:"schedule"`
	// EveryWeeks, above 1, runs the job only every that many weeks,
	// counted from the Monday of From's week; From is the first day it may
	// run, as "2026-10-27".
	EveryWeeks int    `toml:"every_weeks"`
	From       string `toml:"from"`
	// Prompt is the question each run asks, through the agent loop.
	Prompt string `toml:"prompt"`
	// Skill names a skill the run loads without the pick step.
	Skill string `toml:"skill"`
	// Command, Args, Allow and Output: see ARCHITECTURE.md, "Scheduler".
	// Config refuses them until merud runs them.
	Command string            `toml:"command"`
	Args    map[string]string `toml:"args"`
	Allow   []string          `toml:"allow"`
	Output  string            `toml:"output"`
	// Enabled, when false, pauses the job. A pointer, so a missing key
	// reads as nil, which means on.
	Enabled *bool `toml:"enabled"`
}

// On reports whether the job runs on its schedule: Enabled is missing or
// true.
func (j Job) On() bool { return j.Enabled == nil || *j.Enabled }

// Parse reads the job's schedule in loc, the computer's time zone.
func (j Job) Parse(loc *time.Location) (jobs.Schedule, error) {
	return jobs.Parse(j.Schedule, j.EveryWeeks, j.From, loc)
}

// checkJobs checks every [[jobs]] entry and returns each problem, naming
// the job and the key.
func checkJobs(cfg Config) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	if len(cfg.Jobs) > MaxJobs {
		add("jobs: %d jobs; at most %d", len(cfg.Jobs), MaxJobs)
	}
	seen := map[string]bool{}
	for i, j := range cfg.Jobs {
		name := j.Name
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		where := "jobs." + name
		switch {
		case j.Name == "":
			add("%s: name is empty", where)
		case !jobName.MatchString(j.Name):
			add("%s: name %q must be lower-case letters, digits, - and _, up to 40", where, j.Name)
		case seen[j.Name]:
			add("%s: name is used twice", where)
		}
		seen[j.Name] = true

		if j.Schedule == "" {
			add("%s: schedule is empty; give five fields such as \"0 7 * * 1-5\"", where)
		} else if _, err := j.Parse(time.Local); err != nil {
			add("%s: schedule: %w", where, err)
		}

		switch {
		case j.Prompt == "" && j.Command == "":
			add("%s: give a prompt for the run to ask", where)
		case len([]rune(j.Prompt)) > maxPromptChars:
			add("%s: prompt runs to %d characters; at most %d", where, len([]rune(j.Prompt)), maxPromptChars)
		}
		// These come in a later step of #167. Refusing them now beats
		// accepting a key that does nothing.
		for _, k := range []struct {
			key string
			set bool
		}{
			{"command", j.Command != ""},
			{"args", len(j.Args) > 0},
			{"allow", len(j.Allow) > 0},
			{"output", j.Output != ""},
		} {
			if k.set {
				add("%s: %s isn't supported yet; this Meru runs prompt and skill jobs", where, k.key)
			}
		}
		if j.Skill != "" && slices.Contains(cfg.Skills.Disabled, j.Skill) {
			add("%s: skill %q is off in [skills] disabled", where, j.Skill)
		}
	}
	return errs
}
