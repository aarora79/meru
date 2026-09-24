// This file holds the built-in skills: the copies of writing and explainer
// compiled into the binary, InstallBuiltins to put them on disk on first run,
// and Reset to restore one after you've edited it.

package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// builtinFS holds every file under builtin/, compiled into the binary.
//
// The //go:embed line below is a directive to the Go compiler, not a comment:
// at build time it reads the named directory and stores its files inside the
// program, so merud needs no data files next to it. embed only works on a
// variable declared at package level. The embed.FS it fills is read-only, so
// this is not the package-level mutable state AGENTS.md forbids.
// More in docs/coding-notes/go-basics/embed.md.
//
//go:embed builtin
var builtinFS embed.FS

// builtinRoot is the directory inside builtinFS that holds one folder per
// built-in skill. Paths inside an embed.FS always use "/", on every OS.
const builtinRoot = "builtin"

// ErrNotBuiltin reports a Reset for a skill Meru doesn't ship.
var ErrNotBuiltin = errors.New("not a built-in skill")

// Builtins returns the names of the skills Meru ships, sorted.
func Builtins() []string {
	// The embedded directory is fixed at build time, so this read can't fail
	// unless the build itself is broken; the tests prove it isn't.
	entries, err := fs.ReadDir(builtinFS, builtinRoot)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names
}

// InstallBuiltins copies each built-in skill to <dir>/<name>/ when no file or
// folder called <name> exists there yet, and returns the names it copied.
// A folder that already exists is left alone, even an empty one: your copy
// wins (ARCHITECTURE.md, "Built-in skills").
//
// Each skill is written into a hidden temporary folder first and then renamed
// into place, so a crash halfway leaves no half-written skill that later runs
// would mistake for your edited copy. Folders get mode 0700 and files 0600.
// It fails when dir can't be created or a file can't be written.
func InstallBuiltins(dir string) (installed []string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create skills directory %s: %w", dir, err)
	}
	for _, name := range Builtins() {
		target := filepath.Join(dir, name)
		// Lstat looks at the entry itself without following a symbolic link,
		// so a link you put there also counts as "already exists".
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return installed, fmt.Errorf("check skill folder %s: %w", target, err)
		}
		if err := installOne(dir, name, target); err != nil {
			return installed, err
		}
		installed = append(installed, name)
	}
	return installed, nil
}

// installOne writes the built-in skill name into a new temporary folder under
// dir and renames that folder to target. It removes the temporary folder when
// anything fails.
func installOne(dir, name, target string) (err error) {
	// MkdirTemp creates a new folder with a random suffix and mode 0700. The
	// leading "." keeps Load from reading it if we crash before the rename.
	tmp, err := os.MkdirTemp(dir, "."+name+"-")
	if err != nil {
		return fmt.Errorf("install skill %s: %w", name, err)
	}
	// This deferred function runs when installOne returns. err is a named
	// result, so the function sees the error being returned and cleans up
	// only on failure.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := writeBuiltinFiles(name, tmp); err != nil {
		return fmt.Errorf("install skill %s: %w", name, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("install skill %s: %w", name, err)
	}
	return nil
}

// Reset overwrites <dir>/<name>/ with the shipped version of the built-in
// skill name, for `meru skills reset`. It replaces only the files Meru ships;
// any other file you added to that folder stays. It creates the folder when it
// is missing.
//
// It fails with ErrNotBuiltin when Meru doesn't ship a skill called name, and
// refuses when <dir>/<name> is a symbolic link or a plain file, because
// writing through a link would change files outside the skills directory.
func Reset(dir, name string) error {
	if !slices.Contains(Builtins(), name) {
		return fmt.Errorf("reset %q: %w", name, ErrNotBuiltin)
	}
	target := filepath.Join(dir, name)
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(target, 0o700); err != nil {
			return fmt.Errorf("reset skill %s: %w", name, err)
		}
	case err != nil:
		return fmt.Errorf("reset skill %s: %w", name, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("reset skill %s: %s is a symbolic link; remove it first", name, target)
	case !info.IsDir():
		return fmt.Errorf("reset skill %s: %s is not a folder", name, target)
	}
	if err := writeBuiltinFiles(name, target); err != nil {
		return fmt.Errorf("reset skill %s: %w", name, err)
	}
	return nil
}

// writeBuiltinFiles copies every file of the built-in skill name into the
// folder dest, replacing files of the same name. Each file is written to a
// temporary file in dest and renamed over the old one, so a reader never sees
// half a file. It fails on the first file it can't write.
func writeBuiltinFiles(name, dest string) error {
	src := path.Join(builtinRoot, name)
	// fs.WalkDir visits every file and folder under src inside the embedded
	// files, calling the function once for each.
	return fs.WalkDir(builtinFS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// p is a slash path such as "builtin/writing/SKILL.md". Cut the
		// "builtin/writing" prefix to get the path inside the skill folder.
		rel := strings.TrimPrefix(strings.TrimPrefix(p, src), "/")
		out := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		data, err := builtinFS.ReadFile(p)
		if err != nil {
			return err
		}
		return writeFileAtomic(out, data)
	})
}

// writeFileAtomic writes data to dst with mode 0600 by writing a temporary
// file in the same folder and renaming it over dst. A rename within one
// folder replaces the old file in one step, so a reader sees either the old
// file or the new one, never a mix.
func writeFileAtomic(dst string, data []byte) (err error) {
	// CreateTemp opens a new file with a random name and mode 0600.
	f, err := os.CreateTemp(filepath.Dir(dst), ".tmp-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
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
	return os.Rename(f.Name(), dst)
}
