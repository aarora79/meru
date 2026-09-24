# embed

**In one line:** a `//go:embed` line above a variable tells the compiler to copy
files into the program, so the binary carries them with it.

## Why Go has it

A program that needs data files (templates, default settings, Meru's built-in
skills) would otherwise need them installed next to it and found at run time. With
`embed`, the files become part of the binary, and one file on disk is the whole
program.

## Smallest example

```go
package main

import (
    "embed"
    "fmt"
)

//go:embed hello.txt
var hello string // one file, as a string

//go:embed skills
var skills embed.FS // a whole folder, as a read-only file system

func main() {
    fmt.Print(hello)
    data, _ := skills.ReadFile("skills/writing/SKILL.md")
    fmt.Println(len(data))
}
```

- The line must read `//go:embed` with no space after `//`. With a space it is an
  ordinary comment, and the compiler ignores it.
- The directive works only on a variable at package level, of type `string`,
  `[]byte` or `embed.FS`.
- Paths are relative to the folder holding the Go file, and can't reach above it
  with `..`.
- Embedding a folder skips files whose names start with `.` or `_`. Write
  `//go:embed all:skills` to include them.
- Inside an `embed.FS`, paths always use `/`, on Windows too.

## Where Meru uses it

- `internal/skills/builtin.go` — `//go:embed builtin` puts the `writing` and
  `explainer` skills inside `merud`. `InstallBuiltins` copies them to
  `~/.meru/skills/` on first run, and `Reset` copies one back.

## Mistakes to avoid

- Treating the variable as mutable state. An `embed.FS` is read-only, which is why
  Meru allows it at package level. A `string` or `[]byte` embed can be changed at
  run time; don't.
- Forgetting that the files are frozen at build time. Editing
  `internal/skills/builtin/` changes nothing until you rebuild.
