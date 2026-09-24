// This file fits a catalog entry to this machine and to the words after
// `meru mcp add <name>`: which system the entry runs on, and the folders an
// entry such as filesystem takes on the command line.

package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RunsOn reports whether e works on the system goos, a runtime.GOOS value
// such as "darwin" or "windows". An entry with no OS runs everywhere.
func (e Entry) RunsOn(goos string) bool {
	return e.OS == "" || e.OS == goos
}

// WithArgs returns a copy of e with args, the words a person typed after
// the entry's name, filled in. For an entry with a NeedFolders need, each
// arg is a folder: WithArgs turns it into an absolute path, adds it to the
// end of Args and drops the need, since nothing is left to ask. It fails
// when such an entry gets no folder or a folder that doesn't exist, and
// when an entry that takes no args gets some.
func (e Entry) WithArgs(args []string) (Entry, error) {
	i := slices.IndexFunc(e.Needs, func(n Need) bool { return n.Kind == NeedFolders })
	if i < 0 {
		if len(args) > 0 {
			return e, fmt.Errorf("%s takes no arguments; got %s", e.Name, strings.Join(args, " "))
		}
		return e, nil
	}
	if len(args) == 0 {
		return e, fmt.Errorf("%s needs at least one folder: meru mcp add %s <folder> [folder...]", e.Name, e.Name)
	}
	folders := make([]string, 0, len(args))
	for _, a := range args {
		f, err := ExpandFolder(a)
		if err != nil {
			return e, err
		}
		folders = append(folders, f)
	}
	// Build new slices, so the copy shares no backing array with e.
	e.Args = append(slices.Clone(e.Args), folders...)
	e.Needs = slices.Delete(slices.Clone(e.Needs), i, i+1)
	return e, nil
}

// ExpandFolder turns path into the absolute path of a folder that exists.
// It expands a leading "~" to the home folder, because a server started by
// merud gets no shell to do that, and the filesystem server wants absolute
// paths. It fails when the folder is missing or isn't a folder.
func ExpandFolder(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %s: %w", path, err)
		}
		p = filepath.Join(home, p[1:])
	}
	// Abs also cleans the path: "a/../b" becomes "b".
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("folder %s: %w", path, err)
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("folder %s doesn't exist", abs)
	case err != nil:
		return "", fmt.Errorf("folder %s: %w", abs, err)
	case !info.IsDir():
		return "", fmt.Errorf("%s is a file, not a folder", abs)
	}
	return abs, nil
}
