// This file holds the /sites box, the Web search card's list in the
// desktop app's Settings: the websites web_fetch reads without asking in
// every chat, [web] trusted_sites. A site joins the list when the user
// picks "always" at web_fetch's prompt. d removes the marked site after a
// second d, and /sites remove <host> removes one by name. merud edits the
// list (rpc.OpTrustedSiteRemove); this file only asks and lays out the
// answers.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// sitesNote closes the /sites box.
const sitesNote = "Meru reads these websites without asking. It still asks before any download."

// sitesBox is the open /sites box. It opens at once with loading set, and
// merud's reply fills in sites, or err. at is the marked row, busy says
// what merud is doing for the box, and confirm names the site a first d
// asked to remove.
type sitesBox struct {
	loading bool
	err     string
	sites   []string
	at      int
	busy    string
	confirm string
}

// sitesCommand runs /sites. Alone, it opens the box. "remove <host>"
// opens it and asks merud to take that host off the list.
func (m Model) sitesCommand(arg string) (tea.Model, tea.Cmd) {
	verb, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	req := rpc.Request{Op: rpc.OpTrustedSites}
	busy := ""
	switch verb {
	case "":
	case "remove":
		if rest == "" {
			m.notice = "/sites remove takes a website, such as /sites remove en.wikipedia.org"
			return m, nil
		}
		req = rpc.Request{Op: rpc.OpTrustedSiteRemove, ID: rest}
		busy = "removing " + rest + "…"
	default:
		m.notice = "/sites takes nothing, or remove <website>"
		return m, nil
	}
	m.openBox()
	m.sitesBox = &sitesBox{loading: true, busy: busy}
	return m, requestCmd(m.ask, tagSites, req, rpc.EventTrustedSites, changeTimeout)
}

// sitesKey handles a key in the /sites box: ↑ and ↓ move the marker, and
// d removes the marked site after a second d.
func (m *Model) sitesKey(msg tea.KeyMsg) tea.Cmd {
	b := m.sitesBox
	b.at = moveMark(msg, b.at, len(b.sites))
	confirm := b.confirm
	b.confirm = ""
	if b.busy != "" || b.loading || !isKey(msg, "d") || b.at >= len(b.sites) {
		return nil
	}
	site := b.sites[b.at]
	if confirm != site {
		b.confirm = site
		return nil
	}
	b.busy = "removing " + site + "…"
	return requestCmd(m.ask, tagSites, rpc.Request{Op: rpc.OpTrustedSiteRemove, ID: site}, rpc.EventTrustedSites, changeTimeout)
}

// applySites takes in merud's reply to the list or to a removal. A
// removal that failed keeps the box as it was and says why on the notice
// line.
func (m *Model) applySites(msg replyMsg) {
	b := m.sitesBox
	if b == nil {
		return
	}
	busy := b.busy
	b.busy = ""
	if msg.err != nil {
		if b.loading {
			b.err = msg.err.Error()
		} else {
			m.notice = "no change: " + msg.err.Error()
		}
		b.loading = false
		return
	}
	b.loading = false
	b.sites = msg.ev.Sites
	b.at = min(b.at, max(len(b.sites)-1, 0))
	if busy != "" {
		m.notice = "saved to config.toml; Meru asks again before it reads that website"
	}
}

// sitesBoxView draws the /sites box for a pane width columns wide and
// height rows tall (see boxPaneAt).
func (m *Model) sitesBoxView(width, height int) string {
	b := m.sitesBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no websites: "+b.err, boxRoom(width))
	case len(b.sites) == 0:
		body = splitWrap("None. When Meru asks before it reads a website, press a to always allow it.", boxRoom(width))
	}
	focus := -1
	if !b.loading {
		for i, s := range b.sites {
			if i == b.at {
				focus = len(body)
			}
			body = append(body, m.markRow(i == b.at, s))
		}
	}
	note := sitesNote
	switch {
	case b.busy != "":
		note = b.busy
	case b.confirm != "":
		note = "Press d again to take " + b.confirm + " off the list. Any other key keeps it."
	}
	return m.boxPaneAt("Websites", body, focus, note, width, height)
}
