// This file holds the /log box, the desktop app's Activity: the latest
// tool calls from the tool_calls audit log, newest first, the data `meru
// log` prints (rpc.OpLog). The marked call shows its arguments and the
// start of its result under its row.

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// logLimit caps how many calls the /log box lists, as the app's Activity
// does. `meru log -n N` shows more.
const logLimit = 100

// logNote closes the /log box.
const logNote = "Every tool call, allowed or not, lands here. meru log -v prints each result in full."

// logBox is the open /log box. It opens at once with loading set, and
// merud's reply fills in rows or err. at is the marked call.
type logBox struct {
	loading bool
	err     string
	rows    []rpc.LogEntry
	at      int
}

// applyLog fills a waiting /log box with merud's reply.
func (m *Model) applyLog(msg replyMsg) {
	b := m.logBox
	if b == nil || !b.loading {
		return
	}
	b.loading = false
	if msg.err != nil {
		b.err = msg.err.Error()
		return
	}
	b.rows = msg.ev.Log
}

// logBoxView draws the /log box for a pane width columns wide and height
// rows tall (see boxPaneAt): one row per call with its time, in the zone
// merud wrote it in, its tool, its outcome, who approved it and how long it
// took. Under the marked row, dim, come its arguments and its result, each
// on one line cut to fit.
func (m *Model) logBoxView(width, height int) string {
	b := m.logBox
	room := boxRoom(width)
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no tool calls: "+b.err, room)
	case len(b.rows) == 0:
		body = []string{"No tool calls yet."}
	}
	focus := -1
	for i, e := range b.rows {
		when := e.Time
		if t, err := time.Parse(time.RFC3339, e.Time); err == nil {
			when = t.Format("01-02 15:04")
		}
		approval := e.Approval
		switch {
		case approval == "" && e.Caller != "":
			approval = "by " + e.Caller // merud made the call; no model chose it
		case approval == "":
			approval = "-" // nobody was asked
		}
		name := rpc.ToolName(e.Kind, e.Server, e.Tool)
		row := fmt.Sprintf("%s  %s  %s  %s  %s", when, name, e.Outcome, approval, millis(e.DurationMillis))
		if i == b.at {
			focus = len(body)
		}
		body = append(body, m.markRow(i == b.at, row))
		if i == b.at {
			result := strings.Join(strings.Fields(e.Result), " ")
			if result == "" {
				result = "(no result)"
			}
			body = append(body,
				m.style.dim.Render("    args: "+rpc.ArgsLine(e.Args, max(room-10, 10))),
				m.style.dim.Render("    result: "+rpc.Cut(result, max(room-12, 10))))
		}
	}
	return m.boxPaneAt("Tool calls", body, focus, logNote, width, height)
}
