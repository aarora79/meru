// This file holds the "Folders to search" step: suggesting folders with a
// count of the files in each, and writing the chosen ones to [index]
// folders in config.toml.

package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// Folder is one folder on the Folders screen.
type Folder struct {
	// Path is how config.toml writes it, such as "~/Documents".
	Path string `json:"path"`
	// Files counts the files in it, up to countLimit; More is true when
	// there are more than that.
	Files int  `json:"files"`
	More  bool `json:"more"`
	// Chosen is true when config.toml already lists it, or when it is a
	// suggestion the user hasn't turned down.
	Chosen bool `json:"chosen"`
}

// countLimit and countTime cap how long the screen waits for a count: a
// home folder can hold millions of files, and the count is only a guide.
const (
	countLimit = 20000
	countTime  = 2 * time.Second
)

// SkipNote says what the indexer leaves out, for the Folders screen.
const SkipNote = "Meru skips hidden files and folders, secrets such as .env and *.pem files, build folders such as node_modules, " +
	"anything a .gitignore or .meruignore lists, media, archives and files over 5 MB. It never follows a link out of a folder."

// SuggestFolders returns the folders for the screen: the ones config.toml
// already lists, then Documents, Desktop and Notes in the home folder
// when they exist, each with its file count. The listed ones come chosen;
// a suggestion comes chosen only on a first run, when config lists none.
func SuggestFolders(ctx context.Context, p Paths, listed []string) []Folder {
	var out []Folder
	seen := map[string]bool{}
	add := func(path string, chosen bool) {
		if seen[path] {
			return
		}
		seen[path] = true
		n, more := countFiles(ctx, expandHome(path, p.Home))
		out = append(out, Folder{Path: path, Files: n, More: more, Chosen: chosen})
	}
	for _, f := range listed {
		add(p.Tilde(expandHome(f, p.Home)), true)
	}
	for _, name := range []string{"Documents", "Desktop", "Notes"} {
		dir := filepath.Join(p.Home, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			add(p.Tilde(dir), len(listed) == 0)
		}
	}
	return out
}

// PickedFolder turns a folder the user chose in the system's dialog into a
// Folder for the screen, chosen. It fails when path isn't a folder.
func PickedFolder(ctx context.Context, p Paths, path string) (Folder, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Folder{}, err
	}
	if !info.IsDir() {
		return Folder{}, fmt.Errorf("%s isn't a folder", path)
	}
	n, more := countFiles(ctx, path)
	return Folder{Path: p.Tilde(path), Files: n, More: more, Chosen: true}, nil
}

// errStop ends a count early. It is a plain value compared with ==.
var errStop = errors.New("stop counting")

// countFiles counts the files under dir the way the indexer would find
// them, skipping hidden files and folders, and stops at countLimit or
// after countTime. more is true when it stopped early.
func countFiles(ctx context.Context, dir string) (n int, more bool) {
	ctx, cancel := context.WithTimeout(ctx, countTime)
	defer cancel()
	// WalkDir calls the function for each file and folder under dir. It
	// doesn't follow links, as the indexer doesn't.
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a folder it can't read counts as empty
		}
		if path != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			n++
		}
		if n >= countLimit || ctx.Err() != nil {
			more = true
			return errStop
		}
		return nil
	})
	return n, more
}

// SaveFolders writes folders to [index] folders in config.toml, keeping
// every other line, and returns what it did. Each folder must be absolute
// or start with "~/"; config.Load checks that before the file changes.
func SaveFolders(p Paths, folders []string) (string, error) {
	if _, err := EnsureConfig(p.Config()); err != nil {
		return "", err
	}
	clean := make([]string, 0, len(folders))
	for _, f := range folders {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(clean, f) {
			clean = append(clean, f)
		}
	}
	err := catalog.SetTableLists(p.Config(), "index", map[string][]string{"folders": clean}, func(c config.Config) error {
		if !slices.Equal(c.Index.Folders, clean) {
			return fmt.Errorf("[index] folders came out as %v, want %v", c.Index.Folders, clean)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(clean) == 0 {
		return "Meru indexes no folders. Add some later in Meru.app's Settings, Folders, or under [index] folders in " + p.Tilde(p.Config()) + ".", nil
	}
	return "Wrote [index] folders = " + strings.Join(clean, ", ") + " in " + p.Tilde(p.Config()) + ".", nil
}
