# HTTP clients

**In one line:** `net/http` sends requests with an `http.Client`, ties each request
to a `context.Context`, and hands back a response whose body you must close.

## Why Go has it

Go ships an HTTP client in the standard library, so a program can talk to a web
API without a third-party package. The context carries the deadline and the
cancel signal, so a caller can stop a slow request from outside.

## Smallest example

```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:11434/api/version", nil)
if err != nil {
    return err
}
resp, err := http.DefaultClient.Do(req)
if err != nil {
    return err // includes context.Canceled if ctx ended
}
defer resp.Body.Close() // always, or the connection leaks
if resp.StatusCode != http.StatusOK {
    return fmt.Errorf("status %d", resp.StatusCode)
}
```

## Where Meru uses it

- `internal/engine/ollama.go` — `do` sends every request to Ollama. It uses
  `NewRequestWithContext`, reads the error body on a non-2xx status, and returns
  the response for the caller to close.
- `internal/engine/ollama.go` — `NewOllama` copies the caller's client and sets
  `CheckRedirect` to refuse redirects, so no reply can send a prompt to another
  host.
- `internal/engine/ollama_test.go` — `httptest.NewServer` starts a fake Ollama on
  a loopback port for each test.

## Mistakes to avoid

- `Do` returns no error for a 404 or 500. Check `resp.StatusCode` yourself.
- Close `resp.Body` on every path, including errors after `Do` succeeds.
- `http.Client{Timeout: ...}` caps the whole exchange, including reading the body.
  That cuts off long streams, so Meru leaves it unset and lets the context decide.
