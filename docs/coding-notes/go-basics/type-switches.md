# Type switches

**In one line:** a type switch runs a different branch depending on what concrete
type sits inside an interface value.

## Why Go has it

An interface value can hold any type that has the right methods. `any` (also written
`interface{}`) has no methods, so it can hold any value at all. Code that receives
such a value often needs to know what it got: a key press or a window resize, a
string or a number. A type switch asks, and hands you the value with its real type in
each branch.

## Smallest example

```go
func describe(v any) string {
	switch v := v.(type) {
	case int:
		return fmt.Sprintf("int, doubled is %d", v*2)
	case string:
		return "string of length " + strconv.Itoa(len(v))
	default:
		return fmt.Sprintf("something else: %T", v)
	}
}
```

Inside `case int`, `v` is an `int`, so `v*2` works. Inside `case string`, `v` is a
`string`. `%T` prints a value's type.

A single check uses a *type assertion* instead: `n, ok := v.(int)`. `ok` is true when
`v` holds an `int`. Leave out `ok` and a wrong guess crashes the program.

## Where Meru uses it

- `internal/tui/model.go` — `Update` receives a `tea.Msg`, which is `any`, and
  switches on it: key presses, window resizes, events from `merud`, the end of a turn,
  and spinner ticks each get their own branch.
- `internal/tui/run.go` — `final.(Model)` is a type assertion that gets our model
  back from the `tea.Model` that `p.Run` returns.

## Mistakes to avoid

- Put a `default` branch in, or know why you left it out. `Update` has none; anything
  it doesn't name falls through to the input line after the switch.
- `case A, B:` with two types gives `v` the interface type, not `A` or `B`, because Go
  can't know which one it holds.
