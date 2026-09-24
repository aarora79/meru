# filepath

**In one line:** the `path/filepath` package builds, splits and walks file paths
with the separator of the OS the program runs on.

## Why Go has it

macOS and Linux separate folders with `/`, Windows with `\`. Code that glues
paths together with `"/"` breaks on Windows. `filepath.Join`, `filepath.Dir`
and `filepath.Rel` pick the right separator, and `filepath.WalkDir` visits every
file under a folder so you don't write the recursion yourself.

The plain `path` package does the same jobs for slash-only paths, such as URLs
and gitignore patterns.

## Smallest example

```go
package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

func main() {
	root := filepath.Join("notes", "2026") // "notes/2026", or `notes\2026` on Windows
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // stop the walk
		}
		if d.IsDir() && d.Name() == "node_modules" {
			return fs.SkipDir // don't go inside this folder
		}
		fmt.Println(p)
		return nil
	})
}
```

- `WalkDir` calls the function once per entry, parents before children.
- Returning `fs.SkipDir` for a folder skips everything inside it. `fs.SkipAll`
  stops the whole walk without an error.
- `WalkDir` doesn't follow symlinks; a link shows up with `fs.ModeSymlink` set in
  `d.Type()`.
- `filepath.Rel(root, p)` gives `p` relative to `root`, and
  `filepath.ToSlash` turns it into a `/` path for matching patterns.
- `filepath.EvalSymlinks(p)` returns the real path with every symlink resolved.

## Where Meru uses it

- `internal/index/indexer.go` — `scanTree` walks each indexed folder and prunes
  skipped folders with `fs.SkipDir`; `roots` resolves each folder's symlinks so
  files are stored under their real paths; `within` uses `filepath.Rel` to test
  whether a path sits inside a folder.
- `internal/index/skip.go` — `ignored` turns a path into a slash-separated one
  relative to each ignore file's folder before matching.
- `internal/index/watch.go` — `addTree` walks a folder to add a watch for each
  subfolder, and stops with `fs.SkipAll` when the OS runs out of watches.
- `internal/config/load.go` — `filepath.IsAbs` checks each `[index] folders`
  entry.

## Mistakes to avoid

- Testing "is p inside dir" with `strings.HasPrefix(p, dir)`. `/notes-old/x`
  starts with `/notes`. Use `filepath.Rel` and check the result doesn't start
  with `..`, or add a trailing separator to the prefix.
- Comparing paths from `t.TempDir()` with paths the code resolved. On macOS
  `/var` links to `/private/var`, so run the test's folder through
  `filepath.EvalSymlinks` first.
