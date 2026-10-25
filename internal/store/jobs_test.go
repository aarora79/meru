// This file tests the job column on turns: each job's last run, written
// live and rebuilt from a transcript.

package store

import (
	"context"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/transcript"
)

func TestLastJobRuns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := func(h int) time.Time { return time.Date(2026, 10, 24, h, 0, 0, 0, time.UTC) }
	for _, tr := range []Turn{
		{Session: "a", Time: at(7), Job: "morning-brief"},
		{Session: "b", Time: at(8), Job: "morning-brief"},
		{Session: "c", Time: at(9), Job: "go-news"},
		{Session: "d", Time: at(10)}, // a question someone asked
	} {
		if err := s.InsertTurn(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.LastJobRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 || !last["morning-brief"].Equal(at(8)) || !last["go-news"].Equal(at(9)) {
		t.Errorf("LastJobRuns = %v", last)
	}

	// A rebuilt row carries the job from the transcript's user line.
	lines := []transcript.Line{
		{Type: transcript.TypeUser, TS: at(7), Text: "Brief me", Job: "morning-brief"},
		{Type: transcript.TypeAssistant, TS: at(7), Text: "Here is your brief."},
		{Type: transcript.TypeUser, TS: at(9), Text: "And the weather?"},
		{Type: transcript.TypeAssistant, TS: at(9), Text: "Sunny."},
	}
	turns := turnsOf("e", lines)
	if len(turns) != 2 || turns[0].Job != "morning-brief" || turns[1].Job != "" {
		t.Errorf("turnsOf jobs = %q, %q", turns[0].Job, turns[1].Job)
	}
}
