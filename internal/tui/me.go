// This file holds what the chat screen shows about the user's profile: the
// /me box with the memories merud puts into every prompt, and the nudge
// that says Meru doesn't know the user yet. See ARCHITECTURE.md, "Memory",
// step 0.

package tui

import (
	"context"
	"errors"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// meNote closes the /me box: the two ways to add to the profile.
const meNote = "Say \"remember that …\" in a message, or run meru setup user in another terminal."

// profileNudge is the empty conversation's second line while merud says
// the user has no profile.
// The command comes early, so a wrap at 80 columns leaves it whole.
const profileNudge = "Meru doesn't know you yet. Run meru setup user in another terminal, " +
	"or tell it about yourself in a message."

// meBox is the open /me box. It opens at once with loading set, and the
// next memories reply fills in memories or err.
type meBox struct {
	loading  bool
	memories []rpc.MemoryInfo // only the profile kinds
	err      string           // why merud sent no memories
}

// meMsg reports what merud said to OpMemoryList. err says why no memories
// came, such as an older merud that doesn't know the op.
type meMsg struct {
	memories []rpc.MemoryInfo
	err      error
}

// meCmd returns a command that asks merud once for its memories and keeps
// the profile kinds, the ones every prompt carries.
func meCmd(ask askFunc) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		defer cancel()
		var mems []rpc.MemoryInfo
		for ev, err := range ask(ctx, rpc.Request{Op: rpc.OpMemoryList}, nil) {
			if err != nil {
				return meMsg{err: err}
			}
			switch ev.Type {
			case rpc.EventMemories:
				for _, mem := range ev.Memories {
					if slices.Contains(rpc.ProfileKinds(), mem.Kind) {
						mems = append(mems, mem)
					}
				}
			case rpc.EventDone:
				return meMsg{memories: mems}
			case rpc.EventError:
				return meMsg{err: errors.New(ev.Error)}
			}
		}
		return meMsg{err: errors.New("merud sent no reply")}
	}
}

// applyMe fills a waiting /me box with merud's reply. A reply that comes
// after the box closed, or after it already shows something, changes
// nothing.
func (m *Model) applyMe(msg meMsg) {
	b := m.meBox
	if b == nil || !b.loading {
		return
	}
	b.loading = false
	b.memories = msg.memories
	if msg.err != nil {
		b.err = msg.err.Error()
	}
}

// meBoxView draws the /me box for a pane width columns wide and height
// rows tall (see boxPane): each profile kind as a dim heading with its
// memories under it, one per line, wrapped to fit.
//
//	╭──────────────────────────────────────────────────────────────────────╮
//	│ About you                                                            │
//	│                                                                      │
//	│ me                                                                   │
//	│ Name: Amit Arora                                                     │
//	│ …                                                                    │
//	│                                                                      │
//	│ Say "remember that …" in a message, or run meru setup user in        │
//	│ another terminal.                                                    │
//	╰──────────────────────────────────────────────────────────────────────╯
func (m *Model) meBoxView(width, height int) string {
	b := m.meBox
	avail := boxRoom(width)
	wrap := func(s string) []string { return strings.Split(ansi.Wrap(s, avail, ""), "\n") }

	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = wrap("merud gave no memories: " + b.err)
	case len(b.memories) == 0:
		body = wrap("Meru doesn't know you yet.")
	default:
		for _, kind := range rpc.ProfileKinds() {
			var lines []string
			for _, mem := range b.memories {
				if mem.Kind == kind {
					// strings.Fields folds a memory written over several
					// lines onto one, before wrapping it to the box.
					lines = append(lines, wrap(strings.Join(strings.Fields(mem.Text), " "))...)
				}
			}
			if len(lines) == 0 {
				continue
			}
			if len(body) > 0 {
				body = append(body, "")
			}
			body = append(body, m.style.dim.Render(kind))
			body = append(body, lines...)
		}
	}
	return m.boxPane("About you", body, meNote, width, height)
}

// noProfile reports whether merud's last status said the user has no
// profile memories. An unknown count (-1) and no status at all say
// nothing, so they give false.
func (m *Model) noProfile() bool {
	return m.index != nil && m.index.Profile == 0
}
