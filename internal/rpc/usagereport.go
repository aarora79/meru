// This file holds the words every client uses for the anonymous daily usage
// report, and an example report, so meru chat, Meru.app, meru setup and the
// Mac installer say the same thing about what leaves the machine. The
// desktop page keeps its own copy, which a test holds equal to this one.
// See ARCHITECTURE.md, "Anonymous usage report".

package rpc

import (
	"encoding/json"
	"strings"
)

// UsageReportFields names what the report holds, in words, for a sentence
// such as "Meru sends one anonymous count a day: " + UsageReportFields.
// It names every field of report version 2.
const UsageReportFields = "its version, your system and chip, the model set, a rough install age, " +
	"rough counts of yesterday's questions and of the days you used it last week, " +
	"how many weeks in a row it has reported, the version it reported last, " +
	"and whether it is its first report of the week, the month or ever. " +
	"No ID, no questions, no files."

// UsageReportExample returns a report as merud would send it for a new
// install of version, such as "v0.7.7", on goos and goarch with the model
// set profile: no questions yet, a first report of everything. A version
// that isn't a release shows as "dev", as merud's own preview does; a
// development build never sends.
func UsageReportExample(version, goos, goarch, profile string) string {
	// The text after each field is a struct tag: it names the JSON key
	// encoding/json writes, in the order merud writes them.
	example := struct {
		V          int    `json:"v"`
		Meru       string `json:"meru"`
		OS         string `json:"os"`
		Arch       string `json:"arch"`
		Profile    string `json:"profile"`
		Age        string `json:"age"`
		Questions  string `json:"questions"`
		ActiveDays string `json:"active_days"`
		Streak     string `json:"streak"`
		Previous   string `json:"previous"`
		WeekFirst  bool   `json:"week_first"`
		MonthFirst bool   `json:"month_first"`
		First      bool   `json:"first"`
	}{2, releaseOrDev(version), goos, goarch, profile, "week", "0", "0", "1", "none", true, true, true}
	b, _ := json.Marshal(example) // a struct of strings and bools always marshals
	return string(b)
}

// releaseOrDev returns v without its "v" when v is a release, such as
// "0.7.7" for "v0.7.7", and "dev" for any other build.
func releaseOrDev(v string) string {
	n, ok := strings.CutPrefix(v, "v")
	if !ok || strings.Count(n, ".") != 2 || strings.ContainsAny(n, "-+ ") {
		return "dev"
	}
	return n
}
