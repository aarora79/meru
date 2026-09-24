# testing

**In one line:** Go runs every function named `TestXxx(t *testing.T)` in a
`_test.go` file when you type `go test`.

## Why Go has it

Go ships its test runner with the language, so every project tests the same
way and needs no framework. The `testing` package gives each test a `*testing.T`
to report failures, run subtests and clean up. Python's `pytest` finds tests
by name in a similar way; Go does it by file suffix and function name.

## Smallest example

```go
// file: add_test.go, next to add.go
package add

import "testing"

func TestAdd(t *testing.T) {
    tests := []struct{ a, b, want int }{{1, 2, 3}, {-1, 1, 0}}
    for _, tt := range tests {
        if got := Add(tt.a, tt.b); got != tt.want {
            t.Errorf("Add(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
        }
    }
}
```

`go test ./...` compiles each package with its `_test.go` files and runs every
`Test` function. The `_test.go` files never go into the built program.

The `*testing.T` methods Meru uses most:

| Method | Does |
| --- | --- |
| `t.Errorf` | Reports a failure and keeps going, so one run shows every broken case |
| `t.Fatalf` | Reports a failure and stops this test, for when later steps make no sense |
| `t.Run(name, func)` | Runs a subtest with its own name and its own pass or fail |
| `t.Helper()` | Marks a helper function, so a failure points at the caller's line |
| `t.Cleanup(func)` | Runs the function after the test ends, like `defer` for the whole test |
| `t.TempDir()` | Returns a fresh directory that Go deletes after the test |
| `t.Context()` | Returns a context that ends when the test ends |
| `t.Skip` | Marks the test skipped, with a reason |

## Where Meru uses it

- `internal/policy/*_test.go` — tests that read Meru's own source and enforce
  the non-negotiables.
- `internal/testutil/fakeollama/fake_test.go` — tests of the fake Ollama,
  written from outside the package (`package fakeollama_test`) so they use
  only what a caller can.
- `cmd/fakeollama/main_test.go` — tests of the flag helpers.

Useful flags: `-race` finds data races, `-count=1` skips the result cache,
`-run TestName` runs matching tests, `-v` prints each test name, and
`-coverprofile=coverage.out` records which lines ran.

## Mistakes to avoid

- A loop over cases that calls `t.Fatalf` stops at the first bad case. Use
  `t.Errorf`, or wrap each case in `t.Run` so `Fatalf` ends only that subtest.
- A failure message without the inputs forces the reader to rerun the test.
  Print what you passed in, what you got and what you wanted.
