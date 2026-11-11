// This file tests the anonymous daily count on the Start Meru step: how
// the box starts, the example it shows, and the write of the answer to
// [usage_report] send.

package installer

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// TestOfferReport checks that the box starts ticked unless DO_NOT_TRACK
// asks for no reports, and that the example is this Mac's.
func TestOfferReport(t *testing.T) {
	for _, tt := range []struct {
		value string
		send  bool
	}{{"", true}, {"0", true}, {"1", false}, {"yes", false}} {
		o := OfferReport("lite", EarlierChoice{}, tt.value)
		if o.Send != tt.send || o.DoNotTrack == tt.send {
			t.Errorf("DO_NOT_TRACK=%q: send %v, doNotTrack %v; want send %v", tt.value, o.Send, o.DoNotTrack, tt.send)
		}
	}
	o := OfferReport("full", EarlierChoice{}, "")
	var got map[string]any
	if err := json.Unmarshal([]byte(o.Example), &got); err != nil {
		t.Fatalf("example %q isn't JSON: %v", o.Example, err)
	}
	if got["os"] != runtime.GOOS || got["arch"] != runtime.GOARCH || got["profile"] != "full" || got["v"] != 2.0 {
		t.Errorf("example = %s, want this system, chip and profile", o.Example)
	}
	if !strings.Contains(o.Fields, "No ID, no questions, no files.") {
		t.Errorf("fields = %q", o.Fields)
	}
}

// TestSaveReport writes each answer and checks the key and the comments,
// and that a missing config.toml comes from the template first.
func TestSaveReport(t *testing.T) {
	for _, tt := range []struct {
		send bool
		want string
	}{{true, config.ReportOn}, {false, config.ReportOff}} {
		p := tempHome(t)
		if _, err := SaveReport(p, tt.send, EarlierChoice{}, false); err != nil {
			t.Fatalf("SaveReport(%v): %v", tt.send, err)
		}
		cfg, err := config.Load(p.Config())
		if err != nil {
			t.Fatal(err)
		}
		if cfg.UsageReport.Send != tt.want {
			t.Errorf("send = %q, want %q", cfg.UsageReport.Send, tt.want)
		}
		want := strings.Replace(config.Template(), "\nsend = \"\"\n", "\nsend = \""+tt.want+"\"\n", 1)
		if got := readFile(t, p.Config()); got != want {
			t.Errorf("SaveReport changed more than the send line:\n%s", got)
		}
	}

	p := Paths{Home: t.TempDir()} // no config.toml yet
	if _, err := SaveReport(p, false, EarlierChoice{}, false); err != nil {
		t.Fatalf("SaveReport with no config: %v", err)
	}
	if cfg, err := config.Load(p.Config()); err != nil || cfg.UsageReport.Send != config.ReportOff {
		t.Errorf("send = %q, %v; want off", cfg.UsageReport.Send, err)
	}
}

// TestUpdateKeepsEarlierChoice is the promise to anyone who runs the
// installer again: the box starts from the choice config.toml holds, and an
// untouched box writes nothing, so "off" stays off and "not chosen" stays
// for the clients to ask. Only a box the user changed writes.
func TestUpdateKeepsEarlierChoice(t *testing.T) {
	for _, tt := range []struct {
		name    string
		earlier string // send in config.toml before the installer opened
		box     bool   // the box when the user presses Continue
		start   bool   // how the box should start
		want    string // send after the step
	}{
		{"off, untouched", config.ReportOff, false, false, config.ReportOff},
		{"on, untouched", config.ReportOn, true, true, config.ReportOn},
		{"not chosen, untouched", "", false, false, ""},
		{"not chosen, ticked", "", true, false, config.ReportOn},
		{"off, ticked", config.ReportOff, true, false, config.ReportOn},
		{"on, unticked", config.ReportOn, false, true, config.ReportOff},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := tempHome(t)
			body := strings.Replace(config.Template(), "\nsend = \"\"\n", "\nsend = \""+tt.earlier+"\"\n", 1)
			writeFile(t, p.Config(), body)
			earlier := ReadEarlierChoice(p.Config())
			if !earlier.Had || earlier.Send != tt.earlier {
				t.Fatalf("ReadEarlierChoice = %+v, want had and %q", earlier, tt.earlier)
			}
			// DO_NOT_TRACK doesn't change an update: the earlier choice wins.
			if o := OfferReport("lite", earlier, "1"); o.Send != tt.start || !o.HadConfig || o.Earlier != tt.earlier {
				t.Errorf("offer = %+v, want the box to start %v from %q", o, tt.start, tt.earlier)
			}
			if _, err := SaveReport(p, tt.box, earlier, true); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(p.Config())
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UsageReport.Send != tt.want {
				t.Errorf("send = %q after the step, want %q", cfg.UsageReport.Send, tt.want)
			}
		})
	}
	if e := ReadEarlierChoice(Paths{Home: t.TempDir()}.Config()); e.Had {
		t.Errorf("no config.toml reads as %+v, want a new install", e)
	}
}

// TestBridgeStartReport checks the Start Meru screen's box under
// DO_NOT_TRACK, and that Continue writes the answer before it starts
// merud: here merud isn't installed, so the step fails after the write.
func TestBridgeStartReport(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	b, _ := newBridge(t, &fakeRunner{})
	scr, err := b.Screen(StepStart)
	if err != nil || scr.Report == nil || scr.Report.Send || !scr.Report.DoNotTrack {
		t.Fatalf("Start Meru screen = %+v, %v; want the box unticked for DO_NOT_TRACK", scr.Report, err)
	}

	if v, _ := b.Run(StepProfile, Input{Profile: Profile{Name: "Dana Reyes"}}); status(v, StepProfile) != StatusDone {
		t.Fatal("About you didn't finish")
	}
	v, _ := b.Run(StepStart, Input{Report: true})
	if status(v, StepStart) != StatusFailed {
		t.Errorf("Start Meru with no merud = %v, want failed", status(v, StepStart))
	}
	cfg, err := config.Load(b.paths.Config())
	if err != nil || cfg.UsageReport.Send != config.ReportOn {
		t.Errorf("send = %q, %v; want on, written before merud starts", cfg.UsageReport.Send, err)
	}
}
