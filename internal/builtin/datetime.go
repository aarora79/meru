// This file holds the datetime tool: the current date and time, the time in
// another zone, and the weekday of a date and how far it is from today. It
// reads the clock and nothing else, so it runs on every route, without
// asking.

package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	// time/tzdata embeds the time zone database in the binary, so a zone
	// such as "Asia/Kolkata" resolves on a machine that has no zone files,
	// as a Windows machine may not. It adds about 450 KB.
	_ "time/tzdata"

	"github.com/aarora79/meru/internal/engine"
)

// DateTime is the datetime tool's name, as the model sees it.
const DateTime = "datetime"

// datetimeDescription is what the model reads about the tool.
const datetimeDescription = "Gives the current date and time on the user's computer, with the weekday and the time zone. " +
	"Pass timezone (a name such as Asia/Kolkata or Europe/London) for the time there. " +
	"Pass date (YYYY-MM-DD) for that date's weekday and how many days it is from today. " +
	"Your prompt already gives the local date and time. Call this tool for the time in another place, " +
	"the weekday of another date or days between dates; don't work those out yourself."

// datetimeSchema returns the tool's argument schema. Both arguments are
// optional; with neither, the tool gives the local date and time.
func datetimeSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"timezone":{"type":"string","description":"An IANA time zone name, such as America/New_York or Asia/Kolkata."},` +
		`"date":{"type":"string","description":"A date as YYYY-MM-DD."}}}`)
}

// datetimeSpec returns the tool's spec.
func datetimeSpec() engine.ToolSpec {
	return engine.ToolSpec{Name: DateTime, Description: datetimeDescription, Parameters: datetimeSchema()}
}

// datetimeArgs is the JSON object the model sends.
type datetimeArgs struct {
	Timezone string `json:"timezone"`
	Date     string `json:"date"`
}

// dateTime answers one datetime call from now, the current time. It fails,
// with a message the model reads, on arguments that aren't JSON, a time
// zone name it doesn't know, or a date that isn't YYYY-MM-DD.
func dateTime(now time.Time, raw json.RawMessage) (string, error) {
	var args datetimeArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("datetime: arguments: %w", err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s.\n", long(now))

	if tz := strings.TrimSpace(args.Timezone); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return "", fmt.Errorf("datetime: %q isn't a time zone name; use one such as Asia/Kolkata", tz)
		}
		fmt.Fprintf(&b, "In %s: %s.\n", tz, long(now.In(loc)))
	}

	if d := strings.TrimSpace(args.Date); d != "" {
		// ParseInLocation reads the date as midnight in the local zone, the
		// same zone "today" means below.
		date, err := time.ParseInLocation("2006-01-02", d, now.Location())
		if err != nil {
			return "", fmt.Errorf("datetime: date %q isn't YYYY-MM-DD", d)
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		// Rounding the hours to days absorbs a daylight-saving change,
		// when a "day" is 23 or 25 hours long.
		days := int(date.Sub(today).Round(24*time.Hour) / (24 * time.Hour))
		fmt.Fprintf(&b, "%s is a %s, %s.\n", d, date.Weekday(), fromToday(days))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// long writes t for the model: "Thursday, 24 September 2026, 14:05 EDT
// (UTC-04:00)", with the ISO form after it so the model can quote either.
func long(t time.Time) string {
	return t.Format("Monday, 2 January 2006, 15:04 MST (UTC-07:00)") + "; ISO " + t.Format(time.RFC3339)
}

// fromToday says how far a date is from today in words.
func fromToday(days int) string {
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 0:
		return fmt.Sprintf("%d days from today", days)
	default:
		return fmt.Sprintf("%d days ago", -days)
	}
}
