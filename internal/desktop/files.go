// This file holds the Bridge methods that deal in files: saving a chat or
// an answer through merud ("Share as file" and "Save to a note"), showing
// a saved file in its folder, choosing a folder to index, and attaching a
// file to a question.

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// saveTask names a save in its approval cards' IDs and in the Update the
// page gets for them.
const saveTask = "save"

// SaveChat saves the whole chat session as a Markdown file in the output
// folder and returns the file's full path. SaveNote saves text, one
// answer, as a note. merud writes the file with the write_file tool,
// through dispatch, so it asks first as write_file does: the card reaches
// the page as an approval Update with Task "save", and Approve answers it.
// Both fail when the user says no, write_file is off, or merud refuses.
func (b *Bridge) SaveChat(ctx context.Context, session string) (string, error) {
	return b.save(ctx, rpc.Request{Op: rpc.OpSaveFile, Kind: rpc.SaveChat, Session: session})
}

// SaveNote is SaveChat for one answer; see SaveChat.
func (b *Bridge) SaveNote(ctx context.Context, session, text string) (string, error) {
	return b.save(ctx, rpc.Request{Op: rpc.OpSaveFile, Kind: rpc.SaveNote, Session: session, Text: text})
}

// save sends req, showing any approval merud asks for, and returns the
// path the "saved" event names. It waits as long as the user takes to
// answer: ctx, from the page's call, ends when the page gives up.
func (b *Bridge) save(ctx context.Context, req rpc.Request) (string, error) {
	if req.Session == "" {
		return "", errors.New("ask something first; there is nothing to save yet")
	}
	req.Source = rpc.SourceDesktop
	b.mu.Lock()
	b.saves++
	n := b.saves
	b.mu.Unlock()
	// The save's own context, so the card closes when the call ends.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	approve := b.approver(n, saveTask, func() bool { return ctx.Err() == nil })
	path := ""
	for ev, err := range rpc.Do(ctx, b.socket, req, approve) {
		if err != nil {
			return "", err
		}
		switch ev.Type {
		case rpc.EventSaved:
			path = ev.Text
		case rpc.EventError:
			return "", errors.New(ev.Error)
		}
	}
	if path == "" {
		return "", errors.New("merud saved nothing")
	}
	return path, nil
}

// Reveal opens the folder that holds path, a file merud saved, in the
// system's file manager. It fails when path isn't inside the output
// folder: the page only reveals what Meru wrote.
func (b *Bridge) Reveal(path string) error {
	if b.outputDir == "" || !inside(b.outputDir, path) {
		return fmt.Errorf("%s isn't in Meru's output folder", path)
	}
	return b.OpenURL(rpc.FileURL(filepath.Dir(path), b.home))
}

// ChooseFolder shows the system's folder dialog and returns the folder
// picked, or "" when the user cancels.
func (b *Bridge) ChooseFolder() (string, error) {
	if b.pickFolder == nil {
		return "", errors.New("this build can't show a folder dialog")
	}
	return b.pickFolder()
}

// Attachment is a file the user picked to go with a question. Meru reads
// it with read_file, so it must sit where read_file may read: in an
// [index] folder or in the output folder. Path is written as read_file
// takes it, "~/Notes/lisbon.md" under the home folder. Readable says
// whether it can go, and Note, when it can't, says why and what to do.
type Attachment struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Readable bool   `json:"readable"`
	Note     string `json:"note,omitempty"`
}

// AttachFile shows the system's file dialog and returns the file picked,
// checked against the folders read_file may read, which it asks merud
// for. It returns an empty Attachment when the user cancels.
func (b *Bridge) AttachFile(ctx context.Context) (Attachment, error) {
	if b.pickFile == nil {
		return Attachment{}, errors.New("this build can't show a file dialog")
	}
	path, err := b.pickFile()
	if err != nil || path == "" {
		return Attachment{}, err
	}
	var folders []string
	if ev, err := b.one(ctx, rpc.Request{Op: rpc.OpIndexStatus}, rpc.EventStatus); err == nil && ev.Status != nil {
		folders = ev.Status.Folders
	}
	return b.attachment(path, folders), nil
}

// attachment checks path against folders, the [index] folders as config
// writes them, and the output folder.
func (b *Bridge) attachment(path string, folders []string) Attachment {
	a := Attachment{Path: rpc.ShortPath(b.home, path), Name: filepath.Base(path)}
	dirs := []string{b.outputDir}
	for _, f := range folders {
		dirs = append(dirs, b.expand(f))
	}
	for _, d := range dirs {
		if d != "" && inside(d, path) {
			a.Readable = true
			return a
		}
	}
	a.Note = "Meru reads files only in the folders it indexes and in its output folder. " +
		"Add the folder that holds " + a.Name + " in Library, under Folders, or move the file into one."
	return a
}

// expand turns a folder as config writes it, "~/Notes", into a full path.
func (b *Bridge) expand(folder string) string {
	if b.home != "" && (folder == "~" || strings.HasPrefix(folder, "~/")) {
		return filepath.Join(b.home, strings.TrimPrefix(folder[1:], "/"))
	}
	return folder
}

// inside reports whether path sits in dir, with symlinks in both resolved
// where they exist, so a link can't pass for a file inside.
func inside(dir, path string) bool {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	} else if _, err := os.Lstat(path); err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
