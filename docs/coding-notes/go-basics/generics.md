# Generics

**In one line:** a generic type or function takes a type as a parameter, written
in square brackets, so one piece of code works for several types.

## Why Go has it

Without generics, code that does the same thing for three types gets written
three times, or takes `any` and loses the compiler's checks. Go added type
parameters in 1.18. Meru's rule is to use them only when they remove real
duplication (AGENTS.md, "Go practices").

## Smallest example

```go
// objectList is a list of T that also decodes from a string holding the list.
type objectList[T any] []T

func (l *objectList[T]) UnmarshalJSON(b []byte) error {
    var list []T // T is whatever type the field names
    if err := json.Unmarshal(b, &list); err == nil {
        *l = list
        return nil
    }
    // ...try the string form...
}

type tableArgs struct {
    Where     objectList[condition] `json:"where"`
    Aggregate objectList[aggArg]    `json:"aggregate"`
}
```

`[T any]` declares the type parameter: `T` may be any type. Each use names the
real type, as in `objectList[aggArg]`, and the compiler checks every use
against it. An `objectList[aggArg]` is still a slice, so `range`, `len` and
indexing work as on `[]aggArg`.

## Where Meru uses it

- `internal/builtin/table.go`: `objectList[T]` decodes `analyze_table`'s
  `where`, `aggregate` and `sort`, each a list of a different struct, from a
  list or from a string that holds one.

## Mistakes to avoid

- **Generics for one type.** A type parameter that only ever gets one type is
  an indirection with nothing in return; write the plain type.
- **A constraint wider than you need.** `any` suits code that only stores or
  passes values. Code that compares or adds them needs `comparable` or a
  constraint that lists the types.
