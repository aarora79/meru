# defer

**In one line:** `defer f()` runs `f` when the surrounding function returns,
however it returns.

## Why Go has it

Cleanup such as closing a file must happen on every path out of a function,
including early error returns. Writing `defer f.Close()` right after a
successful open puts the cleanup next to the thing it cleans up, and Go runs it
for you.

## Smallest example

```go
package main

import "fmt"

func work() (err error) {
    defer fmt.Println("deferred first, runs last")
    defer func() {
        fmt.Println("deferred second, runs first; err =", err)
    }()
    return fmt.Errorf("boom")
}

func main() { work() }
```

- Deferred calls run **last in, first out**.
- A deferred function can read the function's **named results** (`err` above)
  after the `return` has set them.
- Arguments to a deferred call are evaluated when `defer` runs, not later.

## Where Meru uses it

- `internal/agent/agent.go` — one deferred function records the turn's
  outcome, ends its span and writes the log line, whatever path returns.
- `internal/rpc/server.go` — `serveConn` defers `cancel` after `wg.Wait`, so
  the cancel runs first and unblocks the goroutine that `Wait` is waiting for.
- `cmd/merud/main.go` — closes the log file and flushes telemetry on exit.

## Mistakes to avoid

- Deferring `Close` on a file you wrote, and ignoring its error. `Close` can
  report a failed write; check it. `transcript.Append` does.
- Deferring inside a long loop. Each `defer` waits for the function to return,
  not the loop pass, so resources pile up.
