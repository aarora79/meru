// This file holds Stamp, the cheap check that tells merud when the skills
// directory changed and the registry needs loading again.

package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Stamp returns a string that changes whenever a skill folder under dir
// appears, goes away or is renamed, or a SKILL.md in one changes size or
// modification time. merud compares it with the stamp of its last Load and
// loads again when the two differ, so a skill you add or edit by hand
// counts from the next turn.
//
// It costs one directory read and one file stat per skill, a few
// microseconds for a handful of skills, which is why merud can afford it on
// every turn instead of running a file watcher. Folders whose names start
// with "." are left out, as Load leaves them out.
//
// It returns "" when dir doesn't exist, and fails only when dir exists but
// can't be read.
func Stamp(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read skills directory %s: %w", dir, err)
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// os.Stat follows a symbolic link, as Load does, so an edit to a
		// skill linked in from elsewhere counts too.
		info, err := os.Stat(filepath.Join(dir, name, fileName))
		if err != nil {
			// A folder with no readable SKILL.md still marks its place, so
			// adding the file later changes the stamp.
			fmt.Fprintf(&b, "%s:-\n", name)
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d\n", name, info.Size(), info.ModTime().UnixNano())
	}
	return b.String(), nil
}
