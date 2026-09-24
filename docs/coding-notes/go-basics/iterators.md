# Iterators

**In one line:** an iterator is a function you can use in a `for ... range` loop;
`iter.Seq2[K, V]` hands the loop two values at a time.

## Why Go has it

Before Go 1.23, a function that produced items one at a time returned a channel or
took a callback. Both were awkward: channels need a goroutine and careful cleanup,
and callbacks don't read like loops. Range-over-function iterators let the
producer run its own loop and the caller write an ordinary `for`.

## Smallest example

```go
func countTo(n int) iter.Seq2[int, error] {
    return func(yield func(int, error) bool) {
        for i := 1; i <= n; i++ {
            if !yield(i, nil) {
                return // the caller broke out of the loop
            }
        }
    }
}

// for i, err := range countTo(3) { fmt.Println(i, err) }
```

The loop body runs inside `yield`. When the caller uses `break` or `return`,
`yield` returns false and the producer must stop.

## Where Meru uses it

- `internal/engine/engine.go` — `Stream` returns `iter.Seq2[Delta, error]`: each
  step is a piece of the answer, or an error that ends the stream.
- `internal/engine/ollama.go` — the iterator reads Ollama's NDJSON lines and closes
  the HTTP body when the loop ends, however it ends.
- `internal/rpc/client.go` — `Do` returns the daemon's reply events the same way.

## Mistakes to avoid

- Ignoring `yield`'s return value. Calling `yield` again after it returned false
  panics.
- Doing the cleanup outside the iterator. Put `defer body.Close()` inside the
  function you return, so it runs when the loop ends.
