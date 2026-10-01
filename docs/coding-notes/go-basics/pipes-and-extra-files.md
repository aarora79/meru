# Pipes and extra files

**In one line:** `os.Pipe` makes a one-way channel between two open files, and
`exec.Cmd.ExtraFiles` hands files to a child program as its file descriptors
3, 4 and up.

## Why Go has it

A program on Unix reaches each open file through a small number, its file
descriptor (fd). Every program starts with three: 0 is stdin, 1 is stdout and
2 is stderr. Some programs want more. Chrome, started with
`--remote-debugging-pipe`, reads commands from fd 3 and writes replies to
fd 4, which keeps its stdout and stderr free for logs. `os.Pipe` makes the
pipes, and `ExtraFiles` puts them at those numbers in the child.

## Smallest example

A pipe has a read end and a write end. What goes into `w` comes out of `r`:

```go
r, w, err := os.Pipe()
if err != nil {
	log.Fatal(err)
}
go func() {
	w.Write([]byte("first\x00second\x00"))
	w.Close() // the reader sees io.EOF after the last message
}()
br := bufio.NewReader(r)
for {
	msg, err := br.ReadSlice(0) // up to and including the NUL byte
	if err != nil {
		break
	}
	fmt.Println(string(msg[:len(msg)-1]))
}
```

This prints `first`, then `second`. Chrome ends each message with a NUL byte
(0x00), so `ReadSlice(0)` splits the stream into messages.

To hand pipes to a child, list them in `ExtraFiles`. The first entry becomes
the child's fd 3, the second fd 4:

```go
cmd.ExtraFiles = []*os.File{cmdR, repW} // child reads fd 3, writes fd 4
if err := cmd.Start(); err != nil { ... }
_ = cmdR.Close() // the child has its own copies now
_ = repW.Close()
```

## Why the parent closes the child's ends

`Start` gives the child its own copies of `cmdR` and `repW`. The parent's
copies stay open until it closes them. A reader sees the end of a pipe only
when every copy of the write end is closed. If the parent kept `repW` open,
its read of `repR` would wait forever after the child exited, because one
write end, its own, would still be open. The same rule works the other way:
when the parent closes `cmdW`, or dies and the system closes it, the child
reads the end of fd 3. Chrome treats that as the signal to exit, so no browser
outlives `merud`.

## Reading with ReadSlice

`bufio.Reader.ReadSlice(delim)` returns the bytes up to and including
`delim`, without a copy: the slice points into the reader's own buffer. Two
things follow:

- The next read overwrites that buffer, so copy the bytes before you read
  again. `readMessage` in `internal/render/cdp.go` writes each piece into a
  `bytes.Buffer`.
- A message longer than the buffer comes back in pieces, each with
  `bufio.ErrBufferFull`. `readMessage` keeps reading until it finds the NUL,
  and gives up past 32 MiB.

`ReadBytes` does the copy for you and grows without limit; `ReadSlice` lets
the caller set the limit.

## Why it's Unix-only

On Unix, a child inherits open files by number, so fd 3 in the parent's list
is fd 3 in the child. Windows passes handles, which have no fixed numbers, and
Go doesn't support `ExtraFiles` there. `StartPiped` checks `runtime.GOOS` and
fails with `connectors.ErrNoPipes` on Windows instead of starting a program
that would find nothing on fd 3.

## Where Meru uses it

- `internal/connectors/run.go` — `StartPiped` makes two pipes, starts
  `chrome-headless-shell` with them as fd 3 and fd 4, closes its copies of
  the child's ends, and returns a `Piped` with `merud`'s ends as `In` and
  `Out`. `Stop` closes both and waits for Chrome to exit.
- `internal/render/cdp.go` — `newConn` writes DevTools commands to `In`, each
  ending in a NUL, and a reader goroutine splits `Out` at NUL bytes with
  `readMessage`.
- `internal/connectors/run_test.go` — `pipeHelper` plays Chrome: it opens fd
  3 and fd 4 with `os.NewFile` and echoes each message back in upper case.

## Mistakes to avoid

- Keeping the parent's copy of the child's write end. The parent's reads then
  never end.
- Holding a `ReadSlice` result past the next read. Its bytes change under you.
- Forgetting that `ExtraFiles` starts at fd 3: the entry at index 0 becomes
  fd 3 in the child.
