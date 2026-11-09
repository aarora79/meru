// This file holds the write_file tool, which saves a file the model made,
// such as an email draft or an explainer page, inside the one folder
// [skills] output_dir names. See ARCHITECTURE.md, "Built-in skills".

package builtin

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// WriteFile is the write_file tool's name, as the model sees it.
const WriteFile = "write_file"

// maxWriteBytes caps one file. 1 MiB holds a long report or an explainer
// page with inline diagrams; anything larger from a model is most likely a
// loop that never stopped.
const maxWriteBytes = 1 << 20

// writeFileArgs is the JSON object the model sends to write_file.
type writeFileArgs struct {
	Path string `json:"path"`
	// FilePath is another name for Path. A real model called write_file
	// with "file_path", the call failed, and the turn ran out of time
	// before it tried again; the page it had written was lost.
	FilePath  string `json:"file_path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

// writeFileDescription tells the model what write_file does. It names the
// folder, so the model can tell the user where to look.
func writeFileDescription(dir string) string {
	return "Saves a text file for the user, such as a document, an email draft or an HTML page, in the folder " +
		dir + ". Pass path relative to that folder, such as \"drafts/landlord-email.md\", and the whole file as content. " +
		"It refuses to replace a file that exists unless overwrite is true. " +
		"The result gives a link to the file; give the user that link as it stands, so they can click it. " +
		"To put a picture in an HTML page, such as a photo in a card, write <img src=\"...\"> or a CSS url(...) with the " +
		"picture's path inside " + dir + ", such as a downloaded attachment's path; write_file puts the picture itself " +
		"into the page, so it shows on a phone too, and its result says which pictures went in. You can't make or edit " +
		"a picture's pixels; lay the picture out with HTML and CSS instead. Use only pictures the user gave or named, " +
		"never ID cards, certificates or other documents."
}

// writeFile checks args and writes the file inside t.outputDir. It returns
// the text the model reads, which holds the absolute path written. It
// fails, with text the model reads, when the path is empty, absolute, holds
// "..", or passes through a symbolic link; when the content is over 1 MiB;
// when the file exists and overwrite isn't set; or when the write fails.
//
// Every file operation goes through an os.Root opened on the output folder.
// An os.Root refuses any path that would leave its folder, even through a
// symbolic link, so it backs up the checks here if one of them has a gap.
func (t *Tools) writeFile(raw json.RawMessage) (string, error) {
	var a writeFileArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("write_file: the arguments aren't a valid JSON object for this tool: %v. Nothing was written", err)
	}
	if a.Path == "" {
		a.Path = a.FilePath
	}
	rel, err := cleanRelPath(t.insideOutput(a.Path))
	if err != nil {
		return "", fmt.Errorf("write_file: %w. Nothing was written", err)
	}
	if len(a.Content) > maxWriteBytes {
		return "", fmt.Errorf("write_file: the content is %d bytes, over the 1 MiB limit. Nothing was written", len(a.Content))
	}

	if err := os.MkdirAll(t.outputDir, 0o700); err != nil {
		return "", fmt.Errorf("write_file: create %s: %w. Nothing was written", t.outputDir, err)
	}
	root, err := os.OpenRoot(t.outputDir)
	if err != nil {
		return "", fmt.Errorf("write_file: open %s: %w. Nothing was written", t.outputDir, err)
	}
	// defer runs root.Close() when writeFile returns, on every path.
	defer root.Close()

	if err := makeParents(root, rel); err != nil {
		return "", fmt.Errorf("write_file: %w. Nothing was written", err)
	}
	info, err := root.Lstat(rel)
	verb := "Replaced" // what the result says happened
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A new file: the usual case.
		verb = "Wrote a new file,"
	case err != nil:
		return "", fmt.Errorf("write_file: %s: %w. Nothing was written", a.Path, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("write_file: %s is a symbolic link, and write_file never writes through one. Nothing was written", a.Path)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("write_file: %s exists and isn't a plain file. Nothing was written", a.Path)
	case !a.Overwrite:
		return "", fmt.Errorf("write_file: %s already exists. Ask the user whether to replace it, "+
			"then call again with overwrite set to true, or pick another path. Nothing was written", a.Path)
	}

	// An HTML page gets its local pictures put in; see inline.go.
	content, note := a.Content, ""
	if isPage(rel) {
		if content, note, err = t.inlinePictures(root, rel, content); err != nil {
			return "", fmt.Errorf("write_file: %s: %w. Nothing was written", a.Path, err)
		}
	}
	if err := writeAtomic(root, rel, []byte(content)); err != nil {
		return "", fmt.Errorf("write_file: write %s: %w. Nothing was written", a.Path, err)
	}
	abs := filepath.Join(t.outputDir, rel)
	if note != "" {
		note = " " + note
	}
	return fmt.Sprintf("%s %d bytes, at %s.%s\n%s[%s](%s)", verb, len(content), abs, note,
		linkLead, filepath.Base(abs), FileURL(abs)), nil
}

// linkLead starts the last line of a write_file result, the link. Written
// reads the link back from there.
const linkLead = "Link for the user: "

// Written returns the name and file:// link of the file a write_file
// result reports, and false for any other text, such as a refusal. The
// agent uses it to make sure an answer links each file a turn saved,
// since a model may write "click the link above", pointing at a tool
// result the user never sees.
func Written(result string) (name, link string, ok bool) {
	_, line, found := strings.Cut(result, "\n"+linkLead)
	if !found {
		return "", "", false
	}
	// The line reads [name](file:///…).
	n, u, found := strings.Cut(strings.TrimPrefix(line, "["), "](")
	if !found || !strings.HasSuffix(u, ")") {
		return "", "", false
	}
	return n, strings.TrimSuffix(u, ")"), true
}

// insideOutput turns a path that already names the output folder into one
// relative to it, and returns any other path unchanged for cleanRelPath to
// check. A model that read a file at ~/meru-output/uploads/page.html tends
// to pass that path, or "meru-output/uploads/page.html", back to
// write_file. Taken as relative, the second wrote a new file in
// ~/meru-output/meru-output/uploads/ while the user looked at the old one.
// So "~/<output>/x", an absolute path inside the output folder, and a
// relative path whose first folder has the output folder's name all mean x.
// An absolute path anywhere else stays absolute, and cleanRelPath refuses
// it.
func (t *Tools) insideOutput(p string) string {
	p = strings.TrimSpace(p)
	if rest, ok := strings.CutPrefix(p, "~"); ok && (strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, `\`)) {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(p) {
		// filepath.Rel finds the way from the output folder to p; one that
		// starts ".." leaves it.
		if rel, err := filepath.Rel(t.outputDir, filepath.Clean(p)); err == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
		return p
	}
	name := filepath.Base(t.outputDir)
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) > 1 && parts[0] == name {
		return strings.Join(parts[1:], "/")
	}
	return p
}

// FileURL returns the file:// URL for abs, an absolute path, with the
// characters a URL can't hold, such as spaces, escaped. Both clients open
// such a link with the system's opener.
func FileURL(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	// A Windows path such as C:/x needs a "/" before the drive letter.
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}

// cleanRelPath checks the path the model gave and returns it in the OS's
// form. It accepts "/" and, on Windows, "\" between folders. It fails for
// an empty path, an absolute path or one with a drive letter, a path with a
// ".." part, and a path that names the folder itself.
func cleanRelPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is empty; pass a file name such as \"notes.md\"")
	}
	// Refuse both kinds of separator at the start, and a drive letter, on
	// every OS: a model that writes "C:\x" or "/etc/x" means an absolute path
	// even when merud runs somewhere that reads it otherwise.
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" ||
		(len(p) >= 2 && p[1] == ':') {
		return "", fmt.Errorf("path %q is absolute; pass a path relative to the output folder, such as \"notes.md\"", p)
	}
	// FieldsFunc splits at both separators, so "a\..\b" can't hide a ".."
	// from a check that only knows "/".
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for _, part := range parts {
		if part == ".." {
			return "", fmt.Errorf("path %q holds \"..\"; write_file only writes inside the output folder", p)
		}
	}
	rel := filepath.Clean(filepath.Join(parts...))
	if rel == "." || rel == "" {
		return "", fmt.Errorf("path %q names no file", p)
	}
	return rel, nil
}

// makeParents creates the folders above rel inside root, with mode 0700. It
// fails when one of them is a symbolic link or a file, so a link planted in
// the output folder can't send the write somewhere else.
func makeParents(root *os.Root, rel string) error {
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	// Walk down from the top: "a", then "a/b", then "a/b/c".
	var sofar string
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		sofar = filepath.Join(sofar, part)
		info, err := root.Lstat(sofar)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Mkdir(sofar, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("create folder %s: %w", sofar, err)
			}
		case err != nil:
			return fmt.Errorf("folder %s: %w", sofar, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link, and write_file never writes through one", sofar)
		case !info.IsDir():
			return fmt.Errorf("%s is a file, not a folder", sofar)
		}
	}
	return nil
}

// writeAtomic writes data to rel inside root with mode 0600: first to a
// hidden temporary file in the same folder, then a rename over rel. A rename
// inside one folder happens in one step, so nobody ever sees half a file,
// and a failed write leaves any old file as it was.
func writeAtomic(root *os.Root, rel string, data []byte) (err error) {
	// rand.Text returns 26 random letters and digits, so two calls can't
	// pick the same temporary name.
	tmp := filepath.Join(filepath.Dir(rel), ".write-"+rand.Text()+".tmp")
	// O_EXCL makes the open fail if the name exists, so it never writes
	// into a file someone else made.
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// err is a named result, so this deferred function sees the error
	// being returned and removes the temporary file only on failure.
	defer func() {
		if err != nil {
			_ = root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	// Close can report a write the OS delayed, so its error counts.
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}

// writeFileSchema returns the JSON Schema for write_file's arguments.
func writeFileSchema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Where to save the file, relative to the output folder, such as \"drafts/email.md\".",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The whole text of the file.",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "Set true only when the user agreed to replace a file that exists.",
			},
		},
		"required":             []string{"path", "content"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(s)
	return b
}
