# Channels

**In one line:** a channel is a typed pipe that one goroutine writes values into and
another reads them out of.

## Why Go has it

Go programs run many goroutines (functions running at the same time). They need a
safe way to hand data to each other without two of them writing the same variable at
once. A channel does the handing over and the waiting: a read waits until a value
arrives, and a write waits until there is room.

## Smallest example

```go
package main

import "fmt"

func main() {
	ch := make(chan string, 1) // a channel of strings with room for one value
	go func() { ch <- "hello" }() // a goroutine writes into it
	fmt.Println(<-ch)             // main waits for the value, then prints it
}
```

`make(chan T, n)` creates a channel with room for `n` values before a write has to
wait. With `n` left out, every write waits for a reader. `ch <- v` writes, `<-ch`
reads.

`select` waits on several channels at once and runs the first case that is ready. A
`default` case runs when none is ready, so the `select` never waits:

```go
select {
case msg := <-ch:
	fmt.Println(msg)
default:
	fmt.Println("nothing yet")
}
```

## Where Meru uses it

- `internal/tui/model_test.go` — the fake sender pushes each stream event onto a
  channel; `drain` reads until the channel is empty, using `select` with `default`.
  `TestCancelMidStream` runs a turn in its own goroutine and gets its result back on
  a channel, with `time.After` as a timeout.
- `internal/tui/run.go` — Bubble Tea keeps its message queue in a channel.
  `program.Send` writes to it, which is how stream events from a goroutine reach
  `Update`.

## Mistakes to avoid

- Only the writer closes a channel, never the reader. Writing to a closed channel
  crashes the program.
- A read from a channel nobody writes to waits forever. In tests, pair the read with
  `time.After` in a `select` so a bug fails the test instead of hanging it.
