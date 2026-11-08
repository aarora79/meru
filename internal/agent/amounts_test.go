// This file tests the amounts check and the sources note: which money
// amounts in an answer count as read, the notice a turn sends for the rest,
// and the line that names the user's data sources.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// folio is a hotel receipt as a mail tool returns it.
const folio = `Guest Folio, 2026-11-03 to 2026-11-05
GUEST ROOM $286.00
GUEST ROOM $286.00
CITY OCCUPANCY TAX $17.16
STATE TAX $18.95
LOUNGE BAR $83.24
Food and Beverage Credit -$15.00
Payment VISA $735.34`

func TestUnreadAmounts(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   []string
	}{
		{"every amount read", "You spent $735.34: the room was $286.00 a night, and the bar $83.24.", nil},
		{"same value, other form", "The room was $286 a night and the credit was $15.", nil},
		{"a sum the model made", "Taxes came to $95.18 and the total was $735.34.", []string{"$95.18"}},
		{"a code before", "The bar was USD 83.24.", nil},
		{"a code after, unread", "That is about 700 EUR.", []string{"700 EUR"}},
		{"each named once", "It was $95.18, yes, $95.18.", []string{"$95.18"}},
		{"code is skipped", "Run `echo $99.99` to see it.", nil},
		{"no amounts", "You stayed two nights.", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unreadAmounts(tt.answer, []string{folio}); !slices.Equal(got, tt.want) {
				t.Errorf("unreadAmounts = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAmountsNotice(t *testing.T) {
	if got := amountsNotice(nil); got != "" {
		t.Errorf("no amounts gave %q", got)
	}
	one := amountsNotice([]string{"$95.18"})
	if !strings.HasPrefix(one, "This amount isn't in what Meru read this turn") || !strings.Contains(one, "$95.18") {
		t.Errorf("one amount: %q", one)
	}
	many := amountsNotice([]string{"$1", "$2", "$3", "$4", "$5", "$6", "$7"})
	if !strings.HasPrefix(many, "These amounts aren't") || !strings.Contains(many, "$5 and 2 more") || strings.Contains(many, "$6") {
		t.Errorf("many amounts: %q", many)
	}
}

// TestAmountsCheckInATurn runs a turn whose tool returns the receipt and
// whose answer adds the taxes up wrong.
func TestAmountsCheckInATurn(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		calls  bool
		want   string // part of the notice; "" for none
	}{
		{"a made-up sum", "Taxes were $95.18 and you paid $735.34.", true, "$95.18"},
		{"every amount read", "You paid $735.34 for two nights at $286.00.", true, ""},
		{"no tool ran", "A night in a city hotel often costs about $250.", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			tools := &fakeTools{specs: []engine.ToolSpec{spec("google.get_gmail_message_content")},
				results: map[string]fakeResult{"google.get_gmail_message_content": {text: folio}}}
			rounds := []fakeRound{{pieces: []string{tt.answer}}}
			if tt.calls {
				rounds = append([]fakeRound{{calls: []engine.ToolCall{call("google.get_gmail_message_content", `{"message_id":"m1"}`)}}}, rounds...)
			}
			router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
			a := New(cfg, &fakeEngine{rounds: rounds}, router, nil, tools, nil, nil, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: "what did my hotel stay cost?"})
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
			switch {
			case tt.want == "" && i >= 0:
				t.Errorf("notice %q, want none", evs[i].Text)
			case tt.want != "" && (i < 0 || !strings.Contains(evs[i].Text, tt.want)):
				t.Errorf("events %v: want a notice naming %s", types(evs), tt.want)
			}
		})
	}
}

func TestSourcesNote(t *testing.T) {
	specs := func(names ...string) []engine.ToolSpec {
		var out []engine.ToolSpec
		for _, n := range names {
			out = append(out, spec(n))
		}
		return out
	}
	tests := []struct {
		name  string
		specs []engine.ToolSpec
		want  string // the places, as the note lists them; "" for no note
	}{
		{"files and mail", specs(builtin.SearchFiles, builtin.ReadFile, "google.search_gmail_messages"), "the user's files and the google tools"},
		{"three places, sorted", specs("obsidian.search", builtin.SearchNotes, builtin.Grep, "google.get_events"),
			"the user's files, Apple Notes, the google tools and the obsidian tools"},
		{"files alone", specs(builtin.SearchFiles, builtin.ReadFile), ""},
		{"web and a command", specs(builtin.WebSearch, builtin.WebFetch, "cmd.disk-free", "a2a.helper.ask"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourcesNote(tt.specs)
			if tt.want == "" {
				if got != "" {
					t.Errorf("sourcesNote = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, "sit in several places here: "+tt.want+".") || !strings.Contains(got, "newest first") {
				t.Errorf("sourcesNote = %q, want it to list %q", got, tt.want)
			}
			// noteFor carries it after the tools note.
			if n := noteFor(tt.specs); !strings.HasSuffix(n, got) {
				t.Errorf("noteFor = %q, want it to end with the sources note", n)
			}
		})
	}
}
