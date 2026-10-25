// This file tests the scheduled jobs through a whole merud: the jobs list,
// Run now, the run's chat in the Jobs folder, and Cancel with no run.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

func TestJobOps(t *testing.T) {
	dir, _ := meruHome(t)
	body := settingsHeader + `
[[jobs]]
name = "morning-brief"
schedule = "0 7 * * 1-5"
prompt = "Brief me on today's calendar."
`
	d := startDaemon(t, dir, body, &fakeEngine{version: "0.40.5"})

	ev := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpJobs}), rpc.EventJobs)
	if len(ev.Jobs) != 1 {
		t.Fatalf("jobs = %+v", ev.Jobs)
	}
	j := ev.Jobs[0]
	if j.Name != "morning-brief" || j.When != "7:00 on weekdays" || j.Next == "" || j.Running {
		t.Errorf("job = %+v", j)
	}

	// Cancel with nothing running fails; Run now starts a run.
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpJobCancel, ID: "morning-brief"}, rpc.ChoiceOnce); !strings.Contains(msg, "isn't running") {
		t.Errorf("cancel with no run: %q", msg)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpJobRun, ID: "nope"}, rpc.ChoiceOnce); !strings.Contains(msg, "no job named") {
		t.Errorf("run an unknown job: %q", msg)
	}
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpJobRun, ID: "morning-brief"})

	var last rpc.JobInfo
	waitUntil(t, "the run ends", func() bool {
		last = eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpJobs}), rpc.EventJobs).Jobs[0]
		return !last.Running && last.LastOutcome != ""
	})
	if last.LastOutcome != "ok" || last.LastSession == "" {
		t.Fatalf("after the run: %+v", last)
	}

	// The run is a chat in the Jobs folder, titled by the job.
	sessions := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpSessions}), rpc.EventSessions).Sessions
	var found *rpc.SessionInfo
	for i := range sessions {
		if sessions[i].ID == last.LastSession {
			found = &sessions[i]
		}
	}
	if found == nil || found.Folder != config.JobsFolder || !strings.HasPrefix(found.Title, "morning-brief · ") {
		t.Errorf("run's chat = %+v", found)
	}
	names, err := transcript.LoadFolders(filepath.Join(dir, "sessions"))
	if err != nil || !contains(names, config.JobsFolder) {
		t.Errorf("chat folders = %v, %v; want the Jobs folder", names, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions")); err != nil {
		t.Fatal(err)
	}
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// runJob runs the job named name now through d and waits for it to end,
// returning its info.
func runJob(t *testing.T, sock, name string) rpc.JobInfo {
	t.Helper()
	mustCall(t, sock, rpc.Request{Op: rpc.OpJobRun, ID: name})
	var j rpc.JobInfo
	waitUntil(t, name+" ends", func() bool {
		for _, x := range eventOf(t, mustCall(t, sock, rpc.Request{Op: rpc.OpJobs}), rpc.EventJobs).Jobs {
			if x.Name == name {
				j = x
			}
		}
		return !j.Running && j.LastOutcome != ""
	})
	return j
}

// TestJobCommandAndOutput runs a command job, which runs no model, and a
// prompt job whose answer also goes to a file, both through dispatch.
func TestJobCommandAndOutput(t *testing.T) {
	dir, home := meruHome(t)
	body := settingsHeader + `
[[commands]]
name = "say-done"
description = "Says it is done"
argv = ["echo", "backup done"]

[[jobs]]
name = "backup"
schedule = "30 23 * * *"
command = "say-done"

[[jobs]]
name = "morning-brief"
schedule = "0 7 * * 1-5"
prompt = "Brief me."
output = "briefs/{job}.md"
`
	d := startDaemon(t, dir, body, &fakeEngine{version: "0.40.5"})

	backup := runJob(t, d.sock, "backup")
	if backup.LastOutcome != "ok" {
		t.Fatalf("backup = %+v", backup)
	}
	turns := eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpSessionTurns, Session: backup.LastSession}), rpc.EventTurns).Turns
	if len(turns) != 1 || !strings.Contains(turns[0].Answer, "backup done") || turns[0].Question != "Run cmd.say-done" {
		t.Errorf("backup's chat = %+v", turns)
	}

	brief := runJob(t, d.sock, "morning-brief")
	if brief.LastOutcome != "ok" {
		t.Fatalf("brief = %+v", brief)
	}
	b, err := os.ReadFile(filepath.Join(home, "meru-output", "briefs", "morning-brief.md"))
	if err != nil || len(b) == 0 {
		t.Errorf("output file: %q, %v", b, err)
	}
	// The write went through dispatch, so the run's chat shows it.
	turns = eventOf(t, mustCall(t, d.sock, rpc.Request{Op: rpc.OpSessionTurns, Session: brief.LastSession}), rpc.EventTurns).Turns
	found := false
	for _, step := range turns[len(turns)-1].Tools {
		found = found || step.Name == "write_file"
	}
	if !found {
		t.Errorf("the brief's chat shows no write_file call: %+v", turns)
	}
}

// TestAllowFor checks a job's approver: once for a tool its allow list
// names, deny for any other, and deny when "once" isn't on offer.
func TestAllowFor(t *testing.T) {
	approve := allowFor(config.Job{Allow: []string{"write_file"}})
	ask := func(name string, choices ...rpc.Choice) rpc.Choice {
		c, err := approve(t.Context(), rpc.Approval{Name: name, Choices: choices})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if got := ask("write_file", rpc.ChoiceOnce, rpc.ChoiceSession, rpc.ChoiceDeny); got != rpc.ChoiceOnce {
		t.Errorf("allowed tool: %s", got)
	}
	if got := ask("google.send_gmail_message", rpc.ChoiceOnce, rpc.ChoiceDeny); got != rpc.ChoiceDeny {
		t.Errorf("tool not allowed: %s", got)
	}
	if got := ask("write_file", rpc.ChoiceDeny); got != rpc.ChoiceDeny {
		t.Errorf("no once on offer: %s", got)
	}
}

// TestJobSetAndRemove adds, checks, pauses, renames and removes a job over
// the socket, and checks that config.toml and the list follow, and that
// the Jobs folder stays while a job exists.
func TestJobSetAndRemove(t *testing.T) {
	dir, _ := meruHome(t)
	d := startDaemon(t, dir, settingsHeader, &fakeEngine{version: "0.40.5"})
	cfgPath := filepath.Join(dir, "config.toml")
	set := func(was string, sp rpc.JobSpec, dry bool) ([]rpc.Event, string) {
		return callErr(t, d.sock, rpc.Request{Op: rpc.OpJobSet, ID: was, JobSpec: &sp, DryRun: dry}, rpc.ChoiceOnce)
	}
	brief := rpc.JobSpec{Name: "morning-brief", Schedule: "0 7 * * 1-5", Prompt: "Brief me."}

	// A dry run says the schedule in words and saves nothing.
	evs, msg := set("", brief, true)
	if msg != "" {
		t.Fatal(msg)
	}
	if got := eventOf(t, evs, rpc.EventJobs).Jobs; len(got) != 1 || got[0].When != "7:00 on weekdays" || got[0].Next == "" {
		t.Errorf("dry run = %+v", got)
	}
	if _, msg := set("", rpc.JobSpec{Name: "x", Schedule: "0 25 * * *", Prompt: "p"}, true); !strings.Contains(msg, "schedule") {
		t.Errorf("dry run of a bad schedule: %q", msg)
	}
	if cfg, err := config.Load(cfgPath); err != nil || len(cfg.Jobs) != 0 {
		t.Fatalf("after dry runs: %+v, %v", cfg.Jobs, err)
	}

	// Add, then refuse a second job of the same name.
	evs, msg = set("", brief, false)
	if msg != "" {
		t.Fatal(msg)
	}
	if got := eventOf(t, evs, rpc.EventJobs).Jobs; len(got) != 1 || got[0].Name != "morning-brief" {
		t.Errorf("after add = %+v", got)
	}
	if _, msg := set("", brief, false); !strings.Contains(msg, "exists already") {
		t.Errorf("a second add: %q", msg)
	}

	// While a job exists, the Jobs folder can't be removed or renamed.
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpChatFolderAdd, ID: config.JobsFolder})
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpChatFolderRemove, ID: config.JobsFolder}, rpc.ChoiceOnce); !strings.Contains(msg, "remove the jobs first") {
		t.Errorf("remove the Jobs folder: %q", msg)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpChatFolderRename, ID: config.JobsFolder, Text: "Old"}, rpc.ChoiceOnce); !strings.Contains(msg, "remove the jobs first") {
		t.Errorf("rename the Jobs folder: %q", msg)
	}

	// Pause and rename in one edit.
	paused := brief
	paused.Name, paused.Paused = "brief", true
	evs, msg = set("morning-brief", paused, false)
	if msg != "" {
		t.Fatal(msg)
	}
	if got := eventOf(t, evs, rpc.EventJobs).Jobs; len(got) != 1 || got[0].Name != "brief" || !got[0].Paused || got[0].Prompt != "Brief me." {
		t.Errorf("after edit = %+v", got)
	}
	if cfg, err := config.Load(cfgPath); err != nil || len(cfg.Jobs) != 1 || cfg.Jobs[0].On() {
		t.Errorf("config after edit: %+v, %v", cfg.Jobs, err)
	}

	// Remove; then the folder may go.
	evs = mustCall(t, d.sock, rpc.Request{Op: rpc.OpJobRemove, ID: "brief"})
	if got := eventOf(t, evs, rpc.EventJobs).Jobs; len(got) != 0 {
		t.Errorf("after remove = %+v", got)
	}
	if _, msg := callErr(t, d.sock, rpc.Request{Op: rpc.OpJobRemove, ID: "brief"}, rpc.ChoiceOnce); !strings.Contains(msg, "no job named") {
		t.Errorf("a second remove: %q", msg)
	}
	mustCall(t, d.sock, rpc.Request{Op: rpc.OpChatFolderRemove, ID: config.JobsFolder})
}
