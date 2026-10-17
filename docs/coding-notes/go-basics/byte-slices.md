# Byte slices

**In one line:** a `[]byte` holds raw bytes, such as a file's contents, and
`encoding/json` writes one as a base64 string.

## Why Go has it

A `string` in Go can hold any bytes, but it can't change once made, and code
reads it as text. A `[]byte` is a slice of bytes you can grow, cut and pass
around without copying: the slice is a small header that points at the bytes.
`os.ReadFile` returns one, and so does `io.ReadAll`. A `[][]byte` is a slice of
slices: a list of byte buffers, such as several pictures.

JSON has no type for raw bytes. So when `json.Marshal` meets a `[]byte`, it
writes the bytes as one base64 string, and `json.Unmarshal` turns that string
back into bytes. Ollama's API wants each picture as a base64 string in an
`images` list, which is what a `[][]byte` field marshals to, so Meru never calls
the base64 package itself.

## Smallest example

```go
package main

import (
    "encoding/json"
    "fmt"
)

type message struct {
    Role   string   `json:"role"`
    Images [][]byte `json:"images,omitempty"`
}

func main() {
    b, _ := json.Marshal(message{Role: "tool", Images: [][]byte{{0x89, 'P', 'N', 'G'}}})
    fmt.Println(string(b)) // {"role":"tool","images":["iVBORw=="]}
}
```

## Where Meru uses it

- `internal/engine/engine.go` — `Message.Images` holds pictures for the model;
  the wire code marshals them to base64 for Ollama.
- `internal/dispatch/dispatch.go` — `Result.Images` carries a picture from
  `view_image` to the agent beside the result's text.
- `internal/builtin/searchimages.go` — `readImageFile` reads a picture into a
  `[]byte` through `io.ReadAll(io.LimitReader(f, imageCap+1))`, which stops one
  byte past the cap, so a file that grew past it shows.

## Mistakes to avoid

- **Turning bytes into a string to log them.** `string(b)` on a picture makes
  an unreadable string, and in JSON it becomes a long base64 blob. Log the
  length, `len(b)`, not the bytes. `dispatch` never logs `Result.Images` for
  this reason.
- **Keeping a small slice of a big buffer.** `b[:10]` still points at all of
  `b`, so the whole buffer stays in memory. Copy the part you keep with
  `bytes.Clone` when the rest can go.
