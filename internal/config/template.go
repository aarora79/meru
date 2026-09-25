// This file holds the config template: the commented config.toml that
// meru setup writes on first run and `meru config template` prints.

package config

import _ "embed" // the blank import turns on the //go:embed directive below

// templateText is template.toml, compiled into the binary. The //go:embed
// line is a directive to the Go compiler: at build time it reads the file
// and stores its bytes in this string, so the binaries need no data file
// next to them (docs/coding-notes/go-basics/embed.md). The string can't
// change after the build, so it isn't the mutable package state AGENTS.md
// forbids.
//
// config.example.toml at the repo root is a byte-for-byte copy, for
// people who read the repo; TestExampleIsTemplate fails when they differ.
//
//go:embed template.toml
var templateText string

// Template returns the config template. Every key Meru reads is in it.
// What is on by default is uncommented and holds its default value, so
// loading the template gives the defaults; what is off, such as the MCP
// servers in the catalog, sits in comments, ready to uncomment.
func Template() string { return templateText }
