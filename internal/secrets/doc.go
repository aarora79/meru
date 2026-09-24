// Package secrets reads and writes ~/.meru/secrets.toml, the one file that
// holds API keys and tokens. See ARCHITECTURE.md, "Adding an MCP server":
// secrets stay out of config.
//
// The file is a flat TOML table of name = "value" pairs:
//
//	obsidian_api_key = "0f3a..."
//
// config.toml refers to an entry by writing "secret:<name>" as the value of
// an env variable or an HTTP header. merud calls Resolve on those values when
// it starts a server, and Redact on text it writes to transcripts, logs and
// spans, so a key that comes back in a tool result never lands on disk.
//
// Load refuses a file that other users could read, because a key that
// leaked once can't be taken back. Set writes through a temporary file and
// a rename, so a crash never leaves half a file.
//
// What it doesn't do: it doesn't encrypt the file or talk to the system
// keychain. A file readable only by you matches how ssh keeps its keys, and
// works the same on every platform Meru runs on.
package secrets
