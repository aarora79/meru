// This file holds the helpers both clients use to show a "usage" event:
// UsageTable lays the windows out as rows of cells, ModelUsageTable does
// the same for the per-model reply, and ShortCount and ShortDuration write
// their numbers. `meru usage` prints the tables as plain text and `meru
// chat` draws them in a box, so both show the same cells.

package rpc

import (
	"fmt"
	"strconv"
)

// UsageNote is the line both clients print under the usage table, because
// "today" alone doesn't say where a day starts.
const UsageNote = "Today, week and month follow the local calendar."

// UsageTable returns the windows as rows of cells, ready to line up in
// columns. The first row holds the window names after an empty corner cell;
// each row after it holds one measure's label and its value in each window:
//
//	""           "1h"  "today"  …
//	"sessions"   "1"   "2"      …
//	"questions"  "4"   "9"      …
//
// The columns keep the order merud sent, which is the order of the Usage*
// constants. Token counts come out short ("18k"), because a lifetime total
// runs to millions and would widen every column.
func UsageTable(windows []UsageWindow) [][]string {
	// Each measure pairs its label with a function that reads its value
	// from one window. A func value can sit in a struct field like any
	// other value.
	measures := []struct {
		label string
		value func(UsageWindow) string
	}{
		{"sessions", func(w UsageWindow) string { return ShortCount(int64(w.Sessions)) }},
		{"questions", func(w UsageWindow) string { return ShortCount(int64(w.Turns)) }},
		{"tokens in", func(w UsageWindow) string { return ShortCount(w.TokensIn) }},
		{"tokens out", func(w UsageWindow) string { return ShortCount(w.TokensOut) }},
		{"active time", func(w UsageWindow) string { return ShortDuration(w.ActiveMillis) }},
		{"docs touched", func(w UsageWindow) string { return ShortCount(int64(w.Docs)) }},
		{"tool calls", func(w UsageWindow) string { return ShortCount(int64(w.ToolCalls)) }},
	}
	header := []string{""}
	for _, w := range windows {
		header = append(header, w.Name)
	}
	rows := [][]string{header}
	for _, m := range measures {
		row := []string{m.label}
		for _, w := range windows {
			row = append(row, m.value(w))
		}
		rows = append(rows, row)
	}
	return rows
}

// ModelUsageNote is the line both clients print under the per-model
// table: what the numbers cover, and what counts as a bad call.
const ModelUsageNote = "Every answered question, by the model that wrote the answer. " +
	"CALLS counts tool calls, and BAD CALLS the ones Meru couldn't run as the model wrote them. " +
	"CAPPED counts turns that used every round and wrote no answer."

// unknownModel names the row of turns from before the transcripts named
// their model.
const unknownModel = "(not recorded)"

// ModelUsageTable returns the per-model windows of an OpUsage reply with
// Kind UsageByModel as rows of cells, a header row first:
//
//	"MODEL"                  "TURNS"  "TTFT p50"  "TOK/S"  "CALLS"  "BAD CALLS"  "CAPPED"
//	"qwen3.6:35b-a3b-mxfp8"  "41"     "820ms"     "24.1"   "63"     "2"          "1"
//
// CALLS is short for tool calls, so the table fits a chat box 80 columns
// wide.
//
// TTFT p50 shows "—" for a model with no turn that wrote text, and TOK/S
// for one with no writing time; turns from before the transcripts named
// their model come under "(not recorded)".
func ModelUsageTable(windows []UsageWindow) [][]string {
	rows := [][]string{{"MODEL", "TURNS", "TTFT p50", "TOK/S", "CALLS", "BAD CALLS", "CAPPED"}}
	for _, w := range windows {
		model := w.Model
		if model == "" {
			model = unknownModel
		}
		ttft := "—"
		if w.TTFTp50Millis > 0 {
			ttft = ShortMillis(w.TTFTp50Millis)
		}
		speed := "—"
		if w.EvalMillis > 0 {
			speed = fmt.Sprintf("%.1f", float64(w.TokensOut)/(float64(w.EvalMillis)/1000))
		}
		rows = append(rows, []string{model, ShortCount(int64(w.Turns)), ttft, speed,
			ShortCount(int64(w.ToolCalls)), ShortCount(int64(w.BadCalls)), ShortCount(int64(w.Capped))})
	}
	return rows
}

// ShortMillis writes a time to first token: "820ms" under a second,
// "1.2s" under ten seconds, and "14s" from there up. 9.96 seconds counts
// as ten, as in oneDecimal.
func ShortMillis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return oneDecimal(float64(ms)/1000) + "s"
}

// ShortCount writes n in at most four characters or so: as it is below a
// thousand ("950"), then in thousands, millions or billions, with one
// decimal below ten ("1.2k", "18k", "1.4M"). The units count by 1,000, the
// way people count questions and tokens.
func ShortCount(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	v := float64(n)
	units := []string{"k", "M", "B"}
	for i, unit := range units {
		v /= 1000
		// 999,999 would round to "1000k"; the next unit writes it "1.0M".
		if v < 999.5 || i == len(units)-1 {
			return oneDecimal(v) + unit
		}
	}
	return "" // not reached: the loop returns on its last unit
}

// oneDecimal writes v with one decimal below ten and none from there up,
// so "8.4" and "84" both stay short. 9.96 counts as ten, because "%.1f"
// would round it to "10.0".
func oneDecimal(v float64) string {
	if v < 9.95 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

// ShortDuration writes a count of milliseconds as the two largest units:
// "45s" under a minute, "2m 14s" under an hour, "3h 05m" from there up. The
// second unit always has two digits, so the values line up in a column.
func ShortDuration(ms int64) string {
	s := ms / 1000
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
}

// ShortBytes writes a size on disk for people: "512 B", "8.4 MB", "84 MB",
// "1.2 GB", with one decimal below ten. The units count by 1,024 and carry
// the familiar labels KB, MB and GB, as macOS's `ls -lh` and `du -h` do on
// Linux, so the number matches what those tools print for meru.db. (Finder
// counts by 1,000 and would show a little more.)
func ShortBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	for i, unit := range units {
		v /= 1024
		// 1,023.9 KB would print as "1024 KB"; the next unit writes it
		// "1.0 MB".
		if v < 999.5 || i == len(units)-1 {
			if v < 9.95 {
				return fmt.Sprintf("%.1f %s", v, unit)
			}
			return fmt.Sprintf("%.0f %s", v, unit)
		}
	}
	return "" // not reached: the loop returns on its last unit
}
