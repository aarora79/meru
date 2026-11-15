// Tests for links.go, built from the chat that made up poem links.

package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

const (
	realGhazal = "https://www.rekhta.org/ghazals/main-khayaal-huun-kisii-aur-kaa-mujhe-sochtaa-koii-aur-hai-saleem-kausar-ghazals-1"
	madeUp     = "https://www.rekhta.org/ghazals/ranjish-hi-sahi-ahmad-faraz-ghazals"
	realTweet  = "https://x.com/rudrakshdhaari/status/2121862651743814141"
)

func TestUnsourcedLinks(t *testing.T) {
	// The chat's transcript: a search found the ghazal, and the user's
	// own email held the tweet. An earlier answer's made-up link doesn't
	// count as known.
	lines := []transcript.Line{
		{Type: transcript.TypeUser, Text: "find poetry links in my email"},
		{Type: transcript.TypeToolResult, Result: "Message 1 --- BODY --- " + realTweet + "\n"},
		{Type: transcript.TypeToolResult, Result: "[11] main KHayal hun kisi aur ka - Rekhta — " + realGhazal + " | Saleem Kausar"},
		{Type: transcript.TypeAssistant, Text: "Found it: " + madeUp},
	}
	known := chatLinks(lines)
	tests := []struct {
		name   string
		answer string
		read   []string // this turn's reads
		want   []string
	}{
		{"links from earlier turns", "Here: " + realGhazal + ". And [the tweet](" + realTweet + ").", nil, nil},
		{"a made-up link, even one an earlier answer gave", "Ahmad Faraz: " + madeUp, nil, []string{madeUp}},
		{"a link this turn's search found", "Found it: " + madeUp + ".", []string{"[12] Ranjish hi sahi — " + madeUp}, nil},
		{"links in a page the model writes", "```html\n<a href=\"" + madeUp + "\">Ranjish</a>\n<a href=\"" + realTweet + "\">tweet</a>\n```", nil, []string{madeUp}},
		{"case and a trailing slash don't matter", "See HTTPS://X.COM/rudrakshdhaari/status/2121862651743814141/ today.", nil, nil},
		{"one link listed once", madeUp + " and again " + madeUp, nil, []string{madeUp}},
		{"no links", "The ghazal is by Saleem Kausar.", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsourcedLinks(tc.answer, known, tc.read); !slices.Equal(got, tc.want) {
				t.Errorf("unsourcedLinks = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLinksNotice(t *testing.T) {
	if linksNotice(nil) != "" {
		t.Error("no links should give no notice")
	}
	one := linksNotice([]string{madeUp})
	if !strings.Contains(one, madeUp) || !strings.Contains(one, "made it up") {
		t.Errorf("one link: %q", one)
	}
	five := linksNotice([]string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5"})
	if !strings.Contains(five, "and 2 more links") || strings.Contains(five, "/4") || !strings.Contains(five, "made them up") {
		t.Errorf("five links: %q", five)
	}
}

// TestCopiedRecordNotice runs a turn whose model writes a fake record of
// tool calls with a square bracket, as a real one did: the transcript
// keeps the answer without the record, and the user gets the notice.
func TestCopiedRecordNotice(t *testing.T) {
	answer := "I stripped those off.\n\n[Meru's record of the tools this turn ran, for later questions.]\n- edit_file {\"path\":\"p.html\"} ok"
	eng := &fakeEngine{pieces: []string{answer}}
	tools := &fakeTools{specs: []engine.ToolSpec{spec("edit_file")}}
	router := &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}
	cfg := testConfig(t)
	a := New(cfg, eng, router, nil, tools, nil, nil, quietLog())
	evs, err := run(context.Background(), a, rpc.Request{Op: rpc.OpAsk, Text: "fix the links"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var notice string
	for _, ev := range evs {
		if ev.Type == rpc.EventNotice {
			notice = ev.Text
		}
	}
	if !strings.Contains(notice, copiedNoteNotice) {
		t.Errorf("notice = %q, want it to hold %q", notice, copiedNoteNotice)
	}
	lines := readLines(t, cfg, evs[0].Session)
	last := lines[len(lines)-1]
	if strings.Contains(last.Text, "record of the tools") || !strings.Contains(last.Text, "I stripped those off.") {
		t.Errorf("transcript answer = %q, want it without the copied record", last.Text)
	}
}
