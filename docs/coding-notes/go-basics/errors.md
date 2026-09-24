# errors

**In one line:** a Go function that can fail returns an `error` as its last
result, and the caller checks it on the next line.

## Why Go has it

Go has no exceptions for ordinary failures. A failure is a value the function
returns, so you see every place that can fail by reading the code. `nil` means
"no error".

## Smallest example

```go
package main

import (
    "errors"
    "fmt"
    "io/fs"
    "os"
)

func readConfig(path string) ([]byte, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read config %s: %w", path, err)
    }
    return b, nil
}

func main() {
    _, err := readConfig("/no/such/file")
    fmt.Println(err)                            // read config /no/such/file: open ...: no such file or directory
    fmt.Println(errors.Is(err, fs.ErrNotExist)) // true
}
```

- `b, err :=` receives both results. `:=` declares new variables and infers
  their types.
- `fmt.Errorf` with `%w` **wraps** the error: the message gains context, and
  the original error stays inside.
- `errors.Is` looks through the wrapping to find a known error.
  `errors.Join` combines several errors into one.

## Where Meru uses it

- `internal/config/load.go` — `errors.Is(err, fs.ErrNotExist)` turns a missing
  config file into "use the defaults"; `errors.Join` reports every bad key at
  once.
- `internal/transcript/transcript.go` — `errors.Is(err, fs.ErrExist)` spots a
  session name clash.
- `internal/agent/agent.go` — `errors.Is(err, context.Canceled)` marks a turn
  `cancelled` rather than `error`.
- `cmd/merud/main.go` — errors travel up to `main`, which prints them once and
  exits with status 1.

## Mistakes to avoid

- Comparing error text with `==` or `strings.Contains` in real code. The text
  changes; use `errors.Is` or `errors.As`. (Tests may check text, since the
  text is what the user reads.)
- Handling an error twice: logging it and also returning it. Pick one.
