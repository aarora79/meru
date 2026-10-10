// This file tests UsageDetail, the numbers behind the Usage page's
// dashboard: days in local time with the empty ones filled, percentiles by
// nearest rank, the slowest questions of the week, and tool calls by
// outcome.

package store

import (
	"context"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

func TestUsageDetail(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// New York, not UTC, so a turn late in the evening there falls on a
	// different date in UTC, and the test shows days follow local time.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, ny)
	utc := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
	}

	turns := []Turn{
		// 9 Oct, 01:30 UTC is 8 Oct, 21:30 in New York.
		{Session: "a", Time: utc(10, 9, 1, 30), TokensIn: 10, TokensOut: 1, DurationMillis: 3000, TTFTMillis: 500, Model: "m"},
		// Three on 9 Oct in New York: p50 is 200 and p95 is 900.
		{Session: "a", Time: utc(10, 9, 13, 0), TokensIn: 20, TokensOut: 2, DurationMillis: 9000, TTFTMillis: 900, Model: "m", Route: "tools"},
		{Session: "b", Time: utc(10, 9, 13, 30), TokensIn: 30, TokensOut: 3, DurationMillis: 1000, TTFTMillis: 100, Model: "m"},
		{Session: "b", Time: utc(10, 9, 13, 45), TokensIn: 40, TokensOut: 4, DurationMillis: 2000, TTFTMillis: 200, Model: "m"},
		// 10 Sep, 03:00 UTC is 9 Sep in New York: a day before the window.
		{Session: "c", Time: utc(9, 10, 3, 0), DurationMillis: 99000},
		// 20 Sep is in the 30 days but not the last 7, so it isn't among
		// the slowest despite its time.
		{Session: "c", Time: utc(9, 20, 15, 0), DurationMillis: 60000},
		// A deleted chat's turns go with it.
		{Session: "gone", Time: utc(10, 9, 12, 0), DurationMillis: 50000},
	}
	for _, tr := range turns {
		if err := s.InsertTurn(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	calls := []ToolCall{
		{Session: "a", Time: utc(10, 9, 13, 0), Kind: "builtin", Server: "meru", Tool: "web_fetch", Outcome: "ok"},
		{Session: "a", Time: utc(10, 9, 13, 1), Kind: "builtin", Server: "meru", Tool: "web_fetch", Outcome: "ok"},
		{Session: "a", Time: utc(10, 9, 13, 2), Kind: "builtin", Server: "meru", Tool: "web_fetch", Outcome: "error"},
		// An incognito chat's call has no session, and still counts.
		{Time: utc(10, 9, 13, 3), Kind: "builtin", Server: "meru", Tool: "datetime", Outcome: "ok"},
		// Too old for the window.
		{Session: "c", Time: utc(8, 1, 0, 0), Kind: "mcp", Server: "google", Tool: "search_gmail", Outcome: "error"},
	}
	for _, c := range calls {
		if err := s.InsertToolCall(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteSession(ctx, "gone"); err != nil {
		t.Fatal(err)
	}

	got, err := s.UsageDetail(ctx, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Days) != rpc.UsageDays {
		t.Fatalf("%d days, want %d", len(got.Days), rpc.UsageDays)
	}
	if first, last := got.Days[0].Date, got.Days[len(got.Days)-1].Date; first != "2026-09-10" || last != "2026-10-09" {
		t.Errorf("days run %s to %s, want 2026-09-10 to 2026-10-09", first, last)
	}
	byDate := map[string]rpc.UsageDay{}
	total := 0
	for _, d := range got.Days {
		byDate[d.Date] = d
		total += d.Questions
	}
	if total != 5 {
		t.Errorf("%d questions in the window, want 5", total)
	}
	if d := byDate["2026-10-08"]; d.Questions != 1 || d.TokensIn != 10 || d.TTFTp50Millis != 500 {
		t.Errorf("8 Oct = %+v, want the evening turn", d)
	}
	want := rpc.UsageDay{Date: "2026-10-09", Questions: 3, TokensIn: 90, TokensOut: 9, TTFTp50Millis: 200, TTFTp95Millis: 900}
	if d := byDate["2026-10-09"]; d != want {
		t.Errorf("9 Oct = %+v, want %+v", d, want)
	}
	if d := byDate["2026-09-20"]; d.Questions != 1 || d.TTFTp50Millis != 0 {
		t.Errorf("20 Sep = %+v, want one question with no first token", d)
	}

	if len(got.Slowest) != 4 {
		t.Fatalf("slowest = %+v, want the 4 turns of the last 7 days", got.Slowest)
	}
	if q := got.Slowest[0]; q.Millis != 9000 || q.Session != "a" || q.Route != "tools" || q.TTFT != 900 || q.Model != "m" {
		t.Errorf("slowest = %+v, want the 9 s turn first", q)
	}
	for i := 1; i < len(got.Slowest); i++ {
		if got.Slowest[i].Millis > got.Slowest[i-1].Millis {
			t.Errorf("slowest out of order: %+v", got.Slowest)
		}
	}

	if len(got.Tools) != 2 {
		t.Fatalf("tools = %+v, want web_fetch and datetime", got.Tools)
	}
	if f := got.Tools[0]; f.Tool != "web_fetch" || f.Calls != 3 || f.Outcomes["ok"] != 2 || f.Outcomes["error"] != 1 || f.Kind != "builtin" {
		t.Errorf("tools[0] = %+v, want web_fetch with 2 ok and 1 error", f)
	}
	if d := got.Tools[1]; d.Tool != "datetime" || d.Calls != 1 {
		t.Errorf("tools[1] = %+v, want the incognito datetime call", d)
	}
	if got.Metrics != "" {
		t.Errorf("Metrics = %q; merud fills it in, not the store", got.Metrics)
	}
}

func TestUsageDetailEmpty(t *testing.T) {
	got, err := openTest(t).UsageDetail(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != rpc.UsageDays || len(got.Slowest) != 0 || len(got.Tools) != 0 {
		t.Errorf("empty store gave %+v, want %d zero days and nothing else", got, rpc.UsageDays)
	}
}

func TestNearestRank(t *testing.T) {
	tests := []struct {
		in   []int64
		p    float64
		want int64
	}{
		{[]int64{7}, 50, 7},
		{[]int64{7}, 95, 7},
		{[]int64{1, 2}, 50, 1},
		{[]int64{1, 2, 3, 4}, 50, 2},
		{[]int64{1, 2, 3, 4}, 95, 4},
		{[]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, 95, 19},
	}
	for _, tt := range tests {
		if got := nearestRank(tt.in, tt.p); got != tt.want {
			t.Errorf("nearestRank(%v, %v) = %d, want %d", tt.in, tt.p, got, tt.want)
		}
	}
}
