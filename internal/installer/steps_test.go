// This file tests Flow, the rules for moving a step between its states.

package installer

import (
	"errors"
	"testing"
)

// TestFlowTransitions runs each step through a list of actions and checks
// the state after the last one, and whether the last action was refused.
func TestFlowTransitions(t *testing.T) {
	fail := errors.New("no network")
	tests := []struct {
		name    string
		step    string
		actions []string // start, done, fail, skip
		want    Status
		refused bool // the last action returns an error
	}{
		{"a new step is pending", StepOllama, nil, StatusPending, false},
		{"continue then success", StepOllama, []string{"start", "done"}, StatusDone, false},
		{"continue then failure", StepOllama, []string{"start", "fail"}, StatusFailed, false},
		{"retry after a failure", StepOllama, []string{"start", "fail", "start", "done"}, StatusDone, false},
		{"skip a pending step", StepWeb, []string{"skip"}, StatusSkipped, false},
		{"skip after a failure", StepWeb, []string{"start", "fail", "skip"}, StatusSkipped, false},
		{"run a skipped step after all", StepWeb, []string{"skip", "start", "done"}, StatusDone, false},
		{"run a done step again", StepFolders, []string{"start", "done", "start"}, StatusRunning, false},
		{"can't skip a running step", StepWeb, []string{"start", "skip"}, StatusRunning, true},
		{"can't finish a step that isn't running", StepWeb, []string{"done"}, StatusPending, true},
		{"About you has no skip", StepProfile, []string{"skip"}, StatusPending, true},
		{"About you can't be skipped after a failure", StepProfile, []string{"start", "fail", "skip"}, StatusFailed, true},
		{"About you runs like any step", StepProfile, []string{"start", "done"}, StatusDone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFlow()
			var err error
			for _, a := range tt.actions {
				switch a {
				case "start":
					err = f.Start(tt.step)
				case "done":
					err = f.Finish(tt.step, "ok", nil)
				case "fail":
					err = f.Finish(tt.step, "", fail)
				case "skip":
					err = f.Skip(tt.step)
				}
			}
			if (err != nil) != tt.refused {
				t.Errorf("last action error = %v, want refused %v", err, tt.refused)
			}
			s, _ := f.Get(tt.step)
			if s.Status != tt.want {
				t.Errorf("status = %s, want %s", s.Status, tt.want)
			}
		})
	}
}

// TestFlowFailureKeepsReason checks that a failed step says why, and that a
// retry clears the old reason.
func TestFlowFailureKeepsReason(t *testing.T) {
	f := NewFlow()
	_ = f.Start(StepWeb)
	_ = f.Finish(StepWeb, "", ErrNoDocker)
	s, _ := f.Get(StepWeb)
	if s.Detail != ErrNoDocker.Error() {
		t.Errorf("detail = %q, want the error's text", s.Detail)
	}
	_ = f.Start(StepWeb)
	s, _ = f.Get(StepWeb)
	if s.Detail != "" {
		t.Errorf("retry kept the old reason %q", s.Detail)
	}
}

// TestFlowOneAtATime checks that a second step can't start while one runs.
func TestFlowOneAtATime(t *testing.T) {
	f := NewFlow()
	if err := f.Start(StepOllama); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(StepWeb); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
	if err := f.Start("no-such-step"); err == nil {
		t.Error("Start accepted an unknown step")
	}
}

// TestStepList checks the nine steps: their order, their text, and that
// only About you can't be skipped.
func TestStepList(t *testing.T) {
	want := []string{StepCheck, StepMeru, StepOllama, StepFolders, StepWeb, StepSkills, StepGoogle, StepProfile, StepStart}
	steps := NewFlow().Steps()
	if len(steps) != len(want) {
		t.Fatalf("%d steps, want %d", len(steps), len(want))
	}
	for i, s := range steps {
		if s.ID != want[i] {
			t.Errorf("step %d = %s, want %s", i, s.ID, want[i])
		}
		if s.Title == "" || s.What == "" || s.Why == "" || s.Cost == "" {
			t.Errorf("step %s is missing text: %+v", s.ID, s)
		}
		if s.Skippable != (s.ID != StepProfile) {
			t.Errorf("step %s skippable = %v", s.ID, s.Skippable)
		}
		if s.Status != StatusPending {
			t.Errorf("step %s starts %s", s.ID, s.Status)
		}
	}
}
