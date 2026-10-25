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
