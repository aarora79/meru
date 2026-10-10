// This file holds UsageDetail, the numbers behind the Usage page's
// dashboard and the /usage box in meru chat: questions and time to first
// token per day, the slowest recent questions, and tool calls by outcome.
// Every number comes from the turns and tool_calls tables, which rebuild
// from the transcripts, so the dashboard needs nothing else running.

package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// The dashboard's queries, three in all and none per day, tool or question.
// ts is UTC text in one format, so a window's start compares as text.
// detailTurns reads each turn of the window; Go sorts them into local days
// with the caller's location, the same calendar the usage windows use. A
// person's 30 days hold a few thousand turns at most.
const (
	detailTurns   = `SELECT ts, tokens_in, tokens_out, ttft_ms FROM turns WHERE ts >= ?`
	detailSlowest = `SELECT session, ts, route, model, duration_ms, ttft_ms, tokens_in
		FROM turns WHERE ts >= ? ORDER BY duration_ms DESC LIMIT ?`
	detailTools = `SELECT tool, kind, outcome, COUNT(*) FROM tool_calls
		WHERE ts >= ? GROUP BY tool, kind, outcome`
)

// UsageDetail returns the dashboard as of now: the last rpc.UsageDays local
// days, the rpc.SlowestCount slowest questions of the last rpc.SlowestDays
// days, the rpc.ToolsCount busiest tools of the last rpc.UsageDays days by
// outcome, and use by model. Days follow now's location. Metrics stays
// empty: merud fills it in. It fails when the database does.
func (s *Store) UsageDetail(ctx context.Context, now time.Time) (rpc.UsageDetailReply, error) {
	var out rpc.UsageDetailReply
	// The first day of the window starts at local midnight, rpc.UsageDays-1
	// days before today, so the window holds today and the days before it.
	y, m, d := now.Date()
	first := time.Date(y, m, d-(rpc.UsageDays-1), 0, 0, 0, 0, now.Location())
	since := first.UTC().Format(time.RFC3339)

	days, err := s.detailDays(ctx, first, since)
	if err != nil {
		return out, err
	}
	out.Days = days

	slowSince := now.Add(-rpc.SlowestDays * 24 * time.Hour).UTC().Format(time.RFC3339)
	if out.Slowest, err = s.detailSlowest(ctx, slowSince); err != nil {
		return out, err
	}
	if out.Tools, err = s.detailTools(ctx, since); err != nil {
		return out, err
	}
	if out.Models, err = s.UsageByModel(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// detailDays returns one UsageDay per local day from first through today,
// oldest first, with the days that had no question filled in as zeros. It
// reads the window's turns in one query, puts each in its local day by
// first's location, and works out each day's p50 and p95 time to first
// token in Go, since SQL has no percentile function.
func (s *Store) detailDays(ctx context.Context, first time.Time, since string) ([]rpc.UsageDay, error) {
	byDate := map[string]*rpc.UsageDay{}
	ttfts := map[string][]int64{}
	rows, err := s.db.QueryContext(ctx, detailTurns, since)
	if err != nil {
		return nil, fmt.Errorf("usage by day: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ts string
		var in, out, ttft int64
		if err := rows.Scan(&ts, &in, &out, &ttft); err != nil {
			return nil, fmt.Errorf("usage by day: %w", err)
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue // a row written by hand; it can't be placed in a day
		}
		date := at.In(first.Location()).Format("2006-01-02")
		day, ok := byDate[date]
		if !ok {
			day = &rpc.UsageDay{Date: date}
			byDate[date] = day
		}
		day.Questions++
		day.TokensIn += in
		day.TokensOut += out
		if ttft > 0 {
			ttfts[date] = append(ttfts[date], ttft)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage by day: %w", err)
	}

	out := make([]rpc.UsageDay, 0, rpc.UsageDays)
	for i := 0; i < rpc.UsageDays; i++ {
		date := first.AddDate(0, 0, i).Format("2006-01-02")
		day := rpc.UsageDay{Date: date}
		if d, ok := byDate[date]; ok {
			day = *d
		}
		if v := ttfts[date]; len(v) > 0 {
			sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
			day.TTFTp50Millis = nearestRank(v, 50)
			day.TTFTp95Millis = nearestRank(v, 95)
		}
		out = append(out, day)
	}
	return out, nil
}

// nearestRank returns the p-th percentile of sorted, by nearest rank: the
// smallest value with at least p percent of the values at or below it. It
// is always one real value, and for p = 50 it is the same median
// UsageByModel reports. sorted must not be empty.
func nearestRank(sorted []int64, p float64) int64 {
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

// detailSlowest returns the slowest answered questions since since,
// slowest first.
func (s *Store) detailSlowest(ctx context.Context, since string) ([]rpc.SlowQuestion, error) {
	rows, err := s.db.QueryContext(ctx, detailSlowest, since, rpc.SlowestCount)
	if err != nil {
		return nil, fmt.Errorf("slowest questions: %w", err)
	}
	defer rows.Close()
	out := []rpc.SlowQuestion{}
	for rows.Next() {
		var q rpc.SlowQuestion
		if err := rows.Scan(&q.Session, &q.At, &q.Route, &q.Model, &q.Millis, &q.TTFT, &q.TokensIn); err != nil {
			return nil, fmt.Errorf("slowest questions: %w", err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("slowest questions: %w", err)
	}
	return out, nil
}

// detailTools returns each tool's calls since since by outcome, the
// busiest rpc.ToolsCount tools, busiest first and then by name. An
// incognito chat's calls count: their rows keep the tool and the outcome,
// which is all this reads.
func (s *Store) detailTools(ctx context.Context, since string) ([]rpc.ToolOutcomes, error) {
	rows, err := s.db.QueryContext(ctx, detailTools, since)
	if err != nil {
		return nil, fmt.Errorf("tool calls by outcome: %w", err)
	}
	defer rows.Close()
	byTool := map[string]*rpc.ToolOutcomes{}
	for rows.Next() {
		var tool, kind, outcome string
		var n int
		if err := rows.Scan(&tool, &kind, &outcome, &n); err != nil {
			return nil, fmt.Errorf("tool calls by outcome: %w", err)
		}
		t, ok := byTool[tool]
		if !ok {
			t = &rpc.ToolOutcomes{Tool: tool, Kind: kind, Outcomes: map[string]int{}}
			byTool[tool] = t
		}
		t.Calls += n
		t.Outcomes[outcome] += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tool calls by outcome: %w", err)
	}
	out := make([]rpc.ToolOutcomes, 0, len(byTool))
	for _, t := range byTool {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Tool < out[j].Tool
	})
	if len(out) > rpc.ToolsCount {
		out = out[:rpc.ToolsCount]
	}
	return out, nil
}
