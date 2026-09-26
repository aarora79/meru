// This file holds web_fetch's download mode: with save set, web_fetch
// writes the response body to a file in <[skills] output_dir>/downloads
// instead of reading it into the model's context. It uses the same page
// client as a fetch, so the same public-address check, redirect cap and
// cookie rule apply; only the size cap and the timeout are larger. See
// ARCHITECTURE.md, "Web search".

package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Limits on a download.
const (
	// downloadsFolder is the folder under [skills] output_dir that holds
	// downloads. read_file and grep may read it, as they read all of
	// output_dir (see index.ReadAlso).
	downloadsFolder = "downloads"
	// downloadCap refuses a file over 50 MiB: past most PDFs, datasets and
	// archives a person asks an assistant for, and small enough that a
	// runaway download can't fill the disk. It isn't a config key; nobody
	// has needed another number yet.
	downloadCap = 50 << 20
	// downloadTimeout covers one download, redirects and body included.
	// 50 MiB at 5 Mbit/s takes about 80 seconds.
	downloadTimeout = 120 * time.Second
	// previewChars is how much of a downloaded HTML, PDF or text file's
	// text the result shows, so the model knows what it got.
	previewChars = 2000
	// maxNameChars caps a download's file name, before any "-2" suffix.
	maxNameChars = 100
	// maxNameTries is how many "-2", "-3" ... names download tries before
	// it gives up.
	maxNameTries = 100
)

// download fetches rawURL and saves the body as a new file in the
// downloads folder, then returns the saved path, the size, the content
// type and, for HTML, PDF or plain text, the first previewChars characters
// of its text. It never overwrites: a name in use gets "-2", "-3" and so
// on. It fails, with text the model reads, when there is no output
// folder, the address isn't public, the fetch fails or passes downloadCap
// or downloadTimeout, the downloads folder is a symbolic link or a file,
// or the write fails. A failed download leaves no file behind.
func (t *Tools) download(ctx context.Context, rawURL string) (string, error) {
	if t.outputDir == "" {
		return "", errors.New("web_fetch: save needs an output folder, and [skills] output_dir names none")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	resp, final, err := t.web.get(ctx, rawURL, "*/*", downloadTimeout)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	name := downloadName(resp.Header.Get("Content-Disposition"), resp.Request.URL)

	root, err := openFolder(t.outputDir, downloadsFolder)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w. Nothing was saved", err)
	}
	defer root.Close()
	saved, n, err := saveNew(root, name, resp.Body, downloadCap)
	if err != nil {
		if isTimeout(err) {
			return "", fmt.Errorf("web_fetch: %s took longer than %v. Nothing was saved", final, downloadTimeout)
		}
		return "", fmt.Errorf("web_fetch: %s: %w. Nothing was saved", final, err)
	}

	abs := filepath.Join(t.outputDir, downloadsFolder, saved)
	shownType := mediaType
	if shownType == "" {
		shownType = "no content type given"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Saved %s to %s. %s, %s.", final, abs, size(n), shownType)
	if preview := previewText(root, saved, mediaType, n); preview != "" {
		fmt.Fprintf(&b, "\n\nThe first %d characters of its text:\n\n%s", previewChars, preview)
		if t.hasFiles() {
			b.WriteString("\n\n[Read the rest with read_file, or search it with grep.]")
		}
	}
	return b.String(), nil
}

// openFolder returns an os.Root on the folder named folder inside dir,
// creating both with mode 0700 when they don't exist. An os.Root refuses
// any path that would leave its folder, even through a symbolic link. It
// fails when folder is a symbolic link or a file, so a link planted there
// can't send a file somewhere else. download opens the downloads folder
// with it, and Upload the uploads folder.
func openFolder(dir, folder string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	out, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer out.Close()
	info, err := out.Lstat(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := out.Mkdir(folder, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create %s: %w", filepath.Join(dir, folder), err)
		}
	case err != nil:
		return nil, err
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symbolic link, and Meru never saves through one", filepath.Join(dir, folder))
	case !info.IsDir():
		return nil, fmt.Errorf("%s is a file, not a folder", filepath.Join(dir, folder))
	}
	return out.OpenRoot(folder)
}

// saveNew copies body into a new file in root, named name or, when that
// name is taken, name with "-2", "-3" and so on before its extension. It
// opens each name with O_CREATE|O_EXCL, which fails when anything, a
// symbolic link included, already has that name, so it never writes
// through a link or over a file. The file gets mode 0600. It returns the
// name it used and the bytes written, and removes the file when the copy
// fails or passes limit bytes.
func saveNew(root *os.Root, name string, body io.Reader, limit int64) (saved string, n int64, err error) {
	var f *os.File
	var file string
	for i := 1; i <= maxNameTries; i++ {
		file = numbered(name, i)
		f, err = root.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", 0, fmt.Errorf("%d files named like %s exist already", maxNameTries, name)
		}
		return "", 0, err
	}
	// err is a named result, so this deferred function sees the error
	// being returned and removes the half-written file only on failure.
	// It uses file, not saved: a return sets saved to "" before the
	// deferred function runs.
	defer func() {
		if err != nil {
			_ = root.Remove(file)
		}
	}()
	// io.Copy stops at limit+1 bytes; one byte past the limit means the
	// file is too large, and it goes.
	n, err = io.Copy(f, io.LimitReader(body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("the file is larger than %s, which is as much as Meru saves", size(limit))
	}
	if err != nil {
		_ = f.Close()
		return "", 0, err
	}
	// Close can report a write the OS delayed, so its error counts.
	if err = f.Close(); err != nil {
		return "", 0, err
	}
	return file, n, nil
}

// numbered returns name for i == 1, and name with "-<i>" before its
// extension otherwise: "report.pdf", "report-2.pdf", "report-3.pdf".
func numbered(name string, i int) string {
	if i == 1 {
		return name
	}
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), i, ext)
}

// downloadName picks the file name for a download: the filename in the
// Content-Disposition header when the site sends one, or else the last
// part of the URL's path, made safe by safeName.
func downloadName(disposition string, u *url.URL) string {
	var name string
	if _, params, err := mime.ParseMediaType(disposition); err == nil {
		name = params["filename"]
	}
	if name == "" && u != nil {
		name = path.Base(u.Path)
	}
	return safeName(name, "download")
}

// safeName turns name into a file name Meru can save under: it keeps only
// the part after the last "/" or "\", turns every character but ASCII
// letters, digits, ".", "-" and "_" into "_", drops leading dots so the
// file is never hidden, and cuts the name to maxNameChars. A name that
// ends up empty becomes fallback.
func safeName(name, fallback string) string {
	name = name[strings.LastIndexAny(name, `/\`)+1:]
	// strings.Map calls the function on each character and builds a new
	// string from what it returns.
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
	name = strings.TrimLeft(name, ".")
	if len(name) > maxNameChars {
		// Every character is ASCII now, so cutting bytes cuts characters.
		name = name[:maxNameChars]
	}
	if strings.Trim(name, "_.-") == "" {
		return fallback
	}
	return name
}

// previewText returns the first previewChars characters of the text of the
// file saved in root, for HTML, PDF and plain text files up to pageBodyCap
// bytes, and "" for anything else or a file it can't read. A larger file
// would cost too much memory for a preview.
func previewText(root *os.Root, name, mediaType string, n int64) string {
	if !slices.Contains(textTypes, mediaType) || n > pageBodyCap {
		return ""
	}
	body, err := root.ReadFile(name)
	if err != nil {
		return ""
	}
	p, err := pageText(mediaType, body)
	if err != nil {
		return ""
	}
	runes := []rune(strings.TrimSpace(p.text))
	return string(runes[:min(len(runes), previewChars)])
}
