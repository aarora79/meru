// This file holds the /jobs box, the desktop app's Settings, Jobs: the
// scheduled jobs, each with its schedule in words, its next run and how
// its last run went. Enter runs the marked job now, c stops its run, and o
// opens its last run's chat. merud runs and stops the jobs (rpc.OpJobRun,
// OpJobCancel); this file only asks and lays out the answers. See
// ARCHITECTURE.md, "Scheduler".

package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// jobsNote closes the /jobs box.
const jobsNote = "Each run is a chat in the Jobs folder. Add jobs as [[jobs]] in config.toml."

// jobsBox is the open /jobs box. It opens at once with loading set, and
// merud's reply fills in jobs, or err. at is the marked row, and busy says
// what merud is doing for the box.
type jobsBox struct {
	loading bool
	err     string
	jobs    []rpc.JobInfo
	at      int
	busy    string
}

// jobsCommand runs /jobs: it opens the box and asks merud for the jobs.
func (m Model) jobsCommand() (tea.Model, tea.Cmd) {
	m.openBox()
	m.jobsBox = &jobsBox{loading: true}
	return m, requestCmd(m.ask, tagJobs, rpc.Request{Op: rpc.OpJobs}, rpc.EventJobs, readTimeout)
}

// jobsKey handles a key in the /jobs box: ↑ and ↓ move the marker, Enter
// runs the marked job now, c stops its run, and o opens its last run's
// chat, as /chats would.
func (m *Model) jobsKey(msg tea.KeyMsg) tea.Cmd {
	b := m.jobsBox
	b.at = moveMark(msg, b.at, len(b.jobs))
	if b.busy != "" || b.loading || b.at >= len(b.jobs) {
		return nil
	}
	j := b.jobs[b.at]
	switch {
	case msg.Type == tea.KeyEnter:
		if j.Running {
			m.notice = j.Name + " is running; c stops it"
			return nil
		}
		b.busy = "starting " + j.Name + "…"
		return requestCmd(m.ask, tagJobs, rpc.Request{Op: rpc.OpJobRun, ID: j.Name}, rpc.EventJobs, changeTimeout)
	case isKey(msg, "c"):
		if !j.Running {
			m.notice = j.Name + " isn't running"
			return nil
		}
		b.busy = "stopping " + j.Name + "…"
		return requestCmd(m.ask, tagJobs, rpc.Request{Op: rpc.OpJobCancel, ID: j.Name}, rpc.EventJobs, changeTimeout)
	case isKey(msg, "o"):
		if j.LastSession == "" {
			m.notice = j.Name + " has no run to open since merud started"
			return nil
		}
		return requestCmd(m.ask, tagJobRunChat, rpc.Request{Op: rpc.OpSessionTurns, Session: j.LastSession}, rpc.EventTurns, readTimeout)
	}
	return nil
}

// applyJobs takes in merud's reply to the list, a run or a stop. A change
// that failed keeps the box as it was and says why on the notice line.
func (m *Model) applyJobs(msg replyMsg) {
	b := m.jobsBox
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
	b.jobs = msg.ev.Jobs
	b.at = min(b.at, max(len(b.jobs)-1, 0))
	if busy != "" {
		m.notice = busy[:len(busy)-len("…")] + ": done"
	}
}

// applyJobRunChat opens a job's run, as /chats opens a chat.
func (m *Model) applyJobRunChat(msg replyMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = "couldn't open that run: " + msg.err.Error()
		return nil
	}
	if m.streaming || m.jobsBox == nil {
		return nil
	}
	id := m.jobsBox.jobs[m.jobsBox.at].LastSession
	leave := m.leaveCmd()
	m.reopen(id, msg.ev.Turns)
	m.closeBox()
	return leave
}

// jobLine writes one job as the box shows it: its name, its schedule in
// words, and then its state: running, paused, or its next and last runs.
func jobLine(j rpc.JobInfo, now time.Time) (head, detail string) {
	head = j.Name + "  " + j.When
	switch {
	case j.Running:
		detail = "running since " + clockTime(j.Started, now)
	case j.Paused:
		detail = "paused"
	default:
		detail = "next " + clockTime(j.Next, now)
	}
	if j.LastOutcome != "" {
		detail += " · last " + clockTime(j.Last, now) + ", " + j.LastOutcome
	} else if j.Last != "" {
		detail += " · last " + clockTime(j.Last, now)
	}
	return head, detail
}

// clockTime writes an RFC 3339 time as a person reads it: "today 07:00",
// or "Mon 27 Oct 07:00" on another day; "" for none.
func clockTime(stamp string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	t = t.In(now.Location())
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return "today " + t.Format("15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}

// jobsBoxView draws the /jobs box for a pane width columns wide and height
// rows tall (see boxPaneAt).
func (m *Model) jobsBoxView(width, height int) string {
	b := m.jobsBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no jobs: "+b.err, boxRoom(width))
	case len(b.jobs) == 0:
		body = splitWrap("No jobs yet. Add one as [[jobs]] in config.toml, such as a morning brief at \"0 7 * * 1-5\".", boxRoom(width))
	}
	focus := -1
	now := time.Now()
	if !b.loading {
		for i, j := range b.jobs {
			if i == b.at {
				focus = len(body)
			}
			head, detail := jobLine(j, now)
			body = append(body, m.markRow(i == b.at, head), "    "+m.style.dim.Render(detail))
		}
	}
	note := jobsNote
	if b.busy != "" {
		note = b.busy
	}
	return m.boxPaneAt("Jobs", body, focus, note, width, height)
}
