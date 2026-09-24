# struct tags

**In one line:** a struct tag is a string after a field's type that tells a
library, such as `encoding/json`, how to read or write that field.

## Why Go has it

Go field names start with a capital letter to be visible outside their
package (`TokensIn`), while file formats use their own names (`tokens_in`). A
tag maps one to the other without extra code.

## Smallest example

```go
package main

import (
    "encoding/json"
    "fmt"
)

type Line struct {
    Type     string `json:"type"`
    Text     string `json:"text,omitempty"`
    TokensIn int    `json:"tokens_in,omitempty"`
    secret   string // lower case: not exported, so JSON ignores it
}

func main() {
    b, _ := json.Marshal(Line{Type: "user", Text: "hi"})
    fmt.Println(string(b)) // {"type":"user","text":"hi"}
}
```

- `json:"tokens_in"` names the key.
- `omitempty` leaves the key out when the field holds its zero value (`""`,
  `0`, `nil`).
- `toml:"-"` or `json:"-"` means "never read or write this field".

## Where Meru uses it

- `internal/config/config.go` — `toml:"..."` tags map `config.toml` keys to
  fields; `Dir` has `toml:"-"` because Load fills it in.
- `internal/transcript/transcript.go` — `json:"..."` tags give transcript lines
  the key names in ARCHITECTURE.md.
- `internal/rpc/protocol.go` — tags on `Request` and `Event` define the wire
  format.

## Mistakes to avoid

- A typo in a tag, such as `json:"tokens-in"`, compiles fine and writes the
  wrong key. `go vet` catches malformed tags, not wrong names; tests catch the
  rest.
- `omitempty` on a field where zero is a real value. A count of `0` vanishes
  from the output.
