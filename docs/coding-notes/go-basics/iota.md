# iota

**In one line:** inside a `const` block, `iota` is 0 on the first line and
grows by one on each line after, so a list of named numbers writes itself.

## Why Go has it

Go has no `enum` keyword. A set of related values, such as the ways a tool may
ask before it runs, is a small named type plus a block of constants. `iota`
numbers them so you don't, and a constant you add later takes the next number.

## Smallest example

```go
package main

import "fmt"

// Confirm is a named type over int, so a Confirm can't be mixed up with
// any other int by accident.
type Confirm int

const (
	ConfirmNever  Confirm = iota // 0
	ConfirmAsk                   // 1: a line with no value repeats the one above, with iota one higher
	ConfirmAlways                // 2
)

func main() {
	var c Confirm         // the zero value
	fmt.Println(c == ConfirmNever) // true
}
```

- A line in the block with no `= ...` repeats the expression above it, so only
  the first line names the type and `iota`.
- `iota` starts at 0 again in each `const` block.
- Every type's zero value is what a variable holds before anything sets it. For
  a type numbered with `iota`, that is the first constant.

## Where Meru uses it

- `internal/dispatch/dispatch.go`: the `Confirm` levels: `ConfirmNever`,
  `ConfirmAsk`, `ConfirmAlways` and `ConfirmOutside`. The order is part of the
  meaning: `ConfirmNever` is first, so a `Confirm` nobody set never asks, and
  `ConfirmOutside` went last when it came, so the others kept their numbers.
- `cmd/meru/probe.go`: the states a tool can take in `meru mcp add`'s table:
  `toolOff`, `toolAsk`, `toolAllow`.

## Mistakes to avoid

- Putting a new constant in the middle of the block. Every constant after it
  gets a new number, which breaks any number already stored in a file or sent
  over the socket. Add new ones at the end.
- Letting the zero value mean something risky. Whatever comes first is what a
  forgotten field holds, so put the safe case there. In `dispatch` a backend
  that returns nothing returns `ConfirmNever`, and that is safe only because
  config's allow list already decided the tool may run.
- Writing the numbers into config or the protocol. Store a name, such as
  `"ask"`, and turn it into the constant when you read it.
