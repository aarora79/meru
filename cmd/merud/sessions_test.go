// This file tests the glue that keeps the past-conversation tables in step:
// the replay after each turn and the summarizer's settings.

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/transcript"
)

func TestTurnRecorderReplaysTheSession(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "meru.db"), EmbedModel: "e", Dims: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	sess, err := transcript.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []transcript.Line{
		{Type: transcript.TypeUser, Text: "what is the garden budget?"},
		{Type: transcript.TypeAssistant, Text: "400 dollars."},
	} {
		if err := sess.Append(l); err != nil {
			t.Fatal(err)
		}
	}

	r := turnRecorder{st: st, sessionsDir: dir, log: obs.Discard()}
	if err := r.InsertTurn(ctx, store.Turn{Session: sess.ID(), Time: time.Now()}); err != nil {
		t.Fatalf("InsertTurn: %v", err)
	}
	hits, err := st.SearchMessageKeyword(ctx, "garden", "", 5)
	if err != nil || len(hits) != 1 || hits[0].Session != sess.ID() {
		t.Errorf("message search after the turn = %+v, %v; want the question", hits, err)
	}

	// A session that can't be replayed still keeps its turns row.
	if err := r.InsertTurn(ctx, store.Turn{Session: "not-an-id", Time: time.Now()}); err != nil {
		t.Errorf("InsertTurn with a bad session = %v, want the row written and a warning", err)
	}
}

func TestNewSummarizer(t *testing.T) {
	cfg := config.Config{Agent: config.Agent{SummaryIdle: "45m"}}
	if _, err := newSummarizer(cfg, nil, nil, t.TempDir(), obs.Discard()); err != nil {
		t.Errorf("newSummarizer: %v", err)
	}
	cfg.Agent.SummaryIdle = "soon"
	if _, err := newSummarizer(cfg, nil, nil, t.TempDir(), obs.Discard()); err == nil {
		t.Error("newSummarizer took a bad duration")
	}
}
