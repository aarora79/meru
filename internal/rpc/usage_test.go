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
