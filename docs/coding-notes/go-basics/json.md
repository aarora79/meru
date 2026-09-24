# encoding/json

**In one line:** `encoding/json` turns Go values into JSON text and back, and
`json.RawMessage` lets a piece of JSON pass through untouched.

## Why Go has it

Most of Meru's traffic is JSON: Ollama's API, the socket protocol, transcripts, and
MCP. `json.Marshal` writes a value as JSON; `json.Unmarshal` reads JSON into a value
you pass by pointer. Struct tags name the keys (see
[struct-tags.md](struct-tags.md)).

Some JSON Meru never needs to look inside. A tool's argument schema goes from the
MCP server to the model as it is. `json.RawMessage` is a `[]byte` that holds JSON
text; marshalling it writes the bytes as they are, and unmarshalling into it keeps
them without decoding.

## Smallest example

```go
type Tool struct {
    Name   string          `json:"name"`
    Schema json.RawMessage `json:"schema"` // kept as raw JSON
}

var t Tool
err := json.Unmarshal([]byte(`{"name":"add","schema":{"type":"object"}}`), &t)
// t.Schema holds the bytes {"type":"object"}

ok := json.Valid([]byte(`{"a":`)) // false: cut off halfway
out, _ := json.Marshal(t)         // {"name":"add","schema":{"type":"object"}}
```

## Where Meru uses it

- `internal/engine/engine.go` — `ToolSpec.Parameters` and `ToolCall.Arguments` are
  `json.RawMessage`, so tool schemas and arguments cross the engine untouched.
- `internal/mcp/pool.go` — marshals each tool's input schema into a
  `ToolSpec.Parameters`.
- `internal/mcp/call.go` — checks the model's arguments with `json.Valid` and
  passes them to the server as a `json.RawMessage`.

## Mistakes to avoid

- Passing a value instead of a pointer to `Unmarshal`. It needs a pointer so it can
  fill the value in.
- Forgetting that unexported (lower-case) struct fields are invisible to
  `encoding/json`. They are skipped without an error.
- Trusting a `json.RawMessage` from outside. Nothing checked it; call `json.Valid`
  before sending it on.
