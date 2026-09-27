// This file holds the /folders box, the desktop app's Folders: the
// [index] folders Meru searches, with how many files the index holds from
// each, and the usual folders not indexed yet. Enter adds a suggested
// folder, d removes an indexed one, and /folders add <path> adds any
// other. merud edits [index] folders and scans (rpc.OpFolderAdd and
// OpFolderRemove); this file only asks and lays out the answers.

package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// foldersNote closes the /folders box.
const foldersNote = "Meru searches only these folders, and skips hidden files, build folders and anything that looks like a secret."

// foldersBox is the open /folders box. It opens at once with loading set,
// and merud's reply fills in folders and suggested, or err. at is the
// marked row, counting the indexed folders first; busy says what merud is
// doing for the box, and confirm names the folder a first d asked to
// remove.
type foldersBox struct {
	loading   bool
	err       string
	folders   []rpc.FolderInfo
	suggested []rpc.FolderInfo
	at        int
	busy      string
	confirm   string
}

// foldersCommand runs /folders. Alone, it opens the box. "add <path>"
// opens it and asks merud to add the folder, which may start with "~"; a
// relative path starts from the folder `meru chat` runs in. merud refuses
// the whole disk, the home folder, Meru's own folder, and a folder inside
// or around one already indexed.
func (m Model) foldersCommand(arg string) (tea.Model, tea.Cmd) {
	verb, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	req := rpc.Request{Op: rpc.OpFolders}
	busy := ""
	switch verb {
	case "":
	case "add":
		path, err := fullPath(rest, m.home)
		if rest == "" || err != nil {
			m.notice = "/folders add takes a folder, such as /folders add ~/Notes"
			return m, nil
		}
		req = rpc.Request{Op: rpc.OpFolderAdd, Path: path}
		busy = "adding " + rpc.ShortPath(m.home, path) + "…"
	default:
		m.notice = "/folders takes nothing, or add <folder>"
		return m, nil
	}
	m.openBox()
	m.foldersBox = &foldersBox{loading: true, busy: busy}
	// Listing counts the files in each suggested folder, which may take a
	// few seconds, so every folder op gets the longer wait, as in the app.
	return m, requestCmd(m.ask, tagFolders, req, rpc.EventFolders, changeTimeout)
}

// foldersKey handles a key in the /folders box: ↑ and ↓ move the marker,
// Enter adds the marked suggested folder, and d removes the marked indexed
// one after a second d.
func (m *Model) foldersKey(msg tea.KeyMsg) tea.Cmd {
	b := m.foldersBox
	b.at = moveMark(msg, b.at, len(b.folders)+len(b.suggested))
	confirm := b.confirm
	b.confirm = ""
	if b.busy != "" || b.loading {
		return nil
	}
	switch {
	case msg.Type == tea.KeyEnter && b.at >= len(b.folders) && len(b.suggested) > 0:
		f := b.suggested[b.at-len(b.folders)]
		b.busy = "adding " + f.Path + "…"
		return requestCmd(m.ask, tagFolders, rpc.Request{Op: rpc.OpFolderAdd, Path: f.Path}, rpc.EventFolders, changeTimeout)
	case isKey(msg, "d") && b.at < len(b.folders):
		f := b.folders[b.at]
		if confirm != f.Path {
			b.confirm = f.Path
			return nil
		}
		b.busy = "removing " + f.Path + "…"
		return requestCmd(m.ask, tagFolders, rpc.Request{Op: rpc.OpFolderRemove, Path: f.Path}, rpc.EventFolders, changeTimeout)
	}
	return nil
}

// applyFolders takes in merud's reply to the list or to a change. A change
// that failed keeps the box as it was and says why on the notice line.
func (m *Model) applyFolders(msg replyMsg) {
	b := m.foldersBox
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
	b.folders, b.suggested = msg.ev.Folders, msg.ev.Suggested
	b.at = min(b.at, max(len(b.folders)+len(b.suggested)-1, 0))
	if busy != "" {
		m.notice = "saved to config.toml; merud scans the folders now"
	}
}

// fileCount writes a folder's file count, such as "812 files", or "1000+
// files" when the count stopped at merud's cap.
func fileCount(f rpc.FolderInfo) string {
	s := fmt.Sprintf("%d %s", f.Files, plural(f.Files, "file", "files"))
	if f.More {
		s = fmt.Sprintf("%d+ files", f.Files)
	}
	if !f.Exists {
		s += " · not on disk"
	}
	return s
}

// foldersBoxView draws the /folders box for a pane width columns wide and
// height rows tall (see boxPaneAt).
func (m *Model) foldersBoxView(width, height int) string {
	b := m.foldersBox
	var body []string
	switch {
	case b.loading:
		body = []string{m.style.dim.Render("asking merud…")}
	case b.err != "":
		body = splitWrap("merud gave no folders: "+b.err, boxRoom(width))
	case len(b.folders) == 0:
		body = []string{"No folders yet. Enter adds a suggested one; /folders add <folder> adds any other."}
	}
	focus := -1
	row := func(i int, f rpc.FolderInfo) {
		if i == b.at {
			focus = len(body)
		}
		body = append(body, m.markRow(i == b.at, f.Path+"  "+m.style.dim.Render(fileCount(f))))
	}
	for i, f := range b.folders {
		row(i, f)
	}
	if len(b.suggested) > 0 && !b.loading {
		body = append(body, "", m.style.dim.Render("Not indexed yet"))
		for i, f := range b.suggested {
			row(len(b.folders)+i, f)
		}
	}
	note := foldersNote
	switch {
	case b.busy != "":
		note = b.busy
	case b.confirm != "":
		note = "Press d again to stop searching " + b.confirm + "; its files leave the index. Any other key keeps it."
	}
	return m.boxPaneAt("Folders", body, focus, note, width, height)
}
