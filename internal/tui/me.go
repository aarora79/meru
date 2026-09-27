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

// meNote closes the /me box: how to add to the profile, and to forget.
const meNote = "/me add <fact> or /me prefer <how you like answers> adds one."

// profileNudge is the empty conversation's second line while merud says
// the user has no profile.
// The commands come early, so a wrap at 80 columns leaves them whole.
const profileNudge = "Meru doesn't know you yet. Type /me add and a line about yourself, " +
	"or run meru setup user in another terminal."

// meBox is the open /me box. It opens at once with loading set, and the
// next memories reply fills in memories or err.
type meBox struct {
	loading  bool
	memories []rpc.MemoryInfo // only the profile kinds, kind by kind
	err      string           // why merud sent no memories
	// at is the marked memory, and confirm the ID a first d asked to
	// forget.
	at      int
	confirm string
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

// meCommand runs /me. Alone, it opens the box with the profile memories.
// "add <text>" saves text as a memory of kind "me", a fact about the user,
// and "prefer <text>" as one of kind "preferences", how they like things
// done, as the app's About you form and `meru setup user` do. Both kinds
// go into every prompt.
func (m Model) meCommand(arg string) (tea.Model, tea.Cmd) {
	verb, text, _ := strings.Cut(arg, " ")
	text = strings.TrimSpace(text)
	kind := ""
	switch verb {
	case "":
		m.openBox()
		m.meBox = &meBox{loading: true}
		return m, meCmd(m.ask)
	case "add":
		kind = "me"
	case "prefer":
		kind = "preferences"
	}
	if kind == "" || text == "" {
		m.notice = "/me takes nothing, add <a fact about you>, or prefer <how you like answers>"
		return m, nil
	}
	m.input.Reset()
	m.layout()
	m.notice = "saving…"
	req := rpc.Request{Op: rpc.OpMemoryAdd, Kind: kind, Text: text}
	return m, requestCmd(m.ask, tagMemoryAdd, req, rpc.EventMemories, changeTimeout)
}

// meKey handles a key in the /me box: ↑ and ↓ move the marker, and d
// forgets the marked memory after a second d (see forgetKey).
func (m *Model) meKey(msg tea.KeyMsg) tea.Cmd {
	b := m.meBox
	return m.forgetKey(msg, &b.at, &b.confirm, b.memories)
}

// applyMe fills a waiting /me box with merud's reply. A reply that comes
// after the box closed, or after it already shows something, changes
// nothing. The memories go in the order the box shows them, kind by
// kind, so the marker's index names the row it sits on.
func (m *Model) applyMe(msg meMsg) {
	b := m.meBox
	if b == nil || !b.loading {
		return
	}
	b.loading = false
	b.memories = nil
	for _, kind := range rpc.ProfileKinds() {
		for _, mem := range msg.memories {
			if mem.Kind == kind {
				b.memories = append(b.memories, mem)
			}
		}
	}
	b.at = min(b.at, max(len(b.memories)-1, 0))
	if msg.err != nil {
		b.err = msg.err.Error()
	}
}

// meBoxView draws the /me box for a pane width columns wide and height
// rows tall (see boxPaneAt): each profile kind as a dim heading with its
// memories under it, the marked one with "›", each wrapped to fit.
//
//	╭──────────────────────────────────────────────────────────────────────╮
//	│ About you                                                            │
//	│                                                                      │
//	│ me                                                                   │
//	│ › Name: Dana Reyes                                                   │
//	│   …                                                                  │
//	│                                                                      │
//	│ /me add <text> saves a fact about you, and /me prefer <text> how you │
//	│ like answers.                                                        │
//	╰──────────────────────────────────────────────────────────────────────╯
func (m *Model) meBoxView(width, height int) string {
	b := m.meBox
	avail := boxRoom(width)
	wrap := func(s string) []string { return strings.Split(ansi.Wrap(s, avail, ""), "\n") }

	var body []string
	focus := -1
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = wrap("merud gave no memories: " + b.err)
	case len(b.memories) == 0:
		body = wrap("Meru doesn't know you yet.")
	}
	kind := ""
	for i, mem := range b.memories {
		if mem.Kind != kind {
			if kind != "" {
				body = append(body, "")
			}
			kind = mem.Kind
			body = append(body, m.style.dim.Render(kind))
		}
		if i == b.at {
			focus = len(body)
		}
		// strings.Fields folds a memory written over several lines onto
		// one, before wrapping it to the box, two columns in for the
		// marker.
		for j, l := range strings.Split(ansi.Wrap(strings.Join(strings.Fields(mem.Text), " "), max(avail-2, 1), ""), "\n") {
			if j == 0 {
				body = append(body, m.markRow(i == b.at, l))
				continue
			}
			body = append(body, "  "+l)
		}
	}
	note := meNote
	if b.confirm != "" {
		note = "Press d again to forget the marked memory; any other key keeps it."
	}
	return m.boxPaneAt("About you", body, focus, note, width, height)
}

// noProfile reports whether merud's last status said the user has no
// profile memories. An unknown count (-1) and no status at all say
// nothing, so they give false.
func (m *Model) noProfile() bool {
	return m.index != nil && m.index.Profile == 0
}
