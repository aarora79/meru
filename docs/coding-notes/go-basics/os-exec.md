# os/exec

**In one line:** the `os/exec` package starts another program and connects to its
stdin, stdout and stderr.

## Why Go has it

Programs often need to run other programs. `os/exec` does it without a shell: you
give it the program and a list of arguments, and nothing in them gets expanded or
split. That avoids the quoting bugs and injection holes of building a shell command
as a string.

## Smallest example

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

cmd := exec.CommandContext(ctx, "git", "log", "--oneline", "-3")
cmd.Env = []string{"PATH=" + os.Getenv("PATH")} // only what the child needs
cmd.Stderr = os.Stderr
out, err := cmd.Output() // start, wait, and collect stdout
if err != nil {
    log.Fatal(err)
}
fmt.Print(string(out))
```

- `CommandContext` kills the child when `ctx` ends. Plain `exec.Command` has no
  such link.
- `cmd.Env` nil means "copy this process's whole environment". Set it to a list of
  `KEY=value` strings to choose what the child sees.
- `Start` launches the program and returns; `Wait` blocks until it exits and
  collects its exit status. `Run` and `Output` do both.
- `StdinPipe` and `StdoutPipe` hand back streams you read and write while the
  program runs. The MCP SDK uses them to send JSON-RPC messages.

## Where Meru uses it

- `internal/mcp/stdio.go` — starts each stdio MCP server with a trimmed
  environment and sends its stderr to merud's debug log.
- `internal/commands/run.go` — runs each declared local command with no shell,
  a short environment, output capped at 1 MiB per stream, and a timeout. On
  Unix it sets `SysProcAttr.Setpgid` and a `Cancel` function that kills the
  whole process group, and `WaitDelay` so a child that keeps the output open
  can't hang the call.
- `internal/mcp/testserver_test.go` — the test binary starts itself as a child
  that serves MCP.
- `internal/policy/layout_test.go` — runs `go list` to read the client's imports.

## Mistakes to avoid

- Leaving `cmd.Env` nil and passing on every secret in your environment.
- Forgetting `Wait`. A child that exits stays in the process table as a "zombie"
  until its parent collects it with `Wait`.
- Setting `Stderr` to something other than a file and never setting `WaitDelay`.
  Go copies that output in a goroutine, and `Wait` waits for the copy; a
  grandchild that keeps the pipe open makes `Wait` hang.
