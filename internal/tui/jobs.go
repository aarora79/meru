// This file holds the /jobs box, the desktop app's Settings, Jobs: the
// scheduled jobs, each with its schedule in words, its next run and how
// its last run went. Enter runs the marked job now, c stops its run, o
// opens its last run's chat, p pauses or resumes it, d twice removes it,
// and a and e walk through a new job's or the marked job's fields. merud
// runs the jobs and writes config.toml (rpc.OpJobRun, OpJobCancel,
// OpJobSet, OpJobRemove); this file only asks and lays out the answers.
// See ARCHITECTURE.md, "Scheduler".

package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// jobsNote closes the /jobs box.
const jobsNote = "Each run is a chat in the Jobs folder. a adds a job; merud saves it in config.toml."

// jobsBox is the open /jobs box. It opens at once with loading set, and
// merud's reply fills in jobs, or err. at is the marked row, and busy says
// what merud is doing for the box. armed names the job a first d marked
// for removing, and walk is the form a or e opened.
type jobsBox struct {
	loading bool
	err     string
	jobs    []rpc.JobInfo
	at      int
	busy    string
	armed   string
	walk    *jobWalk
}

// jobStep is one field the job form asks for: its label, a line of help,
// and where its answer goes in a JobSpec.
type jobStep struct {
	label, help string
	field       func(*rpc.JobSpec) *string
}

// jobSteps are the fields the form asks for, in order. A job that runs a
// command skips the prompt and the skill; config.toml holds its command.
var jobSteps = []jobStep{
	{"Name", "Lower-case letters, digits, - and _, such as morning-brief.", func(s *rpc.JobSpec) *string { return &s.Name }},
	{"When", "Five cron fields: minute hour day-of-month month day-of-week. \"0 7 * * 1-5\" is 7:00 on weekdays.",
		func(s *rpc.JobSpec) *string { return &s.Schedule }},
	{"What Meru does", "The question each run asks, such as: brief me on today's calendar and unread mail.",
		func(s *rpc.JobSpec) *string { return &s.Prompt }},
	{"Skill (optional)", "A skill to load for each run, such as web-research. Empty lets Meru pick.",
		func(s *rpc.JobSpec) *string { return &s.Skill }},
}

// jobWalk is the form a or e opens in the /jobs box: one field at a time,
// with the job's value as the placeholder, which an empty Enter keeps. was
// is the name of the job it edits, "" for a new one.
type jobWalk struct {
	was   string
	spec  rpc.JobSpec
	steps []jobStep
	at    int
	input textinput.Model
}

// newJobWalk opens the form on spec, the job named was or a new one, and
// returns it with the command that makes its cursor blink.
func newJobWalk(was string, spec rpc.JobSpec) (*jobWalk, tea.Cmd) {
	w := &jobWalk{was: was, spec: spec, steps: jobSteps}
	if spec.Command != "" {
		w.steps = jobSteps[:2]
	}
	return w, w.open()
}

// open readies the input for the field at w.at.
func (w *jobWalk) open() tea.Cmd {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 0 // merud checks the length
	in.Placeholder = *w.steps[w.at].field(&w.spec)
	w.input = in
	return w.input.Focus()
}

// take keeps what the user typed for the field at w.at, or the old value
// for an empty answer, and reports whether it was the last field. A skill
// takes "-" to clear it, since an empty answer keeps the old one.
func (w *jobWalk) take() bool {
	v := strings.TrimSpace(w.input.Value())
	f := w.steps[w.at].field(&w.spec)
	switch {
	case v == "-" && w.at == 3:
		*f = ""
	case v != "":
		*f = v
	}
	w.at++
	return w.at == len(w.steps)
}

// view returns the form's lines for the box body, and the note under it.
func (w *jobWalk) view() ([]string, string) {
	title := "New job"
	if w.was != "" {
		title = "Edit " + w.was
	}
	st := w.steps[w.at]
	lines := []string{title + " · " + st.label, st.help, w.input.View()}
	note := "Enter keeps this and goes on; an empty Enter keeps what's shown. Esc stops."
	if w.at == 3 {
		note = "Enter saves the job; - clears the skill. Esc stops."
	}
	return lines, note
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
	if b.walk != nil {
		return m.jobWalkKey(msg)
	}
	b.at = moveMark(msg, b.at, len(b.jobs))
	if b.busy != "" || b.loading {
		return nil
	}
	if isKey(msg, "a") {
		var cmd tea.Cmd
		b.walk, cmd = newJobWalk("", rpc.JobSpec{})
		return cmd
	}
	if b.at >= len(b.jobs) {
		return nil
	}
	j := b.jobs[b.at]
	if isKey(msg, "d") {
		if b.armed != j.Name {
			b.armed = j.Name
			m.notice = "d again removes " + j.Name + "; its past runs stay in the Jobs folder"
			return nil
		}
		b.armed, b.busy = "", "removing "+j.Name+"…"
		return requestCmd(m.ask, tagJobs, rpc.Request{Op: rpc.OpJobRemove, ID: j.Name}, rpc.EventJobs, changeTimeout)
	}
	b.armed = "" // any other key disarms a d
	switch {
	case isKey(msg, "e"):
		var cmd tea.Cmd
		b.walk, cmd = newJobWalk(j.Name, j.JobSpec)
		return cmd
	case isKey(msg, "p"):
		spec := j.JobSpec
		spec.Paused = !spec.Paused
		b.busy = "pausing " + j.Name + "…"
		if !spec.Paused {
			b.busy = "resuming " + j.Name + "…"
		}
		return requestCmd(m.ask, tagJobs, rpc.Request{Op: rpc.OpJobSet, ID: j.Name, JobSpec: &spec}, rpc.EventJobs, changeTimeout)
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

// jobWalkKey handles a key while the job form is open: Enter keeps the
// field and moves on, and after the last sends the job to merud; every
// other key types into the field. The box's Esc closes the form first.
func (m *Model) jobWalkKey(msg tea.KeyMsg) tea.Cmd {
	b := m.jobsBox
	w := b.walk
	if msg.Type != tea.KeyEnter {
		var cmd tea.Cmd
		w.input, cmd = w.input.Update(msg)
		return cmd
	}
	if !w.take() {
		return w.open()
	}
	b.walk = nil
	spec := w.spec
	b.busy = "saving " + spec.Name + "…"
	return requestCmd(m.ask, tagJobs, rpc.Request{Op: rpc.OpJobSet, ID: w.was, JobSpec: &spec}, rpc.EventJobs, changeTimeout)
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
	case b.walk != nil:
		lines, note := b.walk.view()
		for _, l := range lines {
			body = append(body, splitWrap(l, boxRoom(width))...)
		}
		return m.boxPaneAt("Jobs", body, len(body)-1, note, width, height)
	case len(b.jobs) == 0:
		body = splitWrap("No jobs yet. Press a to add one, such as a morning brief at \"0 7 * * 1-5\".", boxRoom(width))
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
