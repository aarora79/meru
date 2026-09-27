// This file tests the usage table and its number formats.

package rpc

import (
	"reflect"
	"testing"
)

func TestShortCount(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{950, "950"},
		{999, "999"},
		{1000, "1.0k"},
		{1234, "1.2k"},
		{9960, "10k"},
		{18_400, "18k"},
		{999_499, "999k"},
		{999_999, "1.0M"},
		{1_400_000, "1.4M"},
		{2_500_000_000, "2.5B"},
		{7_000_000_000_000, "7000B"},
	}
	for _, tt := range tests {
		if got := ShortCount(tt.n); got != tt.want {
			t.Errorf("ShortCount(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{999, "0s"},
		{45_000, "45s"},
		{134_000, "2m 14s"},
		{124_000, "2m 04s"},
		{3_600_000, "1h 00m"},
		{11_100_000, "3h 05m"},
		{97_200_000, "27h 00m"},
	}
	for _, tt := range tests {
		if got := ShortDuration(tt.ms); got != tt.want {
			t.Errorf("ShortDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestUsageTable(t *testing.T) {
	windows := []UsageWindow{
		{Name: Usage1h, Sessions: 1, Turns: 4, TokensIn: 18_000, TokensOut: 2_100, ActiveMillis: 134_000, Docs: 3, ToolCalls: 2},
		{Name: UsageLifetime, Sessions: 30, Turns: 1200, TokensIn: 1_400_000, TokensOut: 95_000, ActiveMillis: 11_100_000, Docs: 250},
	}
	want := [][]string{
		{"", "1h", "all"},
		{"sessions", "1", "30"},
		{"questions", "4", "1.2k"},
		{"tokens in", "18k", "1.4M"},
		{"tokens out", "2.1k", "95k"},
		{"active time", "2m 14s", "3h 05m"},
		{"docs touched", "3", "250"},
		{"tool calls", "2", "0"},
	}
	if got := UsageTable(windows); !reflect.DeepEqual(got, want) {
		t.Errorf("UsageTable =\n%q\nwant\n%q", got, want)
	}
	// With no windows, the table still has its labels, so a client can
	// draw an empty table.
	if got := UsageTable(nil); len(got) != 8 || len(got[0]) != 1 {
		t.Errorf("UsageTable(nil) = %q, want 8 rows of one cell", got)
	}
}

func TestShortBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{8_808_038, "8.4 MB"},
		{88_080_384, "84 MB"},
		{1_048_575, "1.0 MB"},
		{1_288_490_189, "1.2 GB"},
	}
	for _, tt := range tests {
		if got := ShortBytes(tt.n); got != tt.want {
			t.Errorf("ShortBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// TestModelUsageTable checks the per-model table: one row per model under
// the header, the speed worked out from tokens and writing time, and a
// dash for what a model has no data for.
func TestModelUsageTable(t *testing.T) {
	windows := []UsageWindow{
		{Model: "qwen3.6:35b-a3b-mxfp8", Turns: 41, TTFTp50Millis: 820, TokensOut: 2410, EvalMillis: 100_000,
			ToolCalls: 63, BadCalls: 2, Capped: 1},
		{Model: "qwen3.8:27b-mlx", Turns: 35, TTFTp50Millis: 12_400, TokensOut: 142, EvalMillis: 10_000, ToolCalls: 51, BadCalls: 4, Capped: 3},
		{Turns: 3},
	}
	want := [][]string{
		{"MODEL", "TURNS", "TTFT p50", "TOK/S", "TOOL CALLS", "BAD CALLS", "CAPPED"},
		{"qwen3.6:35b-a3b-mxfp8", "41", "820ms", "24.1", "63", "2", "1"},
		{"qwen3.8:27b-mlx", "35", "12s", "14.2", "51", "4", "3"},
		{"(not recorded)", "3", "—", "—", "0", "0", "0"},
	}
	if got := ModelUsageTable(windows); !reflect.DeepEqual(got, want) {
		t.Errorf("ModelUsageTable =\n%q\nwant\n%q", got, want)
	}
}

// TestShortMillis checks the three ranges of a time to first token, and
// that 9.96 seconds rounds up to "10s" rather than "10.0s".
func TestShortMillis(t *testing.T) {
	for ms, want := range map[int64]string{0: "0ms", 820: "820ms", 1240: "1.2s", 9960: "10s", 14_200: "14s"} {
		if got := ShortMillis(ms); got != want {
			t.Errorf("ShortMillis(%d) = %q, want %q", ms, got, want)
		}
	}
}
