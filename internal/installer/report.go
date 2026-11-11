// This file holds the anonymous daily count on the Start Meru step: what
// the screen shows about it, with a box ticked unless DO_NOT_TRACK is set,
// and the write of the user's answer to [usage_report] send. merud does
// the sending, and only after Meru.app or meru chat has shown the notice;
// the installer only records the choice. See ARCHITECTURE.md, "Anonymous
// usage report".

package installer

import (
	"encoding/json"
	"runtime"
	"strings"

	"github.com/aarora79/meru/internal/about"
	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// reportFields says what the count holds, under the box on the screen.
const reportFields = "Its version, your system and chip, the model set, and rough install age and question count. No ID, no questions, no files. Nothing goes until you open Meru.app or meru chat, and the first one waits a day."

// ReportOffer is what the Start Meru screen shows about the count. The
// JSON names are what the page reads.
type ReportOffer struct {
	// Send is how the box starts: ticked, unless DO_NOT_TRACK is set.
	Send bool `json:"send"`
	// DoNotTrack is true when DO_NOT_TRACK is set, so the screen can say
	// why the box starts unticked.
	DoNotTrack bool `json:"doNotTrack"`
	// Fields lists what the count holds, and Example shows one, as JSON.
	Fields  string `json:"fields"`
	Example string `json:"example"`
}

// OfferReport returns the offer for a Mac running the model set profile,
// with doNotTrack the value of DO_NOT_TRACK in the installer's
// environment. Any value but "" and "0" asks for no reports.
func OfferReport(profile, doNotTrack string) ReportOffer {
	dnt := doNotTrack != "" && doNotTrack != "0"
	return ReportOffer{Send: !dnt, DoNotTrack: dnt, Fields: reportFields, Example: reportExample(profile)}
}

// reportExample returns an example report for this Mac and profile,
// shaped as merud sends it: this build's version when it is a release,
// this system and chip, the profile, a new install's age and no
// questions.
func reportExample(profile string) string {
	// The text after each field is a struct tag: it names the JSON key
	// encoding/json writes for that field, in the order merud writes them.
	example := struct {
		V         int    `json:"v"`
		Meru      string `json:"meru"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		Profile   string `json:"profile"`
		Age       string `json:"age"`
		Questions string `json:"questions"`
	}{1, exampleVersion(about.Version()), runtime.GOOS, runtime.GOARCH, profile, "week", "0"}
	b, _ := json.Marshal(example) // a struct of strings always marshals
	return string(b)
}

// exampleVersion returns v without its "v" when v is a release, such as
// "0.7.3" for "v0.7.3", and "dev" for any other build, as merud's own
// preview does. A development build never sends a report.
func exampleVersion(v string) string {
	n, ok := strings.CutPrefix(v, "v")
	if !ok || strings.Count(n, ".") != 2 || strings.ContainsAny(n, "-+ ") {
		return "dev"
	}
	return n
}

// SaveReport writes the user's answer, send or not, as [usage_report]
// send in config.toml, and returns a line saying so. It makes config.toml
// from the template first when there is none. It fails when config.toml
// doesn't load or can't be written.
func SaveReport(p Paths, send bool) (string, error) {
	if _, err := EnsureConfig(p.Config()); err != nil {
		return "", err
	}
	choice, line := config.ReportOff, "Meru won't send the anonymous daily count."
	if send {
		choice, line = config.ReportOn, "Meru sends one anonymous count a day, starting the day after you first open Meru.app or meru chat."
	}
	if err := catalog.SetTableString(p.Config(), "usage_report", "send", choice, nil); err != nil {
		return "", err
	}
	return line + " meru report on or meru report off changes it.", nil
}
