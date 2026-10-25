// This file tests the [[jobs]] checks.

package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestJobsConfig(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string // "" for a config that loads
	}{
		{name: "a prompt job", body: `
[[jobs]]
name = "morning-brief"
schedule = "0 7 * * 1-5"
prompt = "Brief me on today's calendar."`},
		{name: "a skill job every other week", body: `
[[jobs]]
name = "go-news"
schedule = "0 9 * * 2"
every_weeks = 2
from = "2026-10-27"
skill = "web-research"
prompt = "Summarize the Go release notes."`},
		{name: "paused", body: `
[[jobs]]
name = "brief"
schedule = "0 7 * * *"
prompt = "hi"
enabled = false`},
		{name: "no name", wantErr: "jobs.#1: name is empty", body: `
[[jobs]]
schedule = "0 7 * * *"
prompt = "hi"`},
		{name: "bad name", wantErr: "must be lower-case", body: `
[[jobs]]
name = "Morning Brief"
schedule = "0 7 * * *"
prompt = "hi"`},
		{name: "twice", wantErr: "jobs.brief: name is used twice", body: `
[[jobs]]
name = "brief"
schedule = "0 7 * * *"
prompt = "hi"
[[jobs]]
name = "brief"
schedule = "0 8 * * *"
prompt = "hi"`},
		{name: "bad schedule", wantErr: "jobs.brief: schedule: hour: 25 is past 23", body: `
[[jobs]]
name = "brief"
schedule = "0 25 * * *"
prompt = "hi"`},
		{name: "no schedule", wantErr: "schedule is empty", body: `
[[jobs]]
name = "brief"
prompt = "hi"`},
		{name: "no prompt", wantErr: "give a prompt", body: `
[[jobs]]
name = "brief"
schedule = "0 7 * * *"`},
		{name: "a command job", body: `
[[commands]]
name = "backup-notes"
description = "Back up the notes"
argv = ["rsync", "-a", "~/notes/", "~/backup/notes/"]
[[jobs]]
name = "backup"
schedule = "30 23 * * *"
command = "backup-notes"
allow = ["cmd.backup-notes"]`},
		{name: "allow and output", body: `
[[jobs]]
name = "brief"
schedule = "0 7 * * *"
prompt = "hi"
allow = ["write_file", "google.send_gmail_message"]
output = "briefs/{date}-{job}.md"`},
		{name: "a command that isn't declared", wantErr: `command "backup-notes" isn't a [[commands]] entry`, body: `
[[jobs]]
name = "backup"
schedule = "0 7 * * *"
command = "backup-notes"`},
		{name: "prompt and command", wantErr: "not both", body: `
[[commands]]
name = "disk"
description = "Free space"
argv = ["df", "-h"]
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
command = "disk"`},
		{name: "args with no command", wantErr: "args go with a command", body: `
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
args = { repo = "~/repos/meru" }`},
		{name: "allow configure", wantErr: "allow can't hold configure", body: `
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
allow = ["configure"]`},
		{name: "output outside", wantErr: "leaves [skills] output_dir", body: `
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
output = "../elsewhere.md"`},
		{name: "output absolute", wantErr: "must be a path inside", body: `
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
output = "/tmp/brief.md"`},
		{name: "output placeholder", wantErr: "{month} isn't one of", body: `
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
output = "briefs/{month}.md"`},
		{name: "output without write_file", wantErr: "output needs write_file", body: `
[builtin]
tools = ["datetime"]
[[jobs]]
name = "j"
schedule = "0 7 * * *"
prompt = "hi"
output = "briefs/{date}.md"`},
		{name: "a disabled skill", wantErr: `skill "explainer" is off`, body: `
[skills]
disabled = ["explainer"]
[[jobs]]
name = "brief"
schedule = "0 7 * * *"
prompt = "hi"
skill = "explainer"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Load: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Load error = %v, want one with %q", err, tt.wantErr)
			}
		})
	}

	// More than MaxJobs is refused.
	var b strings.Builder
	for i := range MaxJobs + 1 {
		fmt.Fprintf(&b, "[[jobs]]\nname = \"j%d\"\nschedule = \"0 7 * * *\"\nprompt = \"hi\"\n", i)
	}
	if _, err := Load(writeConfig(t, b.String())); err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Errorf("51 jobs: err = %v", err)
	}
}

func TestJobOn(t *testing.T) {
	off := false
	if !(Job{}).On() || (Job{Enabled: &off}).On() {
		t.Error("a job is on unless enabled = false")
	}
}
