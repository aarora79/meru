// This file tests the route rule for what connected tools act on.

package agent

import (
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
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

// catalogServers returns the catalog's servers as `meru mcp add` writes
// them: each name with its allow list.
func catalogServers() []config.MCPServer {
	var out []config.MCPServer
	for _, e := range catalog.Entries() {
		out = append(out, config.MCPServer{Name: e.Name, Allow: e.Allow})
	}
	return out
}

func TestConnectedTools(t *testing.T) {
	web := config.Web{SearXNGURL: "http://127.0.0.1:8888"}
	tests := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{"nothing connected", config.Config{}, nil},
		{
			// The eval's connected set: internal/router's evalTools holds
			// the same text.
			"catalog servers, a command and web search",
			config.Config{
				MCP:      config.MCP{Servers: catalogServers()},
				Commands: []config.Command{{Name: "git-log"}},
				Builtin:  config.Builtin{Tools: config.BuiltinTools()},
				Web:      web,
			},
			[]string{"google (gmail, message, thread, event, drive)", "obsidian (vault)", "git-log", "web search"},
		},
		{
			"a server that allows nothing is left out",
			config.Config{MCP: config.MCP{Servers: []config.MCPServer{{Name: "idle"}, {Name: "notes", Allow: []string{"search_notes"}}}}},
			[]string{"notes"},
		},
		{
			"at most five nouns, none of them the server's own name",
			config.Config{MCP: config.MCP{Servers: []config.MCPServer{{Name: "home", Allow: []string{
				"get_home_lights", "set_thermostat", "list_cameras", "lock_doors", "open_garage", "water_plants",
			}}}}},
			[]string{"home (light, thermostat, camera, lock, door)"},
		},
		{
			"an A2A agent names its skills",
			config.Config{A2A: config.A2A{Agents: []config.A2AAgent{{Name: "research", Allow: []string{"summarize_paper"}}}}},
			[]string{"research (summarize, paper)"},
		},
		{
			"web_fetch alone reads pages",
			config.Config{Builtin: config.Builtin{Tools: []string{"web_search", "web_fetch", "datetime", "remember"}}},
			[]string{"web pages"},
		},
		{
			"web search needs SearXNG",
			config.Config{Builtin: config.Builtin{Tools: []string{"web_search", "datetime"}}},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConnectedTools(tt.cfg); !slices.Equal(got, tt.want) {
				t.Errorf("ConnectedTools = %q, want %q", got, tt.want)
			}
		})
	}
}
