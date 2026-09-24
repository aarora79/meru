# Packages and imports

**In one line:** a package is a folder of Go files that share one name, and `import`
lets a file use another package's exported names.

## Why Go has it

A program needs a way to split code into parts and control what each part shows the
others. Go uses the folder as the unit: every `.go` file in `internal/obs/` starts with
`package obs`, and they all see each other's names. Another package sees only the
names that start with a capital letter, such as `obs.Setup`. Lower-case names such
as `obs.bounded` stay private to the folder.

## Smallest example

```go
package main

import (
    "fmt" // standard library

    // A renamed import: this file calls the package sdkmetric.
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func main() {
    fmt.Println(sdkmetric.NewManualReader() != nil) // true
}
```

- An import path names a package. Standard library paths are short (`fmt`, `net/url`).
  Other paths start with the module's address (`go.opentelemetry.io/otel/...`), and
  `go.mod` pins which version you get.
- You call a package's names through its last path element: `fmt.Println`.
- `sdkmetric "go.opentelemetry.io/..."` renames the import. `obs` does this because
  OTel has two packages that both end in `metric`, the API and the SDK, and a file
  can't use one name for both.
- Go refuses to compile a file that imports a package it doesn't use.

## Where Meru uses it

- `internal/obs/setup.go` — imports the OTel SDK and exporters, renamed to
  `sdkmetric`, `sdktrace` and `semconv` so each short name is unique.
- `internal/obs/obs.go` — imports `internal/config` and `internal/engine` by their
  full module path, `github.com/aarora79/meru/internal/...`.
- Every Meru package lives under `internal/`. Go lets only code inside this module
  import an `internal/` package, so no other project can depend on Meru's insides.

## Mistakes to avoid

- **Import cycles.** If package `a` imports `b`, then `b` can't import `a`; Go refuses
  to compile. Move the shared piece into a third package that both import.
- **Lower-case names across packages.** `obs.bounded` fails to compile outside `obs`.
  Capitalise a name only when another package needs it.
