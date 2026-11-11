// This file holds the anonymous daily count (the usage report) as the chat
// screen shows it: the status request the chat sends at start and on its
// status timer, the one-line banner above the input, the /report box that
// shows the full notice, the next report and the keys that turn it on or
// off, and the section the /usage box adds for it. merud decides
// everything; the chat only draws its reply (rpc.OpUsageReport). See
// ARCHITECTURE.md, "Anonymous usage report".

package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// The banner's words, one line in the accent colour above the input. The
// /report box holds the full notice.
const (
	bannerNewText = "Meru sends one anonymous count a day. /report to see it or turn it off."
	bannerAskText = "Meru can send one anonymous count a day. It stays off until you choose: /report."
)

// The /report box's full notice: what the count holds, then what happens
// next. The desktop app shows the same words.
const (
	reportFields      = "one anonymous count a day: " + rpc.UsageReportFields
	reportNewText     = "Meru sends " + reportFields
	reportFirstText   = " Your first one goes tomorrow."
	reportAskText     = "Meru can now send " + reportFields + " It stays off unless you say yes."
	reportSectionHead = "Anonymous daily count"
)

// reportMsg carries merud's answer to one usage_report request. kind is
// the action the request sent, such as rpc.UsageReportSeen. answered is
// true when merud replied at all, even with an error event; an older merud
// answers with an "unknown op" error, which gives answered true and an
// err. status is nil whenever err is set.
type reportMsg struct {
	kind     string
	status   *rpc.UsageReportStatus
	answered bool
	err      error
}

// reportBox is the open /report box. busy is true while a request the box
// sent waits for merud, and err says why the last one failed, in words
// the box shows as they are.
type reportBox struct {
	busy bool
	err  string
	note string // "Sent today's report." after a send that went
}

// reportCmd returns a command that sends one usage_report request with
// the action kind and hands back the reply as a reportMsg. A status check
// waits as long as the other status checks; a change to config may take
// longer, so it gets the longer wait.
func reportCmd(ask askFunc, kind string) tea.Cmd {
	timeout := pingTimeout
	// A send waits for the collector too, up to its own 10 seconds.
	if kind == rpc.UsageReportOn || kind == rpc.UsageReportOff || kind == rpc.UsageReportSend {
		timeout = changeTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		// defer runs cancel when this function returns, freeing the timer.
		defer cancel()
		req := rpc.Request{Op: rpc.OpUsageReport, Kind: kind, Source: rpc.SourceTUI}
		for ev, err := range ask(ctx, req, nil) {
			if err != nil {
				return reportMsg{kind: kind, err: err}
			}
			switch ev.Type {
			case rpc.EventUsageReport:
				if ev.UsageReport != nil {
					return reportMsg{kind: kind, status: ev.UsageReport, answered: true}
				}
			case rpc.EventError:
				return reportMsg{kind: kind, answered: true, err: errors.New(ev.Error)}
			}
		}
		return reportMsg{kind: kind, err: errors.New("merud sent no usage_report reply")}
	}
}

// reportStatusCmd returns the status check the chat sends at start and on
// its timer.
func reportStatusCmd(ask askFunc) tea.Cmd {
	return reportCmd(ask, rpc.UsageReportStatusKind)
}

// applyReport takes in one usage_report reply. A reply with a status
// replaces the one the chat held. An error event, which is how an older
// merud answers an op it doesn't know, drops the status, so the banner
// and the /usage section go away and the user sees no error. When merud
// couldn't be reached, the chat keeps what it had, as the header keeps
// the usage numbers.
//
// The first reply in this run that shows the banner also returns the
// command that tells merud the banner showed ("seen"). merud needs that
// before it sends any report. The seen flag keeps it to one request per
// run, however often the timer asks.
func (m *Model) applyReport(msg reportMsg) tea.Cmd {
	hadBanner := m.reportBanner() != ""
	switch {
	case msg.err == nil:
		m.report = msg.status
	case msg.answered && msg.kind != rpc.UsageReportSend:
		// A refused send is merud saying why, not an older merud, so the
		// chat keeps the status it has.
		m.report = nil
	}
	if b := m.reportBox; b != nil && msg.kind != rpc.UsageReportSeen {
		b.busy = false
		b.err, b.note = "", ""
		switch {
		case msg.err == nil && msg.kind == rpc.UsageReportSend:
			b.note = "Sent today's report."
		case msg.err == nil:
		case msg.kind == rpc.UsageReportSend:
			b.err = "Not sent: " + strings.TrimPrefix(msg.err.Error(), "not sent: ")
		case msg.kind == rpc.UsageReportStatusKind:
			b.err = "merud didn't answer: " + msg.err.Error()
		default:
			b.err = "merud didn't make the change: " + msg.err.Error()
		}
	}
	if (m.reportBanner() != "") != hadBanner {
		m.layout() // the banner line came or went
	}
	if m.reportBanner() != "" && !m.reportSeen && m.ask != nil {
		m.reportSeen = true
		return reportCmd(m.ask, rpc.UsageReportSeen)
	}
	return nil
}

// reportBanner returns the banner's text for the status the chat holds,
// or "" when no banner shows.
func (m *Model) reportBanner() string {
	if m.report == nil {
		return ""
	}
	switch m.report.Banner {
	case rpc.BannerNew:
		return bannerNewText
	case rpc.BannerAsk:
		return bannerAskText
	}
	return ""
}

// reportBannerLine draws the banner, cut to the screen's width.
func (m *Model) reportBannerLine() string {
	return m.style.accent.Render(ansi.Truncate(m.reportBanner(), m.width, "…"))
}

// reportCommand runs /report: it opens the box and asks merud how the
// count stands now, so the box shows fresh numbers.
func (m Model) reportCommand() (tea.Model, tea.Cmd) {
	m.openBox()
	m.reportBox = &reportBox{busy: true}
	return m, reportCmd(m.ask, rpc.UsageReportStatusKind)
}

// reportKey handles one key in the /report box. While the count is on, o
// turns it off and g closes the notice, and t sends today's report now when
// merud says a send would go (CanSend). While it is off or not chosen, s
// turns it on, and n turns it off, which only means something before the
// user has chosen. A key waits while a request is out, so two presses
// can't race.
func (m *Model) reportKey(msg tea.KeyMsg) tea.Cmd {
	b, r := m.reportBox, m.report
	if b.busy || r == nil {
		return nil
	}
	var kind string
	switch {
	case r.Choice == "on" && isKey(msg, "o"):
		kind = rpc.UsageReportOff
	case r.Choice == "on" && isKey(msg, "g"):
		kind = rpc.UsageReportDismiss
	case r.CanSend && isKey(msg, "t"):
		kind = rpc.UsageReportSend
	case r.Choice != "on" && isKey(msg, "s"):
		kind = rpc.UsageReportOn
	case r.Choice == "" && isKey(msg, "n"):
		kind = rpc.UsageReportOff
	default:
		return nil
	}
	b.busy = true
	return reportCmd(m.ask, kind)
}

// reportKeys returns the help line's keys for the /report box, which
// depend on the choice.
func (m *Model) reportKeys() keyList {
	bind := func(k, text string) key.Binding { return key.NewBinding(key.WithKeys(k), key.WithHelp(k, text)) }
	r := m.report
	switch {
	case r == nil:
		return nil
	case r.Choice == "on" && r.CanSend:
		return keyList{bind("t", "send today's now"), bind("o", "turn off"), bind("g", "got it")}
	case r.Choice == "on":
		return keyList{bind("o", "turn off"), bind("g", "got it")}
	case r.Choice == "":
		return keyList{bind("s", "share"), bind("n", "don't share")}
	}
	return keyList{bind("s", "share")}
}

// reportBoxView draws the /report box for a pane width columns wide and
// height rows tall (see boxPane): the full notice when merud wants one
// shown, how the count stands, and the next report as merud would send
// it.
func (m *Model) reportBoxView(width, height int) string {
	b, r := m.reportBox, m.report
	avail := boxRoom(width)
	wrap := func(s string) []string { return strings.Split(ansi.Wrap(s, avail, ""), "\n") }

	var body []string
	switch {
	case r == nil && b.busy:
		body = []string{m.style.dim.Render("asking merud…")}
	case r == nil:
		body = wrap("This merud has no daily count. Update Meru and restart merud to see it.")
	default:
		switch r.Banner {
		case rpc.BannerNew:
			text := reportNewText
			if !r.Sent {
				text += reportFirstText
			}
			body = append(wrap(text), "")
		case rpc.BannerAsk:
			body = append(wrap(reportAskText), "")
		}
		body = append(body, m.reportStatusLines(r, avail)...)
		if r.Next != "" {
			body = append(body, "", m.style.dim.Render("The next one, as merud would send it:"))
			body = append(body, wrap(r.Next)...)
		}
		if b.busy {
			body = append(body, "", m.style.dim.Render("asking merud…"))
		}
		if b.note != "" {
			body = append(body, "", b.note)
		}
		if b.err != "" {
			body = append(body, "")
			body = append(body, wrap(b.err)...)
		}
	}
	return m.boxPane(reportSectionHead, body, "/usage lists the counts sent so far.", width, height)
}

// reportStatusLines writes how the count stands: on, off or not chosen;
// why nothing will send, when merud says; when the next one can go; and
// where it goes, when config names a collector other than Meru's own.
func (m *Model) reportStatusLines(r *rpc.UsageReportStatus, width int) []string {
	state := "Not chosen"
	switch r.Choice {
	case "on":
		state = "On"
	case "off":
		state = "Off"
	}
	lines := []string{state}
	if r.Blocked != "" {
		lines = append(lines, strings.Split(ansi.Wrap(r.Blocked, width, ""), "\n")...)
	}
	if r.NextAt != "" {
		lines = append(lines, "Next one after "+stampLabel(r.NextAt))
	}
	if r.URL != "" {
		lines = append(lines, ansi.Truncate("Sent to: "+r.URL, width, "…"))
	}
	return lines
}

// reportUsageLines writes the /usage box's section for the count: a
// heading, how it stands, the latest lines of its log, one each, and a
// pointer to /report. It returns nothing when merud gave no status, as an
// older merud gives none.
func (m *Model) reportUsageLines(width int) []string {
	r := m.report
	if r == nil {
		return nil
	}
	lines := []string{"", m.style.dim.Render(reportSectionHead)}
	lines = append(lines, m.reportStatusLines(r, width)...)
	for _, e := range r.Recent {
		text := e.Body
		if e.Error != "" {
			text = e.Error
		}
		line := stampLabel(e.At) + "  " + e.Type
		if text != "" {
			line += "  " + text
		}
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return append(lines, m.style.dim.Render("/report shows the next one and turns it on or off."))
}
