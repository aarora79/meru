// This file holds Upload, which copies a file the user attached in the
// desktop app into <[skills] output_dir>/uploads, where read_file may read
// it. It is no tool: the model can't call it. merud calls it for the
// attach_file op, and only the user's own pick in a file dialog or drop on
// the window leads there. See ARCHITECTURE.md, "Desktop app".

package builtin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aarora79/meru/internal/index"
)

// Limits on an upload.
const (
	// uploadsFolder is the folder under [skills] output_dir that holds
	// the files the user attached. It sits apart from attachments, where
	// the google server saves mail attachments, because that server
	// deletes every file in its folder an hour after it was written, and
	// a chat that names an upload may go on for longer.
	uploadsFolder = "uploads"
	// uploadCap refuses a file over 50 MiB, the same cap web_fetch puts
	// on a download: large enough for most PDFs and documents a person
	// asks about, small enough that a slip can't fill the disk with
	// copies.
	uploadCap = downloadCap
)

// Upload copies the file at src, an absolute path, into the uploads
// folder and returns the copy's full path. A name already in use there
// gets "-2", "-3" and so on, so Upload never replaces a file.
//
// The user picked src, so it may sit anywhere. The copy must then pass
// every rule read_file applies to the output folder, so Upload never
// widens what the file tools read: after it the model can read the copy,
// and nothing else.
//
// It fails, with a message the user reads, when read_file is off or has no
// output folder to read, when src is missing, a folder, a symbolic link or
// no regular file, when its name looks like a secret's, when it is over
// uploadCap, or when read_file couldn't read the copy: a hidden, media,
// binary or oversized file, or one of a type Meru doesn't read. A failed
// upload leaves no copy behind.
func (t *Tools) Upload(src string) (string, error) {
	if t.outputDir == "" {
		return "", errors.New("there is no output folder to copy the file into; set [skills] output_dir in config.toml")
	}
	if !t.hasFiles() {
		return "", errors.New("attaching needs read_file, which stays off until you add a folder in the Library, under Folders")
	}
	if !t.enabled(ReadFile) {
		return "", errors.New("attaching needs read_file, which is off; turn it on in the Library, under Connections")
	}
	name := filepath.Base(src)
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("%s isn't a full path", src)
	}
	// Lstat describes src itself; for a symbolic link it describes the
	// link, not what the link points to.
	info, err := os.Lstat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s isn't there any more", name)
	case err != nil:
		return "", fmt.Errorf("can't read %s: %w", name, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s is a shortcut (a symbolic link); attach the file it points to", name)
	case info.IsDir():
		return "", fmt.Errorf("%s is a folder; attach the files in it, or add it in the Library under Folders", name)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("%s isn't a regular file", name)
	case index.IsSecret(name):
		// The indexer never reads such a file, and read_file would refuse
		// the copy, so say why now, before any copy exists.
		return "", fmt.Errorf("%s looks like it holds keys, passwords or tokens, so Meru won't read it", name)
	case info.Size() > uploadCap:
		return "", fmt.Errorf("%s is %s, and Meru takes files up to %s", name, size(info.Size()), size(uploadCap))
	}

	f, err := os.Open(src) // #nosec G304 -- a file the user picked or dropped in the desktop app
	if err != nil {
		return "", fmt.Errorf("can't read %s: %w", name, err)
	}
	// defer runs f.Close() when Upload returns, on every path.
	defer f.Close()
	// The path could have changed between Lstat and Open, say to a link.
	// SameFile compares the file Open reached with the one Lstat saw.
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("%s changed while Meru read it; try again", name)
	}

	root, err := openFolder(t.outputDir, uploadsFolder)
	if err != nil {
		return "", err
	}
	defer root.Close()
	saved, _, err := saveNew(root, safeName(name, "upload"), f, uploadCap)
	if err != nil {
		return "", fmt.Errorf("can't copy %s: %w", name, err)
	}
	dst := filepath.Join(t.outputDir, uploadsFolder, saved)

	// Check is the test read_file runs on each path, so a copy that
	// passes it is one the model can read.
	c, err := t.files.Check(dst)
	if err == nil && c.Reason == "" {
		return dst, nil
	}
	_ = root.Remove(saved)
	if err != nil {
		return "", fmt.Errorf("can't check %s: %w", name, err)
	}
	return "", fmt.Errorf("can't read %s: %s", name, why(c.Reason))
}
