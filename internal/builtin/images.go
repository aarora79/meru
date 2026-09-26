// This file holds the rules for images the user attaches in the desktop
// app: which files count as images, how Upload copies one, and Image,
// which reads a copy back for the turn that sends it to the model. A
// image goes to the model with the question, not through read_file, so
// it skips read_file's test for readable text. See ARCHITECTURE.md,
// "Desktop app".

package builtin

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// imageCap refuses an image over 20 MiB. A phone photo runs from 2 to
// 12 MB, so 20 MiB leaves room, and the model sees each image at a few
// hundred pixels across whatever its size. The cap also bounds a turn:
// five images at the cap, sent as base64, make a request of about 133
// MB to Ollama.
const imageCap = 20 << 20

// sniffBytes is how much of a file net/http's DetectContentType reads to
// name its type.
const sniffBytes = 512

// imageExts lists the file name endings Meru treats as images, and
// imageTypes the types DetectContentType reports for them. Ollama's vision
// models read these four formats.
var (
	imageExts  = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}
	imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
)

// Uploaded is a file Upload copied: the copy's full path, and its kind,
// rpc.AttachImage for an image, which goes to the model with the
// question, or rpc.AttachFile for any other file, which the model reads
// with read_file.
type Uploaded struct {
	Path string
	Kind string
}

// IsImageName reports whether name ends in one of the image endings,
// such as ".png" or ".JPG". The name alone proves nothing: Upload and
// Image also read the file's first bytes (see isImageData).
func IsImageName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range imageExts {
		if ext == e {
			return true
		}
	}
	return false
}

// isImageData reports whether head, the first bytes of a file, starts
// like a PNG, JPEG, GIF or WebP file. DetectContentType reads the magic
// bytes each format opens with, so a text file renamed to .png fails.
func isImageData(head []byte) bool {
	t := http.DetectContentType(head)
	for _, it := range imageTypes {
		if t == it {
			return true
		}
	}
	return false
}

// uploadImage copies the image f, which Upload opened from the user's
// file named name, into root, the uploads folder, and returns the copy's
// name there. It reads the first bytes first and refuses a file whose
// content isn't an image, before any copy exists.
func uploadImage(root *os.Root, f *os.File, name string) (string, error) {
	head := make([]byte, sniffBytes)
	// io.ReadFull fills head, or stops early at the end of a short file.
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("can't read %s: %w", name, err)
	}
	if !isImageData(head[:n]) {
		return "", fmt.Errorf("%s has an image's name, but its content isn't a PNG, JPEG, GIF or WebP image", name)
	}
	// Go back to the start, so the copy gets the whole file.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("can't read %s: %w", name, err)
	}
	saved, _, err := saveNew(root, imageName(name), f, imageCap)
	if err != nil {
		return "", fmt.Errorf("can't copy %s: %w", name, err)
	}
	return saved, nil
}

// imageName is safeName for an image: it keeps the ending, in lower
// case, even when it must cut a long name, because Image and the clients
// tell an image by its ending.
func imageName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	base := safeName(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), "image")
	if len(base) > maxNameChars-len(ext) {
		base = base[:maxNameChars-len(ext)]
	}
	return base + ext
}

// Image returns the bytes of the image at path, for a question that
// carries it. path must be a copy Upload made: a full path to a file
// right inside <[skills] output_dir>/uploads, with an image's name.
//
// A client names the path, so Image trusts none of it. It opens the file
// through an os.Root on the uploads folder, which can't reach outside
// it, and refuses a symbolic link, anything but a regular file, a file
// over imageCap, and one whose first bytes aren't an image's.
//
// It fails, with a message the user reads, on any of those, and when
// there is no output folder or the file is gone.
func (t *Tools) Image(path string) ([]byte, error) {
	if t.outputDir == "" {
		return nil, errors.New("there is no output folder for images; set [skills] output_dir in config.toml")
	}
	dir := filepath.Join(t.outputDir, uploadsFolder)
	clean := filepath.Clean(path)
	name := filepath.Base(clean)
	if !filepath.IsAbs(path) || filepath.Dir(clean) != dir {
		return nil, fmt.Errorf("%s isn't an image you attached; Meru sends only images in %s", path, dir)
	}
	if !IsImageName(name) {
		return nil, fmt.Errorf("%s isn't a PNG, JPEG, GIF or WebP image", name)
	}
	root, err := openFolder(t.outputDir, uploadsFolder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// root.Lstat describes the name itself; for a symbolic link it
	// describes the link, not what it points to.
	info, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%s isn't in the uploads folder any more; attach it again", name)
	case err != nil:
		return nil, fmt.Errorf("can't read %s: %w", name, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symbolic link, and Meru never sends one", name)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s isn't a regular file", name)
	case info.Size() > imageCap:
		return nil, fmt.Errorf("%s is %s, and Meru takes images up to %s", name, size(info.Size()), size(imageCap))
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("can't read %s: %w", name, err)
	}
	defer f.Close()
	// The name could have changed between Lstat and Open; SameFile
	// compares the file Open reached with the one Lstat saw.
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed while Meru read it; attach it again", name)
	}
	// One byte past the cap means the file grew after Lstat.
	b, err := io.ReadAll(io.LimitReader(f, imageCap+1))
	if err != nil {
		return nil, fmt.Errorf("can't read %s: %w", name, err)
	}
	if len(b) > imageCap {
		return nil, fmt.Errorf("%s is over %s, and Meru takes images up to that", name, size(imageCap))
	}
	if !isImageData(b[:min(len(b), sniffBytes)]) {
		return nil, fmt.Errorf("%s isn't a PNG, JPEG, GIF or WebP image", name)
	}
	return b, nil
}
