// This file holds the edit_file tool, which replaces exact text in a file
// inside the output folder, such as one name in a report Meru saved. Before
// it, the only way to change a file was write_file with the whole file as
// content: a real session asked to fix two names in a 14 KB page, and the
// model, which would have had to write all 14 KB back, said it couldn't.

package builtin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EditFile is the edit_file tool's name, as the model sees it.
const EditFile = "edit_file"

// editFileArgs is the JSON object the model sends to edit_file.
type editFileArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
	// All replaces every match; without it, old must match once, so an
	// edit meant for one place can't change three.
	All bool `json:"all"`
}

// editFileDescription tells the model what edit_file does.
func editFileDescription(dir string) string {
	return "Replaces exact text in a file in the folder " + dir + ", such as a name or a number in a page you saved. " +
		"Pass path as write_file takes it, old as the exact text to find and new as its replacement. " +
		"old must match once, unless all is true, which replaces every match. " +
		"Read the file first when you don't know its exact text. The result says how many places changed and gives a link to the file."
}

// editFile checks args, replaces the text and writes the file inside
// t.outputDir. It returns the text the model reads: how many places
// changed, and the link to the file. It fails, with text the model reads,
// when the path breaks write_file's rules, when the file isn't there or
// isn't a plain file, when old is empty or found nowhere, when old matches
// more than once and all isn't set, when the result is over 1 MiB, or when
// the write fails.
//
// Before it writes, it saves the file as it was to a hidden .<name>.bak
// beside it, replacing the last one, so one edit can be undone by hand.
// Every step goes through an os.Root on the output folder, as in
// writeFile.
func (t *Tools) editFile(raw json.RawMessage) (string, error) {
	var a editFileArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("edit_file: the arguments aren't a valid JSON object for this tool: %v. Nothing changed", err)
	}
	if a.Old == "" {
		return "", errors.New("edit_file: old is empty; pass the exact text to replace. Nothing changed")
	}
	rel, err := cleanRelPath(t.insideOutput(a.Path))
	if err != nil {
		return "", fmt.Errorf("edit_file: %w. Nothing changed", err)
	}
	root, err := os.OpenRoot(t.outputDir)
	if err != nil {
		return "", fmt.Errorf("edit_file: open %s: %w. Nothing changed", t.outputDir, err)
	}
	// defer runs root.Close() when editFile returns, on every path.
	defer root.Close()

	info, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("edit_file: %s isn't in the output folder; write_file makes a new file. Nothing changed", a.Path)
	case err != nil:
		return "", fmt.Errorf("edit_file: %s: %w. Nothing changed", a.Path, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("edit_file: %s is a symbolic link, and edit_file never writes through one. Nothing changed", a.Path)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("edit_file: %s isn't a plain file. Nothing changed", a.Path)
	case info.Size() > maxWriteBytes:
		return "", fmt.Errorf("edit_file: %s is over the 1 MiB limit. Nothing changed", a.Path)
	}
	old, err := root.ReadFile(rel)
	if err != nil {
		return "", fmt.Errorf("edit_file: read %s: %w. Nothing changed", a.Path, err)
	}
	text := string(old)
	n := strings.Count(text, a.Old)
	switch {
	case n == 0:
		return "", fmt.Errorf("edit_file: %q isn't in %s; read the file and pass its exact text. Nothing changed", a.Old, a.Path)
	case n > 1 && !a.All:
		return "", fmt.Errorf("edit_file: %q is in %s %d times; set all to true to change every one, "+
			"or pass more of the text around the one to change. Nothing changed", a.Old, a.Path, n)
	}
	edited := strings.ReplaceAll(text, a.Old, a.New)
	if len(edited) > maxWriteBytes {
		return "", fmt.Errorf("edit_file: the edited file would be over the 1 MiB limit. Nothing changed")
	}
	backup := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".bak")
	if err := writeAtomic(root, backup, old); err != nil {
		return "", fmt.Errorf("edit_file: save a copy of %s first: %w. Nothing changed", a.Path, err)
	}
	if err := writeAtomic(root, rel, []byte(edited)); err != nil {
		return "", fmt.Errorf("edit_file: write %s: %w. Nothing changed", a.Path, err)
	}
	abs := filepath.Join(t.outputDir, rel)
	places := "1 place"
	if n > 1 {
		places = fmt.Sprintf("%d places", n)
	}
	return fmt.Sprintf("Replaced %s in %s.\n%s[%s](%s)", places, abs, linkLead, filepath.Base(abs), FileURL(abs)), nil
}

// editFileSchema returns the JSON Schema for edit_file's arguments.
func editFileSchema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file, relative to the output folder, such as \"reports/october.html\".",
			},
			"old": map[string]any{
				"type":        "string",
				"description": "The exact text to find, as the file has it.",
			},
			"new": map[string]any{
				"type":        "string",
				"description": "The text to put in its place; empty deletes it.",
			},
			"all": map[string]any{
				"type":        "boolean",
				"description": "Set true to replace every match; otherwise old must match once.",
			},
		},
		"required":             []string{"path", "old", "new"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(s)
	return b
}
