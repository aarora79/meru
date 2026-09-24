# context

**In one line:** a `context.Context` carries a cancel signal and an optional
deadline into every call that might block, so one cancel stops all of them.

## Why Go has it

A turn in Meru reads a file, calls a model and writes to a socket. When the
user presses Ctrl-C, all of that should stop. Go's answer is to pass a
`context.Context` as the first argument of every such function. Cancelling the
context closes its `Done()` channel, and every function watching it returns.

## Smallest example

```go
package main

import (
    "context"
    "fmt"
    "time"
)

func slowWork(ctx context.Context) error {
    select {
    case <-time.After(5 * time.Second):
        return nil
    case <-ctx.Done():
        return ctx.Err() // context.DeadlineExceeded or context.Canceled
    }
}

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    fmt.Println(slowWork(ctx)) // context deadline exceeded
}
```

- `context.Background()` is the empty root context.
- `WithTimeout` and `WithCancel` return a child context and a `cancel`
  function. Always call `cancel`, usually with `defer`, to free the timer.
- Cancelling a parent cancels every child made from it.

## Where Meru uses it

- `cmd/merud/main.go` and `cmd/meru/main.go` — `signal.NotifyContext` makes a
  context that ends on Ctrl-C or `SIGTERM`.
- `internal/rpc/server.go` — each connection gets a child context, cancelled
  when the client hangs up. `context.AfterFunc` closes the listener when the
  server's context ends.
- `internal/agent/agent.go` — the turn's context reaches the router and the
  model stream. `context.WithoutCancel` keeps the trace but drops the cancel,
  so a cancelled turn still records its metric.
- `internal/config/loopback.go` — a two-second timeout on resolving
  `localhost`.

## Mistakes to avoid

- Storing a context in a struct. Pass it as the first argument instead, so
  each call gets the right one.
- Forgetting `cancel()`. `go vet` warns when a cancel function can be skipped.
