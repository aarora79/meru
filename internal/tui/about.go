// This file holds the /about box: the one line that says what Meru is, its
// version and license, where Meru keeps its files, and the project's links,
// the same things the desktop app's About section shows. internal/about
// holds them for both. The box asks merud for nothing, so it opens while
// merud is down.

package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/about"
	"github.com/aarora79/meru/internal/rpc"
)

// aboutNote closes the /about box.
const aboutNote = "The models run on this computer, and Meru has no account to sign in to."

// aboutBoxView draws the /about box for a pane width columns wide and
// height rows tall (see boxPaneAt). Each link shows its label and its URL,
// side by side where they fit, and with links on the URL is an OSC 8 link.
// ↑ and ↓ scroll a box too tall for the pane.
func (m *Model) aboutBoxView(width, height int) string {
	room := boxRoom(width)
	wrap := func(s string) []string { return splitWrap(s, room) }
	body := wrap(about.Tagline)
	body = append(body, "")
	v := m.fullVersion
	if v == "unknown" {
		v = "unknown: this build carries no version"
	}
	body = append(body, "version  "+v, "license  "+about.License)
	if m.info.Dir != "" {
		dir := rpc.ShortPath(m.home, m.info.Dir)
		body = append(body, "config   "+filepath.Join(dir, "config.toml"), "folder   "+dir)
	}
	body = append(body, "")
	var labels, urls []string
	for _, l := range about.Links() {
		labels = append(labels, l.Label)
		urls = append(urls, l.URL)
	}
	links := twoColumns(labels, urls, room)
	if m.look.links {
		// Each URL, whole or cut to fit, becomes a link to the full URL,
		// which terminals that support OSC 8 open on a click.
		for i, l := range links {
			for _, u := range urls {
				if at := strings.Index(l, u); at >= 0 {
					links[i] = l[:at] + rpc.Hyperlink(u, u) + l[at+len(u):]
				}
			}
		}
	}
	body = append(body, links...)
	b := m.aboutBox
	b.lines = len(body)
	return m.boxPaneAt("About Meru", body, b.at, aboutNote, width, height)
}

// splitWrap wraps s to width columns and returns its lines.
func splitWrap(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, width, ""), "\n")
}
