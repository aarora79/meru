# Interfaces

**In one line:** an interface is a list of method signatures, and any type that has
those methods satisfies it, with no `implements` keyword.

## Why Go has it

Code often needs "something that can do X" without caring what the something is.
In Python you rely on duck typing and find out at run time. Go checks the same
idea at compile time: if a type has the methods, it fits, and the compiler says so
before the program runs.

## Smallest example

```go
type Speaker interface {
    Speak() string
}

type Dog struct{}

func (Dog) Speak() string { return "woof" }

func greet(s Speaker) string { return s.Speak() }

// greet(Dog{}) returns "woof"; Dog never mentions Speaker.
```

## Where Meru uses it

- `internal/engine/engine.go` — `Engine`, the four methods every model call goes
  through. `OllamaEngine` satisfies it.
- `internal/engine/ollama.go` — `var _ Engine = (*OllamaEngine)(nil)` makes the
  compiler check that `OllamaEngine` still has every method.
- `internal/engine/ollama.go` — `*APIError` has an `Error() string` method, so it
  satisfies Go's built-in `error` interface.
- `internal/router/router.go` — `Decide` takes an `engine.Engine`, so tests can
  pass a fake with a canned answer.
- `internal/router/router_test.go` — `fakeEngine` embeds `engine.Engine` in a
  struct to inherit the methods it doesn't write.

## Mistakes to avoid

- Don't define an interface before a second implementation exists. Pass the
  concrete type until then. Meru makes one planned exception, `Engine`.
- A struct that embeds a nil interface compiles, but calling a method it didn't
  write panics. Use that trick only in tests, for methods the test never calls.
