// This file holds /attach, which attaches a file or an image to the next
// question, as the desktop app's attach button does. merud copies the file
// into its uploads folder (rpc.OpAttachFile); the chat keeps the copy's
// path, adds a "Read this file" line for a file, and sends an image's path
// in the request's images. See ARCHITECTURE.md, "Terminal UI" and
// "Attaching files".

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// maxAttachments caps the files and images one question carries, as in
// the desktop app: each file costs a read_file call, a small model loses
// track of more than a few, and merud takes five images at most.
const maxAttachments = 5

// attachment is one file or image the next question carries: the copy
// merud made in its uploads folder.
type attachment struct {
	name string // the copy's file name, as the attachments line shows it
	path string // the copy as read_file takes it, "~/meru-output/uploads/garden-plan.pdf"
	full string // the copy's full path, which a question's images name
	kind string // rpc.AttachImage or rpc.AttachFile
	size string // such as "2.4 MB"; "" when the copy can't be read
}

// attachCommand runs /attach. With a path it asks merud to copy that file
// into the uploads folder; the reply lands in applyAttach. Alone, it takes
// every attachment off the next question. The copies stay where merud put
// them, as they do when the app's chips come off.
//
// The path may start with "~" for the home folder, and a relative path
// starts from the folder `meru chat` runs in. Typing the path is the
// user's consent to share that one file, as a pick or a drop is in the
// app; merud still refuses a folder, a link, a secret's name and a file
// too big or unreadable.
func (m Model) attachCommand(arg string) (tea.Model, tea.Cmd) {
	if arg == "" {
		m.input.Reset()
		m.layout()
		switch n := len(m.attached); n {
		case 0:
			m.notice = "nothing attached · /attach <path> attaches a file or an image"
		default:
			m.attached = nil
			m.layout()
			m.notice = fmt.Sprintf("took %d %s off the next question", n, plural(n, "attachment", "attachments"))
		}
		return m, nil
	}
	if len(m.attached) >= maxAttachments {
		m.notice = fmt.Sprintf("a question takes %d files at most · /attach alone takes them off", maxAttachments)
		return m, nil
	}
	path, err := fullPath(arg, m.home)
	if err != nil {
		m.notice = "can't attach " + arg + ": " + err.Error()
		return m, nil
	}
	m.input.Reset()
	m.layout()
	m.notice = "attaching " + filepath.Base(path) + "…"
	// Copying a file of up to 50 MiB may take a moment, so the request
	// gets the longer wait.
	return m, requestCmd(m.ask, tagAttach, rpc.Request{Op: rpc.OpAttachFile, Path: path}, rpc.EventSaved, changeTimeout)
}

// fullPath turns a path the user typed into a full one: "~" or "~/x"
// becomes a path under home, and a relative path joins the working
// folder. It fails when the path needs the home folder and home is "", or
// the working folder can't be found.
func fullPath(p, home string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home == "" {
			return "", fmt.Errorf("the home folder is unknown")
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// applyAttach takes in merud's reply to /attach. A copy joins the next
// question, up to maxAttachments. A file, which the model reads with
// read_file, switches a scope that offers no read_file (mail, web or
// talk) to files, as the app does; an image goes with the question in
// every scope.
func (m *Model) applyAttach(msg replyMsg) {
	if msg.err != nil {
		m.notice = "not attached: " + msg.err.Error()
		return
	}
	if len(m.attached) >= maxAttachments {
		m.notice = fmt.Sprintf("a question takes %d files at most, so %s stayed out", maxAttachments, filepath.Base(msg.ev.Text))
		return
	}
	a := attachment{name: filepath.Base(msg.ev.Text), path: rpc.ShortPath(m.home, msg.ev.Text), full: msg.ev.Text, kind: msg.ev.Kind}
	if a.kind != rpc.AttachImage {
		a.kind = rpc.AttachFile // an older merud sends no kind, and copies only files
	}
	// os.Stat reads the copy's size; it fails when the copy has gone.
	if info, err := os.Stat(a.full); err == nil {
		a.size = rpc.ShortBytes(info.Size())
	}
	m.attached = append(m.attached, a)
	m.layout()
	m.notice = "attached " + a.name
	if a.kind == rpc.AttachFile && m.scope != "" && m.scope != rpc.ScopeFiles {
		m.scope = rpc.ScopeFiles
		m.notice += " · scope: my files, so Meru can read it"
	}
}

// takeAttachments turns text into the next question and takes the
// attachments off: one "Read this file: <path>" line per file, the line
// the model follows with a read_file call, and the images by full path.
// The question gets the scope set now.
func (m *Model) takeAttachments(text string) outgoing {
	q := outgoing{text: text, scope: m.scope}
	var lines strings.Builder
	for _, a := range m.attached {
		if a.kind == rpc.AttachImage {
			q.images = append(q.images, a.full)
			continue
		}
		lines.WriteString("\nRead this file: " + a.path)
	}
	if lines.Len() > 0 {
		q.text += "\n" + lines.String()
	}
	m.attached = nil
	return q
}

// attachLine draws the line above the input box that lists the
// attachments, such as "attached: garden-plan.pdf 2.4 MB · garden-bed.png
// image 812 KB", cut to the screen's width.
func (m *Model) attachLine() string {
	parts := make([]string, len(m.attached))
	for i, a := range m.attached {
		s := a.name
		if a.kind == rpc.AttachImage {
			s += " image"
		}
		if a.size != "" {
			s += " " + a.size
		}
		parts[i] = s
	}
	return m.style.dim.Render(ansi.Truncate("attached: "+strings.Join(parts, " · "), m.width, "…"))
}
