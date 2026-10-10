# HTTP servers

**In one line:** `net/http` serves requests with an `http.Server`, which calls a
handler once per request, and stops cleanly with `Shutdown`.

## Why Go has it

Go ships an HTTP server in the standard library, so a program can answer web
requests without a framework. A handler is anything with a
`ServeHTTP(w, r)` method, and `http.HandlerFunc` turns a plain function into one.

## Smallest example

```go
ln, err := net.Listen("tcp", "127.0.0.1:9464") // claim the port first
if err != nil {
    return err
}
srv := &http.Server{
    Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        fmt.Fprintln(w, "hello")
    }),
    ReadHeaderTimeout: 5 * time.Second,
}
go func() {
    <-ctx.Done()
    _ = srv.Shutdown(context.Background())
}()
if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
    return err
}
```

`Serve` blocks until the server stops. After `Shutdown`, it returns
`http.ErrServerClosed`, which means the stop was asked for, not that something
broke.

## Where Meru uses it

- `cmd/merud/metrics.go` — `listenMetrics` claims the `/metrics` port, and
  `serveMetrics` runs `Serve` and the `Shutdown` that ends it as two functions
  in `merud`'s errgroup.
- `internal/obs/prometheus.go` — `MetricsHandler` is an `http.HandlerFunc` that
  checks the path, the `Host` header and the method, then writes the metrics.
- `internal/obs/prometheus_test.go` — `httptest.NewRecorder` captures what a
  handler writes, so a test needs no port.

## Mistakes to avoid

- A server with no `ReadHeaderTimeout` lets a client that sends its headers
  slowly hold a connection open for as long as it likes.
- Listen before you start anything slow, so a taken port fails at once.
- Treat `http.ErrServerClosed` from `Serve` as a clean stop, not an error.
- `w.Header().Set` must come before the first `Write`: the first write sends
  the headers.
