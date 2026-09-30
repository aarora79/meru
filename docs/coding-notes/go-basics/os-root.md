# os.Root

**In one line:** an `os.Root` is a handle on one folder whose file methods
refuse any path that would leave that folder, even through a symbolic link.

## Why Go has it

A program that writes files named by someone else, such as the entries of a
downloaded archive, must keep them inside one folder. Checking each name for
`..` and a leading `/` catches most tricks. It misses one: an earlier entry can
be a link that points outside, and a later entry writes through it. `os.Root`,
added in Go 1.24, does the check inside the operating system calls, on every
part of every path, so a name or a link that leaves the folder fails.

## Smallest example

```go
root, err := os.OpenRoot("/tmp/unpack")
if err != nil {
    return err
}
// defer runs root.Close() when the function returns.
defer root.Close()

// Fine: stays inside /tmp/unpack.
f, err := root.OpenFile("bin/node", os.O_WRONLY|os.O_CREATE, 0o750)

// Fails: climbs out.
_, err = root.OpenFile("../evil", os.O_WRONLY|os.O_CREATE, 0o600)

// Fails too, if "lib" is a link to "/etc".
_, err = root.OpenFile("lib/passwd", os.O_WRONLY|os.O_CREATE, 0o600)
```

- Names are relative to the root and use `/`.
- `Root` has most of `os`'s file functions as methods: `OpenFile`, `Mkdir`,
  `MkdirAll`, `Symlink`, `Remove`, `Stat` and more.
- A link inside the root may point elsewhere inside the root; `Root` follows it.

## Where Meru uses it

- `internal/connectors/download.go` — `unpackTarGz` writes every file, folder
  and link of the Node and uv archives through an `os.Root` on the destination
  folder. It also checks each name and link target itself, to give a clear
  `ErrUnsafeArchive`, and `os.Root` stops anything that check misses.

## Mistakes to avoid

- Trusting `Root.Symlink` to check the link's target. It creates the link with
  any target; only later use through the `Root` stays inside. A program that
  runs files from the folder afterwards goes through the ordinary `os` calls,
  so check the target before you make the link.
- Forgetting `Close`. A `Root` holds an open folder handle.
