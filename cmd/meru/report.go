// This file holds `meru report`, `meru report on`, `meru report off` and
// `meru report send`:
// they show where the anonymous daily count stands, the exact report
// merud would send next and the last few lines of its log, and set the
// choice in [usage_report] send; send asks merud to send today's report
// now. merud decides and sends; this side asks and prints. See ARCHITECTURE.md, "Anonymous usage report".
//
// A one-shot command never tells merud it showed the notice ("seen"):
// only meru chat and the desktop app count as showing it, so merud never
// starts sending because a script ran `meru report`.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/about"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// reportRecent is how many log lines `meru report` prints, newest first.
const reportRecent = 5

// reportFields says what the count holds. Setup prints it before it asks.
const reportFields = "Meru can send one anonymous count a day: its version, your system and chip, the model set, and rough install age and question count. No ID, no questions, no files. Nothing goes until you open meru chat or the app, and the first one waits a day."

// isReportArgs reports whether args, the words after "report", are ones
// `meru report` takes: none, "on", "off" or "send". Anything else, such as
// `meru report on the weather`, reads as a question that starts with
// "report".
func isReportArgs(args []string) bool {
	return len(args) == 0 || (len(args) == 1 &&
		(args[0] == config.ReportOn || args[0] == config.ReportOff || args[0] == rpc.UsageReportSend))
}

// reportCmd runs `meru report [on|off|send]`. With no word it asks merud
// how the count stands; with "on" or "off" it sets the choice first; with
// "send" it asks merud to send today's report now, which merud refuses,
// with the reason, once one went today or while a rule holds it back.
// Either way it prints the status merud sends back. It fails when merud can't be
// reached, when merud refuses, and with a plain line when merud is older
// than the usage_report op.
func reportCmd(ctx context.Context, socket string, args []string, stdout io.Writer) error {
	kind := rpc.UsageReportStatusKind
	if len(args) == 1 {
		kind = args[0] // "on", "off" and "send" are also the op's Kind values
	}
	req := rpc.Request{Op: rpc.OpUsageReport, Kind: kind, Source: rpc.SourceCLI}
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventUsageReport:
			if ev.UsageReport == nil {
				return errors.New("merud sent no report status")
			}
			text := reportText(*ev.UsageReport, time.Local)
			if kind == rpc.UsageReportSend {
				text = "Sent today's report.\n\n" + text
			}
			_, err := io.WriteString(stdout, text)
			return err
		case rpc.EventError:
			if strings.Contains(ev.Error, "unknown op") {
				return errors.New("this merud is older than meru report; update merud and restart it")
			}
			return errors.New(ev.Error)
		}
	}
	return errors.New("merud sent no report status")
}

// reportText writes s as `meru report` prints it, with times in loc:
//
//	Anonymous daily count: On
//	Next one after 2026-11-12 00:47.
//
//	The next report:
//	{"v":1,"meru":"0.7.3",...}
//
//	Recent:
//	  2026-11-11 00:52  sent  {"v":1,...}
//
// Taking loc as a parameter, not reading the local zone inside, lets the
// tests pin the times.
func reportText(s rpc.UsageReportStatus, loc *time.Location) string {
	var b strings.Builder
	choice := "Not chosen"
	switch s.Choice {
	case config.ReportOn:
		choice = "On"
	case config.ReportOff:
		choice = "Off"
	}
	fmt.Fprintf(&b, "Anonymous daily count: %s\n", choice)
	if s.Choice == "" {
		b.WriteString("Run meru report on or meru report off to choose.\n")
	}
	if s.Blocked != "" {
		b.WriteString(s.Blocked + "\n")
	}
	if s.NextAt != "" {
		fmt.Fprintf(&b, "Next one after %s.\n", reportTime(s.NextAt, loc))
	}
	if s.URL != "" {
		fmt.Fprintf(&b, "Sent to: %s\n", s.URL)
	}
	if s.Next != "" {
		fmt.Fprintf(&b, "\nThe next report:\n%s\n", s.Next)
	}
	if len(s.Recent) > 0 {
		b.WriteString("\nRecent:\n")
		// s.Recent[:n] is a slice of the first n entries; min keeps n
		// inside the list.
		for _, e := range s.Recent[:min(len(s.Recent), reportRecent)] {
			fmt.Fprintf(&b, "  %s  %s", reportTime(e.At, loc), e.Type)
			// A switch with no value after it runs the first case whose
			// test is true, like a chain of if and else if.
			switch {
			case e.Error != "":
				fmt.Fprintf(&b, "  %s", e.Error)
			case e.Body != "":
				fmt.Fprintf(&b, "  %s", e.Body)
			case e.Client != "":
				fmt.Fprintf(&b, "  by %s", e.Client)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// reportTime writes the RFC 3339 time at in loc, as "2006-01-02 15:04",
// like log.go's localTime but to the minute and in a zone the caller
// picks. A time that doesn't parse comes back as merud sent it, so a newer
// format still shows.
func reportTime(at string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// reportExample returns an example report for this machine and profile,
// shaped as merud sends it: this build's version when it is a release,
// this system and chip, profile, a new install's age and no questions.
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
	b, _ := json.Marshal(example)
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
