// This file holds the Bridge's side of scheduled jobs: listing them for
// Settings, Jobs, running one now, stopping a run, and adding, editing,
// pausing and removing a job. merud schedules and
// runs the jobs (cmd/merud/jobs.go); the Bridge only asks. See
// ARCHITECTURE.md, "Scheduler".

package desktop

import (
	"context"
	"errors"

	"github.com/aarora79/meru/internal/rpc"
)

// Jobs returns the scheduled jobs, each with its schedule in words, next
// and last run, and whether it runs now. It fails when merud can't be
// reached or is too old to know the op.
func (b *Bridge) Jobs(ctx context.Context) ([]rpc.JobInfo, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpJobs}, rpc.EventJobs)
	if err != nil {
		return nil, err
	}
	return ev.Jobs, nil
}

// RunJob runs the job named name now, outside its schedule, and returns
// the jobs, with it running. It fails for an empty name, an unknown job or
// one already running.
func (b *Bridge) RunJob(ctx context.Context, name string) ([]rpc.JobInfo, error) {
	return b.jobChange(ctx, rpc.OpJobRun, name)
}

// CancelJob stops the run of the job named name, which ends "cancelled",
// and returns the jobs. It fails when the job has no run going.
func (b *Bridge) CancelJob(ctx context.Context, name string) ([]rpc.JobInfo, error) {
	return b.jobChange(ctx, rpc.OpJobCancel, name)
}

// SaveJob saves spec as a job, in place of the job named was, so an edit
// can rename it, or as a new job when was is empty, and returns the jobs.
// Pause and Resume save the job with Paused changed. merud checks the job
// as config.toml would, and fails with what's wrong.
func (b *Bridge) SaveJob(ctx context.Context, was string, spec rpc.JobSpec) ([]rpc.JobInfo, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpJobSet, ID: was, JobSpec: &spec}, rpc.EventJobs)
	if err != nil {
		return nil, err
	}
	return ev.Jobs, nil
}

// CheckJob asks merud whether spec would save, in place of the job named
// was, and returns it with its schedule in words and its next run, for the
// form to show as the user types. merud saves nothing. It fails with
// what's wrong with the job.
func (b *Bridge) CheckJob(ctx context.Context, was string, spec rpc.JobSpec) (rpc.JobInfo, error) {
	ev, err := b.one(ctx, rpc.Request{Op: rpc.OpJobSet, ID: was, JobSpec: &spec, DryRun: true}, rpc.EventJobs)
	if err != nil {
		return rpc.JobInfo{}, err
	}
	if len(ev.Jobs) != 1 {
		return rpc.JobInfo{}, errors.New("merud sent no job back")
	}
	return ev.Jobs[0], nil
}

// RemoveJob takes the job named name out of config.toml and returns the
// jobs left. Its past runs stay in the Jobs folder.
func (b *Bridge) RemoveJob(ctx context.Context, name string) ([]rpc.JobInfo, error) {
	return b.jobChange(ctx, rpc.OpJobRemove, name)
}

// jobChange sends op for the job named name and returns the jobs merud
// sends back.
func (b *Bridge) jobChange(ctx context.Context, op rpc.Op, name string) ([]rpc.JobInfo, error) {
	if name == "" {
		return nil, errors.New("no job named")
	}
	ev, err := b.one(ctx, rpc.Request{Op: op, ID: name}, rpc.EventJobs)
	if err != nil {
		return nil, err
	}
	return ev.Jobs, nil
}
