# goroutines

**In one line:** `go f()` runs `f` at the same time as the code that started
it; a goroutine is far cheaper than an operating-system thread.

## Why Go has it

A server handles many clients at once. In Go, you write the code for one
client as ordinary blocking code and start one goroutine per client. The Go
runtime spreads goroutines across CPU cores.

## Smallest example

```go
package main

import (
    "fmt"
    "sync"
)

func main() {
    var wg sync.WaitGroup
    for i := range 3 {
        wg.Go(func() { // starts a goroutine and counts it
            fmt.Println("worker", i)
        })
    }
    wg.Wait() // blocks until all three have returned
}
```

- `sync.WaitGroup` counts running goroutines. `wg.Go` (Go 1.25 and later)
  starts one and counts it; `wg.Wait` blocks until the count is zero.
- A **channel** (`make(chan T)`) passes values between goroutines.
  Closing a channel wakes every goroutine waiting on it.
- `select` waits on several channels and runs the first case that is ready.
- A `sync.Mutex` stops two goroutines from touching the same data at once.

## errgroup: goroutines that can fail

`golang.org/x/sync/errgroup` is a `WaitGroup` that also collects errors.
Meru uses it wherever goroutines return an error.

```go
g, gctx := errgroup.WithContext(ctx)
for i, job := range jobs {
    g.Go(func() error { // starts a goroutine
        res, err := run(gctx, job)
        out[i] = res // each goroutine writes its own slot
        return err
    })
}
if err := g.Wait(); err != nil { // waits for all; returns the first error
    return err
}
```

- `g.Wait` waits for every goroutine, even after one fails, so none is left
  running.
- `gctx` ends when the first goroutine returns an error, which tells the
  others to stop early.
- Goroutines that each write a different index of a slice need no lock.

## Where Meru uses it

- `internal/rpc/server.go` — `Serve` starts one goroutine per connection and
  waits for all of them before returning. Each connection also starts a small
  goroutine that notices the client hanging up. A mutex keeps two events from
  being written at once.
- `cmd/merud/main.go` — an errgroup runs the socket server, the startup scan
  and the folder watcher side by side.
- `internal/agent/tools.go` — `runTools` runs a round's tool calls in an
  errgroup and puts the results in call order.
- Tests use channels to signal "the handler started" and "Serve returned".

## Mistakes to avoid

- Starting a goroutine nobody waits for. Every goroutine needs an owner that
  can stop it and wait for it; otherwise it leaks.
- Sharing data without a lock. Run tests with `go test -race`, which reports
  unsafe sharing.
