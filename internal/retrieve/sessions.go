// This file holds SearchSessions: recall of past conversations. It searches
// session summaries by meaning and by keyword, and questions and answers by
// keyword, then merges the three lists by session with rrf. The span is
// meru.retrieve.sessions and the metric stage "sessions".

package retrieve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/store"
)

// sessionListSize is how many hits each of the three searches returns
// before the merge. It is smaller than the 50 for chunks: a person has far
// fewer sessions than their files have chunks, and a turn keeps only 3.
const sessionListSize = 20

// SessionResult is one recalled session: its row, the message that best
// matched the query, and its fused score.
type SessionResult struct {
	store.Session
	// Match is the question or answer that matched the query best, or nil
	// when the session matched only by its summary.
	Match *store.Message
	// Score is the session's RRF score; higher is better.
	Score float64
}

// SearchSessions finds the n past sessions that best match query, best
// first, leaving out the session excludeSession (the one asking). It runs
// three searches:
//
//   - summaries by meaning: the query's vector against session_vec;
//   - summaries by keyword: summary_fts;
//   - questions and answers by keyword: message_fts, grouped by session.
//     A session's place in this list is the place of its best message, and
//     that message becomes the result's Match.
//
// rrf merges the three lists, as Search does for chunks. A blank query or
// n <= 0 returns nothing. It fails when the embedding or a search fails.
//
// The meru.retrieve.sessions span carries counts and timings only, never
// the query or any text.
func SearchSessions(ctx context.Context, st *store.Store, eng engine.Engine, query, excludeSession string, n int) (results []SessionResult, err error) {
	if strings.TrimSpace(query) == "" || n <= 0 {
		return nil, nil
	}
	ctx, span := obs.Tracer().Start(ctx, "meru.retrieve.sessions")
	defer func() {
		obs.EndSpanErr(ctx, span, err)
		span.End()
	}()
	start := time.Now()

	vecs, err := eng.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("recall sessions: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, errors.New("recall sessions: embed query: no vector returned")
	}
	byMeaning, err := st.SearchSessionVector(ctx, vecs[0], excludeSession, sessionListSize)
	if err != nil {
		return nil, fmt.Errorf("recall sessions: %w", err)
	}
	bySummary, err := st.SearchSummaryKeyword(ctx, query, excludeSession, sessionListSize)
	if err != nil {
		return nil, fmt.Errorf("recall sessions: %w", err)
	}
	byMessage, err := st.SearchMessageKeyword(ctx, query, excludeSession, sessionListSize)
	if err != nil {
		return nil, fmt.Errorf("recall sessions: %w", err)
	}

	// rrf and top work on int64 IDs, so give each session a number in the
	// order it first shows up, and keep the names to turn numbers back.
	num := map[string]int64{}
	var names []string
	number := func(session string) int64 {
		if id, ok := num[session]; ok {
			return id
		}
		num[session] = int64(len(names))
		names = append(names, session)
		return num[session]
	}
	list := func(hits []store.SessionHit) []int64 {
		ids := make([]int64, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, number(h.Session))
		}
		return ids
	}
	// Messages arrive best first, so a session's first message is its best
	// one, and its place in the grouped list is that message's place.
	best := map[string]int64{}
	var grouped []store.SessionHit
	for _, h := range byMessage {
		if _, seen := best[h.Session]; !seen {
			best[h.Session] = h.MessageID
			grouped = append(grouped, h)
		}
	}
	scores := rrf(list(byMeaning), list(bySummary), list(grouped))
	winners := top(scores, n)

	ids := make([]string, len(winners))
	var msgIDs []int64
	for i, w := range winners {
		ids[i] = names[w.id]
		if m, ok := best[ids[i]]; ok {
			msgIDs = append(msgIDs, m)
		}
	}
	sessions, err := st.Sessions(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("recall sessions: %w", err)
	}
	msgs, err := st.Messages(ctx, msgIDs)
	if err != nil {
		return nil, fmt.Errorf("recall sessions: %w", err)
	}
	bySession := map[string]store.Message{}
	for _, m := range msgs {
		bySession[m.Session] = m
	}

	// Sessions keeps the order of ids, so results come out best first.
	results = make([]SessionResult, 0, len(sessions))
	for _, ss := range sessions {
		r := SessionResult{Session: ss, Score: scores[num[ss.ID]]}
		if m, ok := bySession[ss.ID]; ok {
			r.Match = &m
		}
		results = append(results, r)
	}

	dur := time.Since(start)
	obs.RecordRetrieval(ctx, "sessions", dur)
	span.SetAttributes(
		attribute.Int("meru.retrieve.vector_hits", len(byMeaning)),
		attribute.Int("meru.retrieve.summary_hits", len(bySummary)),
		attribute.Int("meru.retrieve.message_hits", len(byMessage)),
		attribute.Int("meru.retrieve.fused", len(scores)),
		attribute.Int("meru.retrieve.results", len(results)),
		attribute.Float64("meru.retrieve.ms", ms(dur)),
	)
	return results, nil
}
