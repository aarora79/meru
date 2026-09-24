# select

**In one line:** `select` waits on several channel operations at once and runs
the case for whichever is ready first.

## Why Go has it

A goroutine often has more than one thing to wait for: new work, a timer, or a
signal to stop. Reading one channel blocks until that channel has a value, so a
goroutine that reads them one after another could sit on the first while the
second fills up. `select` waits on all of them together. When several are ready,
Go picks one at random, so no case starves the others.

## Smallest example

```go
package main

import (
	"fmt"
	"time"
)

func main() {
	events := make(chan string)
	go func() { events <- "saved notes.md" }()

	timer := time.NewTimer(time.Second)
	for {
		select {
		case e := <-events:
			fmt.Println("event:", e)
			timer.Reset(200 * time.Millisecond) // wait for things to go quiet
		case <-timer.C:
			fmt.Println("quiet; do the work now")
			return
		}
	}
}
```

This is debouncing: each event pushes the deadline back, and the work runs once
the events stop for 200 ms.

## Where Meru uses it

- `internal/index/watch.go` — `Watch` loops on a `select` over four channels:
  `ctx.Done()` to stop, fsnotify's event and error channels, and a timer that
  fires when the earliest changed path has been quiet for 500 ms.
- `internal/tui/model_test.go` — `select` with a `default` case reads a channel
  without waiting (see [channels](channels.md)).

## Mistakes to avoid

- Forgetting a `ctx.Done()` case. The loop then runs forever after its owner
  gave up on it.
- Reading a closed channel. It returns the zero value at once, every time, so
  the loop spins. Use the two-value form `v, ok := <-ch` and stop when `ok` is
  false, as `Watch` does.
- `time.After` inside a loop makes a new timer on every pass. Make one
  `time.Timer` and `Reset` it. Since Go 1.23, `Reset` is safe without draining
  the timer's channel first.
