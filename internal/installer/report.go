// This file holds the anonymous daily count on the Start Meru step: what
// the screen shows about it, and the write of the user's answer to
// [usage_report] send. On a new install the box starts ticked, unless
// DO_NOT_TRACK is set. When config.toml was there before the installer
// opened, as on an update, the box starts from the choice it holds, and
// the installer writes only a choice the user changed: an update never
// turns the report on behind the user's back. merud does
// the sending, and only after Meru.app or meru chat has shown the notice;
// the installer only records the choice. See ARCHITECTURE.md, "Anonymous
// usage report".

package installer

import (
	"errors"
	"io/fs"
	"os"
	"runtime"

	"github.com/aarora79/meru/internal/about"
	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// reportFields says what the count holds, under the box on the screen.
const reportFields = "It holds " + rpc.UsageReportFields +
	" Nothing goes until you open Meru.app or meru chat, and the first one waits a day."

// EarlierChoice is what config.toml said about the count when the
// installer opened. Had is false when there was no config.toml, as on a
// new install; Send is [usage_report] send then: "on", "off", or "" when
// the user hadn't chosen.
type EarlierChoice struct {
	Had  bool
	Send string
}

// ReadEarlierChoice reads the choice config.toml at path holds now. merud
// and the earlier steps may write config.toml later in the run, so the
// Bridge calls it once, when the installer opens. A config.toml that
// doesn't load counts as one with no choice, so the box starts unticked
// and nothing is written unless the user ticks it.
func ReadEarlierChoice(path string) EarlierChoice {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return EarlierChoice{}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return EarlierChoice{Had: true}
	}
	return EarlierChoice{Had: true, Send: cfg.UsageReport.Send}
}

// start returns how the box starts: from the earlier choice when there was
// a config.toml, and ticked on a new install unless DO_NOT_TRACK is set.
func (e EarlierChoice) start(dnt bool) bool {
	if e.Had {
		return e.Send == config.ReportOn
	}
	return !dnt
}

// ReportOffer is what the Start Meru screen shows about the count. The
// JSON names are what the page reads.
type ReportOffer struct {
	// Send is how the box starts; see EarlierChoice.start.
	Send bool `json:"send"`
	// Earlier is the choice config.toml already held, "on", "off" or "",
	// and HadConfig whether there was one, so the screen can say the box
	// keeps it.
	Earlier   string `json:"earlier"`
	HadConfig bool   `json:"hadConfig"`
	// DoNotTrack is true when DO_NOT_TRACK is set, so the screen can say
	// why the box starts unticked.
	DoNotTrack bool `json:"doNotTrack"`
	// Fields lists what the count holds, and Example shows one, as JSON.
	Fields  string `json:"fields"`
	Example string `json:"example"`
}

// OfferReport returns the offer for a Mac running the model set profile,
// with earlier the choice config.toml held when the installer opened and
// doNotTrack the value of DO_NOT_TRACK in the installer's environment. Any
// value but "" and "0" asks for no reports.
func OfferReport(profile string, earlier EarlierChoice, doNotTrack string) ReportOffer {
	dnt := doNotTrack != "" && doNotTrack != "0"
	return ReportOffer{
		Send: earlier.start(dnt), Earlier: earlier.Send, HadConfig: earlier.Had,
		DoNotTrack: dnt, Fields: reportFields, Example: reportExample(profile),
	}
}

// reportExample returns an example report for this Mac and profile, as
// merud would send it for a new install; see rpc.UsageReportExample.
func reportExample(profile string) string {
	return rpc.UsageReportExample(about.Version(), runtime.GOOS, runtime.GOARCH, profile)
}

// SaveReport records the user's answer, send or not, as [usage_report]
// send in config.toml, and returns a line saying where the count stands.
// On a new install it writes the answer. When config.toml was there before
// the installer opened, it writes only when the user changed the box from
// where it started, so an update keeps "off" off and leaves "not chosen"
// for meru chat and the app to ask. It makes config.toml from the template
// first when there is none. It fails when config.toml doesn't load or
// can't be written.
func SaveReport(p Paths, send bool, earlier EarlierChoice, dnt bool) (string, error) {
	if _, err := EnsureConfig(p.Config()); err != nil {
		return "", err
	}
	const change = " meru report on or meru report off changes it."
	if earlier.Had && send == earlier.start(dnt) {
		switch earlier.Send {
		case config.ReportOn:
			return "Meru keeps sending the anonymous daily count, as you chose before." + change, nil
		case config.ReportOff:
			return "Meru keeps the anonymous daily count off, as you chose before." + change, nil
		}
		return "Meru.app and meru chat will ask about the anonymous daily count." + change, nil
	}
	choice, line := config.ReportOff, "Meru won't send the anonymous daily count."
	if send {
		choice, line = config.ReportOn, "Meru sends one anonymous count a day, starting the day after you first open Meru.app or meru chat."
	}
	if err := catalog.SetTableString(p.Config(), "usage_report", "send", choice, nil); err != nil {
		return "", err
	}
	return line + change, nil
}
