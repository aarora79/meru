# Continuous integration

GitHub Actions runs two workflows on every push and pull request.
`.github/workflows/ci.yml` checks that the code builds, passes its tests and
reads cleanly. `.github/workflows/security.yml` looks for vulnerabilities,
risky code and leaked secrets, and also runs every Monday so a newly published
vulnerability shows up even when nobody has pushed.

Each check has a `make` target, and CI calls those targets, so what passes on
your machine passes in CI. `make check` runs them all in CI's order. The tools
run through `go run tool@version`: Go downloads and caches each one on first
use, so you install nothing but Go. The Makefile pins every tool version at
its top.

CI builds no releases. The owner builds each one on a Mac with
`make release`, because `Meru.app` needs the Mac's own WebView;
[releasing.md](releasing.md) explains how.

## The checks

| Check | Workflow job | Catches | Run it locally |
| --- | --- | --- | --- |
| gofmt | ci / lint | Code not in standard Go layout | `make fmt-check` (`make fmt` fixes it) |
| go vet | ci / lint | Suspect code the compiler accepts: wrong `Printf` verbs, copied locks, unreachable code | `make vet` |
| staticcheck | ci / lint | Bugs, dead code, deprecated calls and simpler forms that vet misses | `make lint` |
| go mod verify, tidy | ci / lint | Downloaded modules that don't match `go.sum`; a `go.mod` with missing or unused requirements | `make tidy-check` |
| Tests with race detector | ci / test (Linux and macOS) | Failing tests, including the policy tests below; two goroutines touching the same memory without a lock | `make test` |
| Coverage | ci / test | Nothing fails on it; the job summary shows the total and the profile is kept as an artifact for 14 days | `make cover` |
| Cross-compile | ci / build | Code that builds on your OS but not on another; accidental cgo | `make build` |
| Desktop app and installer | ci / desktop (macOS) | The desktop app or the Mac installer failing to build with cgo and the WebView, or failing vet, staticcheck or govulncheck with their build tags | `make desktop installer desktop-check` (needs cgo) |
| End-to-end | ci / e2e | A real `merud` and `meru` failing together against the fake Ollama | `make e2e` |
| actionlint | ci / actionlint | Mistakes in the workflow files themselves | `make actionlint` |
| govulncheck | security / govulncheck | Known vulnerabilities in the Go toolchain or a module, reported only when Meru's code can reach them | `make vuln` |
| gosec | security / gosec | Risky patterns: unchecked errors, files opened from variable paths, weak crypto, shell commands built from input | `make sec` |
| gitleaks | security / gitleaks | API keys, tokens and passwords committed anywhere in the history | `make secrets` (working tree) or `make secrets-history` |
| CodeQL | security / codeql | Data-flow bugs such as user input reaching a file path or a command | GitHub only |
| Dependency review | security / dependency-review (pull requests) | A pull request that adds a module with a known vulnerability of moderate severity or worse | GitHub only |

The cross-compile covers darwin/arm64, darwin/amd64, linux/amd64, linux/arm64
and windows/amd64, all with `CGO_ENABLED=0`. Meru promises plain native
binaries, and a cgo dependency would break that promise on the first platform
without a C compiler.

The desktop app is the one exception, kept apart. Its window library, Wails v3,
needs cgo and the system's WebView, so `cmd/meru-desktop` builds only with the
`desktop` build tag. Every other job, and `make check`, skips it. The `desktop`
job builds it on a macOS runner with `make desktop`, then runs `make
desktop-check`: go vet, staticcheck and govulncheck with the same tags, since the
usual jobs never see its code. The rest of the app, `internal/desktop`, has no
tag and runs in the test job. Linux (WebKitGTK) and Windows (WebView2) jobs come
later. The Mac installer, `cmd/meru-installer`, is built the same way in the
same job with `make installer`, and `internal/installer` runs in the test job.

The end-to-end tests live in `test/e2e/` behind the `e2e` build tag. They build
the real `merud`, `meru` and `fakeollama` binaries and drive them as separate
processes; [docs/coding-notes/e2e.md](coding-notes/e2e.md) explains how.

gosec and CodeQL send their findings to the repository's Security tab, where
GitHub keeps them next to the lines they point at. A pull request from a fork
can't upload them, because GitHub gives forks a read-only token; the gosec job
still fails on a finding.

Code scanning and dependency review on a private repository need GitHub Advanced
Security. While the repository is private, the workflow skips the CodeQL job, the
dependency review and gosec's upload, and they come back on their own when the
repository turns public again. `govulncheck`, `gosec` and `gitleaks` still run on
every push and pull request, and `make check` runs all three locally.

## Policy tests

The package `internal/policy` holds tests and nothing else. They read Meru's
own source with Go's parser and fail `go test ./...` when the code breaks a
non-negotiable from AGENTS.md. They run in the ordinary test job, so a
violation blocks a merge like any failing test.

| Test | Fails when |
| --- | --- |
| `TestNoDeniedImports` | Any Go file imports a cloud-model SDK, or a telemetry, crash-report or self-update library |
| `TestGoModRequiresNoDeniedModule` | `go.mod` requires one of those modules, even unused |
| `TestNoProviderHostLiterals` | A string literal in shipped code names a cloud-model API host |
| `TestNoNonLoopbackURLLiterals` | A string literal in shipped code holds an `http`, `https`, `ws` or `wss` URL whose host isn't loopback |
| `TestClientImports` | A client (`cmd/meru`, `cmd/meru-desktop` or `internal/desktop`) imports a daemon-side package such as engine, transcript, agent or store, the OpenTelemetry SDK or Wails' updater, calls `obs.Setup`, or uses an `Updater`; the desktop app may not import `catalog`, `secrets` or `tui` either |
| `TestClientDependencies` | A client reaches one of those packages through another package; the message shows the import chain. The desktop app is listed with `-tags desktop,production` |
| `TestClientImports`, `TestClientDependencies` for the installer | The Mac installer (`cmd/meru-installer`, `internal/installer`) reaches the engine, the agent loop, the store, `index`, `dispatch`, the MCP or A2A clients, `builtin` or `commands` |
| `TestInstallerRunsOnlyThroughRun` | An installer file other than `internal/installer/run.go` imports `os/exec`, or any installer file imports `syscall` or calls `os.StartProcess` |
| `TestInstallerAllowlist` | The installer's allowlist names a shell, an interpreter or a downloader, or a path that isn't absolute |
| `TestConnectorsRunOnlyThroughRun` | A file in `internal/connectors` other than `run.go` imports `os/exec`, any of its files imports `syscall` or calls `os.StartProcess`, or `run.go` stops importing `os/exec` |
| `TestRenderStartsNoProgram` | A file in `internal/render`, `web_fetch`'s page reader, imports `os/exec`, `syscall` or `golang.org/x/sys`, or calls `os.StartProcess`: it starts Chrome only through `connectors.StartPiped` |

Each failure names the file and line. A few rules decide what counts:

- **Only string literals.** A link in a comment is documentation and passes.
- **Test files are skipped** for the host and URL checks, because a config test
  may need a remote URL to prove that config refuses it. Test code never ships.
  Imports are checked in test files too.
- **Loopback and reserved hosts pass:** `localhost`, `127.0.0.0/8`, `::1`,
  `example.com`, and names ending in `.example`, `.test` or `.invalid`.
  A URL built at run time, such as `"http://%s/api"`, passes, because the test
  can't know the host.
- **The deny-lists** live in `internal/policy/testdata/*.txt`, one entry per
  line with a comment. They sit in text files so that no Go string literal in
  the policy package names a provider host; the self-tests build their bad
  examples from those files at run time.

To add a module or host to a deny-list, add a line to the right file in
`testdata/`. To let a documentation link through, one that Meru prints for a
person to open but never fetches, add its prefix and a reason to
`internal/policy/allowed_urls.txt`. A reviewer has to accept each entry.

These tests catch plain mistakes. They can't catch a host assembled from
pieces at run time, so code review and the loopback checks in `config` and
`obs` stay the real defence.

## Dependabot

`.github/dependabot.yml` asks Dependabot to check Go modules and GitHub Actions
weekly and open a pull request for each update. OpenTelemetry modules come
grouped in one pull request, as do `golang.org/x` modules and all Actions. Each
pull request runs both workflows like any other.

## Permissions

Both workflows start from `contents: read`. Only the gosec and CodeQL jobs add
`security-events: write`, which they need to upload results. No job can push
code or comment on a pull request.

## When a check fails

- **gofmt:** run `make fmt` and commit.
- **tidy-check:** run `go mod tidy` and commit `go.mod` and `go.sum`.
- **govulncheck in the standard library:** raise the `toolchain` line in
  `go.mod` to the patched Go release the report names.
- **govulncheck in a module:** update the module with `go get module@version`.
- **gosec:** fix the code. If the finding is wrong, add
  `// #nosec G123 -- reason` on the line, with a reason a reviewer can check.
- **A policy test:** read the message. It names the rule, the file and the
  line. If you need a cloud model or a remote host, stop and raise it: the
  non-negotiables don't bend.
