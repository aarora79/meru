// This file tests the route rule for connected tools: toolTarget, and the
// nouns in the tools' names that it matches.

package agent

import (
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/engine"
)

// googleAndNotes stands in for the owner's servers: workspace-mcp and an
// Obsidian vault, plus built-ins that must not count.
func googleAndNotes() []engine.ToolSpec {
	var specs []engine.ToolSpec
	for _, n := range []string{
		"google.search_gmail_messages", "google.get_gmail_message_content", "google.send_gmail_message",
		"google.list_calendars", "google.get_events", "google.manage_event",
		"google.search_drive_files", "google.get_drive_file_content", "google.create_drive_folder",
		"obsidian.obsidian_list_vaults", "obsidian.obsidian_search_vault", "obsidian.obsidian_read_note",
		"read_file", "grep", "web_search", "cmd.git-log",
	} {
		specs = append(specs, spec(n))
	}
	return specs
}

func TestToolNouns(t *testing.T) {
	got := toolNouns(googleAndNotes())
	for _, want := range []string{"gmail", "message", "calendar", "event", "drive", "note", "vault"} {
		if !slices.Contains(got, want) {
			t.Errorf("toolNouns lacks %q: %q", want, got)
		}
	}
	for _, not := range []string{"search", "file", "list", "grep", "git-log", "log"} {
		if slices.Contains(got, not) {
			t.Errorf("toolNouns holds %q: %q", not, got)
		}
	}
}

func TestAsksAboutToolNoun(t *testing.T) {
	specs := googleAndNotes()
	tests := []struct {
		question string
		want     bool
	}{
		{"what was the last email I sent?", true},
		{"any new messages from Sam?", true},
		{"what's on my calendar tomorrow?", true},
		{"do I have events on Friday?", true},
		{"find the budget doc in my drive", true},
		{"what do my notes say about the garden?", true},
		{"read my files about the garden", false},
		{"search for the release notes of Go 1.27", true}, // "notes": a wrong guess costs schemas only
		{"what is the capital of France?", false},
		{"list the folders in ~/backup", false},
	}
	for _, tt := range tests {
		if got := asksAboutToolNoun(tt.question, specs); got != tt.want {
			t.Errorf("asksAboutToolNoun(%q) = %v, want %v", tt.question, got, tt.want)
		}
	}
	if asksAboutToolNoun("what was the last email I sent?", []engine.ToolSpec{spec("read_file")}) {
		t.Error("with no MCP servers, no question should match")
	}
}

func TestToolTarget(t *testing.T) {
	specs := append(googleAndNotes(), spec("remember"))
	tests := []struct {
		question string
		specs    []engine.ToolSpec
		want     string
	}{
		{"search my Obsidian vault for AI", specs, "names a tool server"},
		{"ask google for my unread mail", specs, "names a tool server"},
		{"remember that my name is Dana", specs, "asks Meru to remember"},
		{"search the web for the latest Go release", specs, "asks for the web"},
		{"what's on my calendar tomorrow?", specs, "names what a tool handles"},
		{"what did Naur argue about programming?", specs, ""},
		{"list the folders in ~/backup", specs, ""},
		{"search the web for the latest Go release", []engine.ToolSpec{spec("read_file")}, ""},
		{"remember that my name is Dana", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.question, func(t *testing.T) {
			if got := toolTarget(tt.question, tt.specs); got != tt.want {
				t.Errorf("toolTarget = %q, want %q", got, tt.want)
			}
		})
	}
}
