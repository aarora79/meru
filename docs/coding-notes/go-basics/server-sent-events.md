# Server-sent events

**In one line:** a server-sent event stream is an HTTP reply that stays open and
sends one small block of text per event, and Go reads it one line at a time with
`bufio.Scanner`.

## Why it matters here

Ollama streams an answer as NDJSON (newline-delimited JSON): one JSON object per
line. The OpenAI chat completions API streams in another format, server-sent events
(SSE), with the content type `text/event-stream`. Each event is a few lines of
`field: value`, and a blank line ends it:

```text
event: message
data: {"choices":[{"delta":{"content":"Hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: [DONE]
```

The OpenAI API sends only `data:` lines and ends with `data: [DONE]`. Some
servers also name the event's type on an `event:` line, and `readSSE` keeps it
as the event's `Type`. A line that starts
with `:` is a comment, which servers send to keep a quiet connection open.

## Smallest example

```go
sc := bufio.NewScanner(resp.Body)
var data []byte
for sc.Scan() { // one line per loop, without its "\n"
    line := sc.Bytes()
    if len(line) == 0 { // a blank line ends the event
        handle(data)
        data = nil
        continue
    }
    if v, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
        data = append(data, v...)
    }
}
if err := sc.Err(); err != nil { /* the connection broke */ }
```

`bufio.Scanner` reads from the body in chunks and hands back one line per
`Scan`. `sc.Bytes()` points into the scanner's own buffer, which the next
`Scan` reuses, so copy the bytes (as `append` does above) before you keep them.

## Where Meru uses it

- `internal/engine/endpoint.go`: `readSSE` turns a body into an
  `iter.Seq2[sseEvent, error]` (see [iterators](iterators.md)), one event per
  step, with its type and data.
- `internal/engine/openai.go`: `Stream` ranges over
  `readSSE`, decodes each event's JSON, and yields text as it comes.
- `internal/testutil/fakeollama/openai.go`: the fake writes the same format,
  flushing after each event so the client sees it at once.

## Mistakes to avoid

- **The default line limit.** A `Scanner` refuses a line over 64 KiB, and one
  event that carries a tool call's arguments can be longer.
  `sc.Buffer(make([]byte, 0, 64<<10), maxStreamLine)` raises the limit.
- **Forgetting the space.** `data: x` and `data:x` mean the same; the format
  drops one space after the colon, so trim exactly one.
- **Treating the end of the body as success.** A body that ends before
  `[DONE]` or `message_stop` means the server died or the connection dropped.
  Meru's engines report that as `io.ErrUnexpectedEOF`.
