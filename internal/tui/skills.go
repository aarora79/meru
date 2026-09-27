// This file holds the /skills box, the desktop app's Skills: every skill
// with whether it is on, and Enter to turn the marked one on or off. merud
// edits [skills] disabled and loads the skills again (rpc.OpSkillEnable and
// OpSkillDisable); this file only asks and lays out the answers.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// skillsNote closes the /skills box.
const skillsNote = "A skill that is on loads when a question needs it. meru skills show <name> prints one."

// skillsBox is the open /skills box. It opens at once with loading set,
// and merud's reply fills in skills, or err. warnings are the skill
// folders merud skipped, and why; at is the marked skill, and busy says
// what merud is doing for the box.
type skillsBox struct {
	loading  bool
	err      string
	skills   []rpc.SkillInfo
	warnings []string
	at       int
	busy     string
}

// skillsKey handles a key in the /skills box: ↑ and ↓ move the marker,
// and Enter or space turns the marked skill on or off.
func (m *Model) skillsKey(msg tea.KeyMsg) tea.Cmd {
	b := m.skillsBox
	b.at = moveMark(msg, b.at, len(b.skills))
	if (msg.Type != tea.KeyEnter && msg.Type != tea.KeySpace) || len(b.skills) == 0 || b.busy != "" {
		return nil
	}
	s := b.skills[b.at]
	op, word := rpc.OpSkillDisable, "off"
	if s.Disabled {
		op, word = rpc.OpSkillEnable, "on"
	}
	b.busy = "turning " + s.Name + " " + word + "…"
	return requestCmd(m.ask, tagSkills, rpc.Request{Op: op, ID: s.Name}, rpc.EventSkills, changeTimeout)
}

// applySkills takes in merud's reply to the list or to a change. A change
// that failed keeps the box as it was and says why on the notice line.
// The marker stays on the same skill, since merud lists the disabled ones
// last and a change can move it.
func (m *Model) applySkills(msg replyMsg) {
	b := m.skillsBox
	if b == nil {
		return
	}
	changed := b.busy != ""
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
	marked := ""
	if b.at < len(b.skills) {
		marked = b.skills[b.at].Name
	}
	b.loading = false
	b.skills = msg.ev.Skills
	b.warnings = nil
	if msg.ev.Text != "" {
		b.warnings = strings.Split(msg.ev.Text, "\n")
	}
	b.at = min(b.at, max(len(b.skills)-1, 0))
	for i, s := range b.skills {
		if s.Name == marked {
			b.at = i
		}
	}
	if changed {
		m.notice = "saved to config.toml"
	}
}

// skillsBoxView draws the /skills box for a pane width columns wide and
// height rows tall (see boxPaneAt): a row per skill with "on" or "off",
// whether it ships with Meru, and its description, then the folders merud
// skipped.
func (m *Model) skillsBoxView(width, height int) string {
	b := m.skillsBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no skills: "+b.err, boxRoom(width))
	case len(b.skills) == 0:
		body = []string{"No skills."}
	}
	for i, s := range b.skills {
		state := "on "
		if s.Disabled {
			state = "off"
		}
		text := state + "  " + s.Name
		var tags []string
		if s.Builtin {
			tags = append(tags, "built in")
		}
		if s.Edited {
			tags = append(tags, "edited")
		}
		if s.Description != "" {
			tags = append(tags, s.Description)
		}
		if len(tags) > 0 {
			text += "  " + m.style.dim.Render(strings.Join(tags, " · "))
		}
		body = append(body, m.markRow(i == b.at, text))
	}
	if len(b.warnings) > 0 {
		body = append(body, "", m.style.dim.Render("Skipped"))
		for _, w := range b.warnings {
			body = append(body, splitWrap(w, boxRoom(width))...)
		}
	}
	note := skillsNote
	if b.busy != "" {
		note = b.busy
	}
	return m.boxPaneAt("Skills", body, b.at, note, width, height)
}
