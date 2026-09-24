# build tags

**In one line:** a `//go:build` line at the top of a file tells the go command
when to include that file in a build.

## Why Go has it

One package often needs a file that only some builds should see: code for one
operating system, or slow tests that shouldn't run every time. A build tag
states the condition in the file itself, so no build script has to pick files.

## Smallest example

```go
//go:build e2e

// This file only compiles with `go test -tags e2e`.
package e2e

import "testing"

func TestHello(t *testing.T) {
    // start merud and meru, ask a question, check the answer
}
```

The `//go:build` line must come before `package`, with a blank line after it.
Without `-tags e2e`, the go command skips the file, so `go test ./...` never
sees it.

Conditions combine with `&&`, `||` and `!`: `//go:build linux && !arm64`.
Every operating system and architecture name works as a tag without being
set, which is how `//go:build darwin` works.

A file name can do the same for those names: `socket_windows.go` builds only
for Windows, and `cpu_arm64.go` only for 64-bit ARM. AGENTS.md asks for this
form for platform code.

## Where Meru uses it

- `test/e2e/` — the end-to-end tests carry `//go:build e2e`, because they build
  the real binaries and take longer. `make e2e` runs them, and so does the
  `e2e` job in CI.
- Meru reserves the `integration` tag for tests that need a real Ollama on
  this machine. Run them with `go test -tags integration ./...`, and the end-to-end
  one with `go test -tags 'e2e integration' ./test/e2e/...`. CI doesn't, because
  its runners have no model.
- `internal/policy` parses files regardless of tags, so a file behind `e2e`
  still has to follow the rules.

## Mistakes to avoid

- A missing blank line after `//go:build` turns it into an ordinary comment,
  and the file builds every time.
- A tagged file that no build includes can rot unseen. Make sure some CI job
  passes the tag, as the `e2e` job does.
