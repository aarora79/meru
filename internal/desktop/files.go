// This file holds the Bridge methods that deal in files: saving a chat or
// an answer through merud ("Share as file" and "Save to a note"), showing
// a saved file in its folder, choosing a folder to index, and attaching
// files to a question: merud copies each one where read_file may read it.

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// maxAttachments caps the files one question carries. Each file adds a
// read_file call, and a small model loses track of more than a few; five
// also keeps a slip, such as dropping a whole folder's files, from filling
// the uploads folder with copies.
const maxAttachments = 5

// Attachment is a file the user attached to the next question, as the
// composer's chip shows it. merud copied it into its uploads folder, where
// read_file may read it. Path is the copy, written as read_file takes it,
// "~/meru-output/uploads/garden-plan.pdf"; Name is the name of the user's
// own file, and Size its size as the chip shows it, such as "2.4 MB".
type Attachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size string `json:"size"`
}

// AttachFile shows the system's file dialog, which starts wherever the
// system chooses and lets the user pick one or more files anywhere, and
// attaches each file picked, as attach says. The result comes to the page
// as an Update of kind KindAttachments. AttachFile fails only when this
// build can't show the dialog, or the dialog fails; a cancel does nothing.
func (b *Bridge) AttachFile(ctx context.Context) error {
	if b.pickFiles == nil {
		return errors.New("this build can't show a file dialog")
	}
	paths, err := b.pickFiles()
	if err != nil || len(paths) == 0 {
		return err
	}
	b.attach(ctx, paths)
	return nil
}

// Drop attaches the files the user dropped on b's window, as attach says.
// The window calls it from its file-drop event, and it returns at once:
// the copies go on in a goroutine that b's ServiceShutdown waits for.
//
// Drop is a plain function, not a method, on purpose. The window binds
// every exported method of the Bridge, so the page could call one with
// any path it liked; only a real drop, which Wails reports from Go,
// reaches this function.
func Drop(b *Bridge, paths []string) {
	// wg.Go starts the function in a new goroutine and counts it.
	b.wg.Go(func() { b.attach(context.Background(), paths) })
}

// Detach takes attachment i, counting from 0, off the next question. The
// copy stays in the uploads folder; only merud writes there. It fails when
// no attachment sits at i.
func (b *Bridge) Detach(i int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < 0 || i >= len(b.attached) {
		return fmt.Errorf("no attachment %d", i+1)
	}
	b.attached = slices.Delete(b.attached, i, i+1)
	b.emitAttachments("")
	return nil
}

// DetachAll takes every attachment off the next question, for New chat.
func (b *Bridge) DetachAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attached = nil
	b.emitAttachments("")
}

// attach asks merud to copy each file in paths, which the user picked or
// dropped, into its uploads folder, and adds each copy to the next
// question, up to maxAttachments. Picking or dropping a file is the user's
// consent to share that one file with Meru, and merud still refuses a
// folder, a link, a file over 50 MiB, a file whose name looks like a
// secret's, and one read_file couldn't read. Then it sends the page the
// attachments with a notice that names each file that didn't go, and why.
func (b *Bridge) attach(ctx context.Context, paths []string) {
	b.mu.Lock()
	room := maxAttachments - len(b.attached)
	b.mu.Unlock()
	var added []Attachment
	var problems []string
	for i, p := range paths {
		if i >= room {
			problems = append(problems, fmt.Sprintf("A question takes %d files at most, so %s stayed out.",
				maxAttachments, plural(len(paths)-i, "file")))
			break
		}
		ev, err := b.one(ctx, rpc.Request{Op: rpc.OpAttachFile, Path: p}, rpc.EventSaved)
		if err != nil {
			problems = append(problems, "Not attached: "+err.Error()+".")
			continue
		}
		added = append(added, b.attachment(p, ev.Text))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Another pick or drop may have filled the question meanwhile.
	for _, a := range added {
		if len(b.attached) >= maxAttachments {
			problems = append(problems, fmt.Sprintf("A question takes %d files at most, so %s stayed out.", maxAttachments, a.Name))
			continue
		}
		b.attached = append(b.attached, a)
	}
	b.emitAttachments(strings.Join(problems, " "))
}

// attachment describes the copy merud saved at saved of the user's file
// at src. It reads the copy's size; "" when it can't.
func (b *Bridge) attachment(src, saved string) Attachment {
	a := Attachment{Path: rpc.ShortPath(b.home, saved), Name: filepath.Base(src)}
	if info, err := os.Stat(saved); err == nil {
		a.Size = sizeText(info.Size())
	}
	return a
}

// emitAttachments sends the page the attachments as they stand, with
// notice. The caller holds b.mu.
func (b *Bridge) emitAttachments(notice string) {
	b.emit(UpdateEvent, Update{Kind: KindAttachments, Attachments: slices.Clone(b.attached), Notice: notice})
}

// withAttachments returns q with one "Read this file: <path>" line per
// attachment, the line the model follows with a read_file call. The
// caller holds b.mu.
func (b *Bridge) withAttachments(q string) string {
	if len(b.attached) == 0 {
		return q
	}
	var s strings.Builder
	s.WriteString(q + "\n")
	for _, a := range b.attached {
		s.WriteString("\nRead this file: " + a.Path)
	}
	return s.String()
}

// sizeText writes n bytes as the chip shows it: "812 bytes", "4.2 KB" or
// "2.4 MB".
func sizeText(n int64) string {
	switch {
	case n < 1<<10:
		return plural(int(n), "byte")
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// plural writes n and a noun, adding "s" to the noun unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
