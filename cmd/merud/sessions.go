// This file keeps the past-conversation tables in step with the session
// transcripts: the replay at startup, the replay after each turn, and the
// summarizer that runs beside the server. ARCHITECTURE.md, "Facts and
// episodes", says why merud summarizes sessions.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/summarize"
)

// replaySessions brings sessions, messages and their indexes up to date
// with the transcripts under sessionsDir. On a fresh meru.db it reads
// every transcript; after that, only the lines added while merud was
// stopped. A failed replay costs recall of some past sessions, not the
// answer to any question, so it only logs a warning.
func replaySessions(ctx context.Context, st *store.Store, sessionsDir string, log *slog.Logger) {
	start := time.Now()
	n, err := st.ReplaySessions(ctx, sessionsDir)
	if err != nil {
		log.Warn("replay sessions from transcripts", "err", err)
		return
	}
	if n > 0 {
		log.Info("sessions replayed from transcripts", "lines", n, "ms", time.Since(start).Milliseconds())
	}
}

// turnRecorder is the agent's TurnRecorder in merud. It writes the turns
// row, then replays the turn's session into the past-conversation tables,
// so a question in another session can find this one at once. The agent
// records a turn only when it has written the answer, so the replay sees
// the question and the answer both.
type turnRecorder struct {
	st          *store.Store
	sessionsDir string
	log         *slog.Logger
}

// InsertTurn writes t's row and replays its session. A failed replay only
// logs a warning: the transcript has the lines, and the next replay of the
// session picks them up. It fails when the row can't be written.
func (r turnRecorder) InsertTurn(ctx context.Context, t store.Turn) error {
	if err := r.st.InsertTurn(ctx, t); err != nil {
		return err
	}
	if _, err := r.st.ReplaySession(ctx, r.sessionsDir, t.Session); err != nil {
		r.log.WarnContext(ctx, "replay the session after its turn", "session", t.Session, "err", err)
	}
	return nil
}

// newSummarizer builds the summarizer merud runs beside the server: it
// summarizes sessions with the fast model once they have been quiet for
// [agent] summary_idle. It fails only when summary_idle doesn't parse,
// which config.Load has already ruled out.
func newSummarizer(cfg config.Config, st *store.Store, eng engine.Engine, sessionsDir string, log *slog.Logger) (*summarize.Summarizer, error) {
	idle, err := time.ParseDuration(cfg.Agent.SummaryIdle)
	if err != nil {
		return nil, fmt.Errorf("agent.summary_idle: %w", err)
	}
	return summarize.New(st, eng, cfg.Models.Fast, idle, sessionsDir, log), nil
}
