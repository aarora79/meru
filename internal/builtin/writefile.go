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
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

// writeFileDescription tells the model what write_file does. It names the
// folder, so the model can tell the user where to look.
func writeFileDescription(dir string) string {
	return "Saves a text file for the user, such as a document, an email draft or an HTML page, in the folder " +
		dir + ". Pass path relative to that folder, such as \"drafts/landlord-email.md\", and the whole file as content. " +
		"It refuses to replace a file that exists unless overwrite is true. " +
		"The result gives the full path; tell the user where the file is."
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
	rel, err := cleanRelPath(a.Path)
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
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A new file: the usual case.
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

	if err := writeAtomic(root, rel, []byte(a.Content)); err != nil {
		return "", fmt.Errorf("write_file: write %s: %w. Nothing was written", a.Path, err)
	}
	abs := filepath.Join(t.outputDir, rel)
	return fmt.Sprintf("Wrote %d bytes to %s.", len(a.Content), abs), nil
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
