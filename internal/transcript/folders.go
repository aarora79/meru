// This file holds the list of chat folders: folders.json in the sessions
// directory, a JSON array of names. A chat's own meta line says which
// folder it sits in; this list keeps a folder that holds no chat yet, and
// the order the user sees.

package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FoldersFile is the name of the folder list in the sessions directory.
// It ends in .json, not .jsonl, so nothing that walks the sessions takes it
// for a transcript.
const FoldersFile = "folders.json"

// LoadFolders returns the chat folders listed in dir/folders.json, in the
// file's order. A missing file means no folders and no error. It fails
// when the file can't be read or isn't a JSON array of strings.
func LoadFolders(dir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, FoldersFile)) // #nosec G304 -- a fixed name in merud's own sessions directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read chat folders: %w", err)
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return nil, fmt.Errorf("read chat folders: %s isn't a list of names: %w", FoldersFile, err)
	}
	return names, nil
}

// SaveFolders writes names to dir/folders.json, creating dir when needed.
// It writes a temporary file and renames it over the old one, so a crash
// leaves the old list or the new one, never half of one. The file gets
// mode 0600, as the transcripts do. It fails when the file can't be
// written.
func SaveFolders(dir string, names []string) error {
	if names == nil {
		names = []string{} // "[]" reads better in the file than "null"
	}
	b, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return fmt.Errorf("save chat folders: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save chat folders: %w", err)
	}
	tmp := filepath.Join(dir, FoldersFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("save chat folders: %w", err)
	}
	// os.Rename replaces the old file in one step on the same disk.
	if err := os.Rename(tmp, filepath.Join(dir, FoldersFile)); err != nil {
		_ = os.Remove(tmp) // the rename error is the one worth reporting
		return fmt.Errorf("save chat folders: %w", err)
	}
	return nil
}
