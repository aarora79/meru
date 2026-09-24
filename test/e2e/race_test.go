//go:build e2e && race

// This file builds only under `go test -race`: the go command sets the
// "race" build tag itself when the race detector is on. It makes TestMain
// build the binaries with the race detector as well.

package e2e

// raceBuildFlags are extra `go build` flags for the binaries under test.
var raceBuildFlags = []string{"-race"}
