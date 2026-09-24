# Atomic values

**In one line:** `sync/atomic` gives you a variable that many goroutines can read and
write at the same time without a lock and without corrupting it.

## Why Go has it

Goroutines run at the same time. If one goroutine writes a variable while another
reads it, the reader can see half of the write, and `go test -race` reports a data
race. A `sync.Mutex` fixes this, but every reader then waits its turn. For one value
that is written rarely and read often, an atomic type is simpler and faster: each
`Load` and `Store` happens as one step that no other goroutine can split.

## Smallest example

```go
package main

import (
    "fmt"
    "sync/atomic"
)

type config struct{ verbose bool }

func main() {
    var current atomic.Pointer[config] // zero value holds nil
    fmt.Println(current.Load() == nil)  // true

    current.Store(&config{verbose: true})
    fmt.Println(current.Load().verbose) // true
}
```

`atomic.Pointer[config]` is a generic type: the part in square brackets says what the
pointer points to. Its zero value is ready to use and holds nil, so it needs no
constructor.

## Where Meru uses it

- `internal/obs/obs.go` — `current` holds the live metric instruments and the
  `capture_content` flag. `Setup` stores a new value once; every `Record…` call loads
  it, and a nil means "export is off, return now".

## Mistakes to avoid

- **Changing the value behind the pointer.** `current.Load().verbose = false` edits
  shared data with no protection. Build a new struct and `Store` a pointer to it.
- **Two loads for one decision.** Reading `current.Load()` twice can return two
  different values if `Store` runs in between. Load once into a local variable and use
  that.
