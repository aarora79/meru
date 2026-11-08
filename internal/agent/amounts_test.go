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
		route  string
		answer string
		calls  bool
		want   string // part of the notice; "" for none
	}{
		{"a made-up sum", "tools", "Taxes were $95.18 and you paid $735.34.", true, "$95.18"},
		{"every amount read", "tools", "You paid $735.34 for two nights at $286.00.", true, ""},
		{"a direct turn", "direct", "A night in a city hotel often costs about $250.", false, ""},
		// The model called no tool and described a receipt it never read.
		{"no tool ran on a tools route", "search+tools", "Here is the folio email: the room was $308.27 and you paid $361.17.", false, "$308.27"},
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
			router := &fakeRouter{dec: Decision{Route: tt.route, Confidence: 0.9, Outcome: "ok"}}
			a := New(cfg, &fakeEngine{rounds: rounds}, router, nil, tools, nil, nil, quietLog())
			evs, err := run(context.Background(), a, rpc.Request{Text: "and the bill?"})
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

func TestWithoutMeruNotes(t *testing.T) {
	copied := "Let me get the actual folio PDF content.\n\n" +
		"[Meru stopped here: it used all 8 rounds of tool calls before the answer was done. It tried: read_file ×2. Continue picks it up.]\n\n" +
		"(Meru's record of the tools this turn ran, for later questions. It isn't part of the answer above.)\n- google.get_gmail_message_content ok"
	tests := []struct{ in, want string }{
		{copied, "Let me get the actual folio PDF content."},
		{"You paid $735.34 for the stay.", "You paid $735.34 for the stay."},
		{"[Meru stopped here: no closing bracket", ""},
		{"(Meru's notes on the pages this turn read.) [1] a page", ""},
	}
	for _, tt := range tests {
		if got := withoutMeruNotes(tt.in); got != tt.want {
			t.Errorf("withoutMeruNotes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCopiedNoteGetsThePlanNudge runs a round whose reply is a plan with a
// copied cap note: Meru cuts the note, nudges the model, and the next
// round's answer stands alone.
func TestCopiedNoteGetsThePlanNudge(t *testing.T) {
	cfg := testConfig(t)
	tools := &fakeTools{specs: []engine.ToolSpec{spec("google.get_gmail_message_content")},
		results: map[string]fakeResult{"google.get_gmail_message_content": {text: folio}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{pieces: []string{"Let me get the actual folio PDF content.\n\n[Meru stopped here: it used all 8 rounds of tool calls. It tried: read_file ×2. Continue picks it up.]"}},
		{calls: []engine.ToolCall{call("google.get_gmail_message_content", `{"message_id":"m1"}`)}},
		{pieces: []string{"You paid $735.34 for the stay."}},
	}}
	router := &fakeRouter{dec: Decision{Route: "search+tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, tools, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Text: "Continue"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(eng.calls); n != 3 {
		t.Fatalf("model called %d times, want 3: the plan, the call, the answer", n)
	}
	lines := readLines(t, cfg, evs[0].Session)
	last := lines[len(lines)-1]
	if strings.Contains(last.Text, "Meru stopped here") || !strings.Contains(last.Text, "$735.34") {
		t.Errorf("answer = %q, want the real answer and no copied note", last.Text)
	}
}

func TestClaimsFileWork(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"Good news — I built the card with your family photo embedded.", true},
		{"I've downloaded your photo from Nidhi's email (IMG_0584.jpeg) and rebuilt the card with it.", true},
		{"The card now uses your actual family photo.", true},
		{"I saved the page to ~/meru-output/card.html.", true},
		{"I can rebuild the card with the photo if you like.", false},
		{"Want me to make the card now?", false},
		{"I found three photos in the email.", false},
		{"I found the photos you made last year in the email.", false},
		{"I didn't change the file.", false},
	}
	for _, tt := range tests {
		if got := claimsFileWork(tt.text); got != tt.want {
			t.Errorf("claimsFileWork(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

// TestFileClaimAfterADownload: the turn's only call, an attachment
// download, succeeds; the answer says it rebuilt the card; no file was
// written, so the notice says so.
func TestFileClaimAfterADownload(t *testing.T) {
	cfg := testConfig(t)
	tools := &fakeTools{specs: []engine.ToolSpec{spec("google.get_gmail_attachment_content"), spec(builtin.WriteFile)},
		results: map[string]fakeResult{"google.get_gmail_attachment_content": {text: "Attachment downloaded successfully!\n\nSaved at ~/meru-output/attachments/IMG_0584.jpeg."}}}
	eng := &fakeEngine{rounds: []fakeRound{
		{calls: []engine.ToolCall{call("google.get_gmail_attachment_content", `{"message_id":"m1","attachment_id":"a1"}`)}},
		{pieces: []string{"I've downloaded your photo and rebuilt the card with it."}},
	}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	a := New(cfg, eng, router, nil, tools, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Text: "include the photo in the card"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	i := slices.IndexFunc(evs, func(ev rpc.Event) bool { return ev.Type == rpc.EventNotice })
	if i < 0 || !strings.Contains(evs[i].Text, "wrote or edited no file") {
		t.Errorf("events %v: want the file-claim notice", types(evs))
	}
}
