// Package platform holds what differs between macOS, Linux and Windows,
// so the rest of Meru asks it instead of testing runtime.GOOS.
//
// It has two parts. For returns one system's row: its name, the programs
// that open a link and copy to the clipboard, the folders where Docker
// lands, how to install Ollama, and whether the system has Apple Notes or
// a GPU that shares main memory. Describe reads the machine merud runs on:
// the system and its version, the processor, main memory, and the GPU
// with its memory.
//
// See ARCHITECTURE.md, "Platforms". A row is plain data, so a test on any
// system checks every row; build tags would let it see only its own.
//
// What this package deliberately doesn't do: it imports only the standard
// library, so any package, client or daemon, may use it. It starts
// programs only in run.go, read-only ones with fixed arguments and no
// shell. It never installs or starts a service, and it holds no feature
// that exists on one system only: Apple Notes stays in internal/applenotes,
// and the code checks the row's AppleNotes field.
package platform
