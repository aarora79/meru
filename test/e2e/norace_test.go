//go:build e2e && !race

// This file builds when the race detector is off. It pairs with race_test.go:
// exactly one of the two defines raceBuildFlags.

package e2e

// raceBuildFlags are extra `go build` flags for the binaries under test:
// none without -race.
var raceBuildFlags []string
