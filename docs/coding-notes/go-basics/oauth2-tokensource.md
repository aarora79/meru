# oauth2.TokenSource

**In one line:** `golang.org/x/oauth2` hands out access tokens through
`TokenSource`, an interface with one method, `Token`, and a type that wraps one
can add behavior without changing the package.

## Why Go has it

An OAuth access token expires, often after an hour. Code that sends requests
shouldn't track the expiry or know how to refresh. `TokenSource` puts both behind
one call: each `Token()` returns a token that still works, and the package's own
source trades the refresh token for a new one once the old one has expired. The
`oauth2` module comes from the Go team but sits outside the standard library; the
`x` in `golang.org/x` marks such modules.

## Smallest example

```go
// The interface, as the package declares it:
// type TokenSource interface { Token() (*oauth2.Token, error) }

type loggingSource struct{ base oauth2.TokenSource }

func (s loggingSource) Token() (*oauth2.Token, error) {
    t, err := s.base.Token() // the base source refreshes when it must
    if err == nil {
        log.Println("token good until", t.Expiry)
    }
    return t, err
}
```

`loggingSource` has a `Token` method with the same signature, so it is a
`TokenSource` too, and any code that takes one takes it. The base source comes
from an `oauth2.Config`: `oc.TokenSource(ctx, tok)` returns a source that hands
back `tok` while it is good and refreshes it after that.

Two more pieces of the package come up beside `TokenSource`.

**The HTTP client travels in the context.** `oauth2` sends its token requests
with `http.DefaultClient` unless the context holds another client under the key
`oauth2.HTTPClient`:

```go
ctx := context.WithValue(context.Background(), oauth2.HTTPClient, myClient)
src := oc.TokenSource(ctx, tok) // every refresh goes through myClient
```

The source keeps that context and uses it for each refresh, so give it one that
never ends.

**AuthStyle says how the client proves who it is.** The token endpoint must get
the client ID and, for a client with one, the secret. `oauth2.AuthStyleInParams`
sends them as form fields in the request body, and `oauth2.AuthStyleInHeader` as
an HTTP Basic `Authorization` header. The zero value, `AuthStyleAutoDetect`,
tries both and remembers which one worked. `AuthStyle` is an `int` type, so it
saves to JSON as a number.

## Where Meru uses it

- `internal/mcp/oauth.go`: `savingSource` wraps the source from
  `oauth2.Config.TokenSource`. Its `Token` writes each new access token to
  `~/.meru/oauth/<server>.json`, and turns a refresh the server refuses into
  `errSignInNeeded`, so the server shows "sign in needed".
- `internal/mcp/oauth.go`: `ctxWithClient` puts the sign-in HTTP client in the
  context under `oauth2.HTTPClient`, so a refresh after a restart keeps the
  https-only and private-address rules.
- `internal/mcp/oauth.go`: `storedSignIn` keeps the token URL and `AuthStyle`
  beside the token, so merud can rebuild the `oauth2.Config` after a restart
  with no new sign-in.

## Mistakes to avoid

- **A request's context for the source.** The source keeps the context it got.
  Once that request ends, every later refresh fails with `context.Canceled`.
- **A wrapper with state and no lock.** The transport calls `Token` from each
  request's goroutine, so two calls can run at once. `savingSource` guards the
  last token it saved with its own `sync.Mutex`.
- **Saving nothing after a refresh.** The new token then lives only in memory.
  After a restart the old refresh token comes back from disk, and a server that
  hands out a new refresh token on each refresh may have retired it already.
