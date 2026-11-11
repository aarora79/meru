// This file tests the anonymous daily count in the chat: the banner and
// the room it takes, the one "seen" a run sends, the /report box and its
// keys, and the section the /usage box adds. Each test fakes merud's
// reply, as usage_test.go does.

package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// reportEvents returns merud's reply to a usage_report request that
// carries st.
func reportEvents(st rpc.UsageReportStatus) []rpc.Event {
	return []rpc.Event{{Type: rpc.EventUsageReport, UsageReport: &st}, {Type: rpc.EventDone}}
}

// TestReportLayout checks that an 80x24 terminal with both the banner and
// the attachments line showing still gives the conversation its rows:
// the two lines take one row each, and View fills the screen exactly.
func TestReportLayout(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.resize(80, 24)
	plain := m.conversation.Height
	m.report = &rpc.UsageReportStatus{Banner: rpc.BannerNew}
	m.attached = []attachment{{name: "garden-plan.pdf", kind: rpc.AttachFile, size: "2.4 MB"}}
	m.layout()
	if got := m.conversation.Height; got != plain-2 || got < 10 {
		t.Errorf("conversation height = %d, want %d and at least 10", got, plain-2)
	}
	view := m.View()
	if n := strings.Count(view, "\n") + 1; n != 24 {
		t.Errorf("view has %d lines, want 24:\n%s", n, view)
	}
	if !strings.Contains(view, bannerNewText) || !strings.Contains(view, "attached: garden-plan.pdf") {
		t.Errorf("view lacks the banner or the attachments line:\n%s", view)
	}
}

// TestReportBanner checks each state merud's reply can put the banner in,
// and that only a banner that shows sends "seen".
func TestReportBanner(t *testing.T) {
	tests := []struct {
		name     string
		merud    *fakeMerud
		want     string // the banner's text, "" for none
		wantSeen bool
	}{
		{"new install", &fakeMerud{events: reportEvents(rpc.UsageReportStatus{Choice: "on", Banner: rpc.BannerNew})}, bannerNewText, true},
		{"upgrade", &fakeMerud{events: reportEvents(rpc.UsageReportStatus{Banner: rpc.BannerAsk})}, bannerAskText, true},
		{"no banner", &fakeMerud{events: reportEvents(rpc.UsageReportStatus{Choice: "off"})}, "", false},
		{"older merud", &fakeMerud{events: []rpc.Event{{Type: rpc.EventError, Error: `unknown op "usage_report"`}}}, "", false},
		{"merud down", &fakeMerud{err: errors.New("no socket")}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.merud.ask, newFakeSender())
			m, cmd := update(t, m, reportStatusCmd(tt.merud.ask)())
			if r := tt.merud.reqs[0]; r.Op != rpc.OpUsageReport || r.Kind != rpc.UsageReportStatusKind || r.Source != rpc.SourceTUI {
				t.Errorf("request = %+v, want a status check from the chat", r)
			}
			if got := m.reportBanner(); got != tt.want {
				t.Errorf("banner = %q, want %q", got, tt.want)
			}
			if view := m.View(); tt.want != "" && !strings.Contains(view, tt.want) {
				t.Errorf("view lacks the banner:\n%s", view)
			}
			if (cmd != nil) != tt.wantSeen {
				t.Fatalf("seen command = %v, want one: %v", cmd != nil, tt.wantSeen)
			}
			if cmd == nil {
				return
			}
			cmd()
			if r := lastReq(t, tt.merud); r.Kind != rpc.UsageReportSeen || r.Source != rpc.SourceTUI {
				t.Errorf("request = %+v, want seen from the chat", r)
			}
		})
	}
}

// TestReportSeenOnce checks that two status replies with the banner send
// "seen" once: the status timer asks again every few seconds.
func TestReportSeenOnce(t *testing.T) {
	merud := &fakeMerud{events: reportEvents(rpc.UsageReportStatus{Banner: rpc.BannerAsk})}
	m := testModel(merud.ask, newFakeSender())
	m, first := update(t, m, reportStatusCmd(merud.ask)())
	m, second := update(t, m, reportStatusCmd(merud.ask)())
	if first == nil || second != nil {
		t.Errorf("seen commands: first %v, second %v; want only the first", first != nil, second != nil)
	}
	if !m.reportSeen {
		t.Error("reportSeen = false after the banner showed")
	}
}

// TestReportStatusOnTimer checks that the chat asks how the count stands
// at start and on the status timer.
func TestReportStatusOnTimer(t *testing.T) {
	merud := &fakeMerud{}
	m := testModel(merud.ask, newFakeSender())
	for _, cmd := range []tea.Cmd{m.Init(), func() tea.Cmd { _, c := update(t, m, refreshMsg{}); return c }()} {
		merud.reqs = nil
		batch, ok := cmd().(tea.BatchMsg)
		if !ok {
			t.Fatal("no batch")
		}
		// The last command is the timer, which would wait for it.
		for _, c := range batch[:len(batch)-1] {
			if c != nil {
				c()
			}
		}
		asked := false
		for _, r := range merud.reqs {
			asked = asked || (r.Op == rpc.OpUsageReport && r.Kind == rpc.UsageReportStatusKind)
		}
		if !asked {
			t.Errorf("requests = %+v, want a usage_report status", merud.reqs)
		}
	}
}

// TestReportBox checks the /report box: its words for each notice, and
// which keys send which change.
func TestReportBox(t *testing.T) {
	tests := []struct {
		name     string
		status   rpc.UsageReportStatus
		want     []string // text the box shows
		dontWant []string
		keys     map[string]string // key to the Kind it sends; "" sends nothing
	}{
		{
			name:     "new, nothing sent yet",
			status:   rpc.UsageReportStatus{Choice: "on", Banner: rpc.BannerNew, Next: `{"v":1}`, NextAt: "2026-11-12T00:30:00Z"},
			want:     []string{"Meru sends one anonymous count a day", "first one goes", "tomorrow.", "On", "Next one after", `{"v":1}`},
			dontWant: []string{"Not chosen"},
			keys:     map[string]string{"o": rpc.UsageReportOff, "g": rpc.UsageReportDismiss, "s": "", "n": ""},
		},
		{
			name:     "new, one sent",
			status:   rpc.UsageReportStatus{Choice: "on", Banner: rpc.BannerNew, Sent: true},
			dontWant: []string{"first one goes"},
			keys:     map[string]string{"t": ""},
		},
		{
			name:   "on, can send now",
			status: rpc.UsageReportStatus{Choice: "on", CanSend: true},
			want:   []string{"send today's now"},
			keys:   map[string]string{"t": rpc.UsageReportSend},
		},
		{
			name:   "upgrade",
			status: rpc.UsageReportStatus{Banner: rpc.BannerAsk, Blocked: "Nothing goes until you say yes."},
			want:   []string{"Meru can now send one anonymous", "unless you say yes.", "Not chosen", "Nothing goes until you say yes."},
			keys:   map[string]string{"s": rpc.UsageReportOn, "n": rpc.UsageReportOff, "o": "", "g": ""},
		},
		{
			name:     "off, no notice",
			status:   rpc.UsageReportStatus{Choice: "off", URL: "https://count.example.org"},
			want:     []string{"Off", "Sent to: https://count.example.org"},
			dontWant: []string{"Meru sends", "Meru can now"},
			keys:     map[string]string{"s": rpc.UsageReportOn, "n": "", "o": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merud := &fakeMerud{events: reportEvents(tt.status)}
			m := testModel(merud.ask, newFakeSender())
			m.resize(80, 40)
			m, cmd := update(t, m, typeText("/report"), press(tea.KeyEnter))
			if m.reportBox == nil || cmd == nil {
				t.Fatal("/report opened no box")
			}
			m, _ = update(t, m, cmd())
			view := m.View()
			for _, w := range tt.want {
				if !strings.Contains(view, w) {
					t.Errorf("box lacks %q:\n%s", w, view)
				}
			}
			for _, w := range tt.dontWant {
				if strings.Contains(view, w) {
					t.Errorf("box shows %q:\n%s", w, view)
				}
			}
			for k, kind := range tt.keys {
				merud.reqs = nil
				next, cmd := update(t, m, typeText(k))
				if kind == "" {
					if cmd != nil {
						t.Errorf("%s sent a request; want none", k)
					}
					continue
				}
				if cmd == nil {
					t.Errorf("%s sent nothing; want %s", k, kind)
					continue
				}
				if !next.reportBox.busy {
					t.Errorf("%s left the box idle; want it to wait", k)
				}
				next, _ = update(t, next, cmd())
				if r := lastReq(t, merud); r.Kind != kind || r.Source != rpc.SourceTUI {
					t.Errorf("%s sent %+v, want %s", k, r, kind)
				}
				if next.reportBox.busy || next.reportBox.err != "" {
					t.Errorf("%s: box busy %v, err %q after the reply", k, next.reportBox.busy, next.reportBox.err)
				}
			}
			m, _ = update(t, m, press(tea.KeyEsc))
			if m.reportBox != nil {
				t.Error("esc left the box open")
			}
		})
	}
}

// TestReportBoxOlderMerud checks that the box says what to do when merud
// doesn't know the op, and that no banner shows.
func TestReportBoxOlderMerud(t *testing.T) {
	merud := &fakeMerud{events: []rpc.Event{{Type: rpc.EventError, Error: `unknown op "usage_report"`}}}
	m := testModel(merud.ask, newFakeSender())
	m, cmd := update(t, m, typeText("/report"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	if view := m.View(); !strings.Contains(view, "no daily count") || m.reportBanner() != "" {
		t.Errorf("view:\n%s", view)
	}
}

// TestReportUsageSection checks the /usage box's section: how the count
// stands, the log lines, and none of it with an older merud.
func TestReportUsageSection(t *testing.T) {
	m := testModel(nil, newFakeSender())
	m.resize(100, 80)
	m.usageBox = &usageBox{windows: usageFixture, detail: &rpc.UsageDetailReply{}}
	if view := m.View(); strings.Contains(view, reportSectionHead) {
		t.Errorf("section shows with no status:\n%s", view)
	}
	m.report = &rpc.UsageReportStatus{
		Choice: "on", NextAt: "2026-11-12T00:30:00Z", URL: "https://count.example.org",
		Recent: []rpc.UsageReportEntry{
			{Type: "failed", At: "2026-11-11T00:40:00Z", Error: "no network"},
			{Type: "sent", At: "2026-11-10T00:20:00Z", Body: `{"v":1}`},
		},
	}
	view := m.View()
	for _, w := range []string{
		reportSectionHead, "On", "Next one after " + stampLabel("2026-11-12T00:30:00Z"),
		"Sent to: https://count.example.org",
		stampLabel("2026-11-11T00:40:00Z") + "  failed  no network",
		stampLabel("2026-11-10T00:20:00Z") + `  sent  {"v":1}`,
		"/report",
	} {
		if !strings.Contains(view, w) {
			t.Errorf("usage box lacks %q:\n%s", w, view)
		}
	}
}

// TestReportSendRefused checks that a send merud refuses shows the reason
// in the box and keeps the status, since the refusal is merud answering,
// not an older merud that doesn't know the op.
func TestReportSendRefused(t *testing.T) {
	merud := &fakeMerud{events: reportEvents(rpc.UsageReportStatus{Choice: "on", CanSend: true})}
	m := testModel(merud.ask, newFakeSender())
	m.resize(80, 40)
	m, cmd := update(t, m, typeText("/report"), press(tea.KeyEnter))
	m, _ = update(t, m, cmd())
	merud.events = []rpc.Event{{Type: rpc.EventError, Error: "not sent: Today's has gone."}}
	m, cmd = update(t, m, typeText("t"))
	if cmd == nil {
		t.Fatal("t sent nothing")
	}
	m, _ = update(t, m, cmd())
	if m.report == nil {
		t.Fatal("a refused send cleared the status")
	}
	if view := m.View(); !strings.Contains(view, "Not sent: Today's has gone.") {
		t.Errorf("box lacks the reason:\n%s", view)
	}
}
