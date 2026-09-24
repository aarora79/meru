// This file holds the Ollama version gate. The router reads log
// probabilities, which Ollama added in v0.12.11, so merud refuses to start on
// anything older (see ARCHITECTURE.md, "Model tiers").

package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MinOllamaVersion is the oldest Ollama that Meru runs on: the first release
// that reports log probabilities.
const MinOllamaVersion = "0.12.11"

// CheckVersion asks Ollama for its version and returns an error naming both
// versions when it is older than MinOllamaVersion. It also fails when Ollama
// can't be reached or reports a version it can't read.
func (e *OllamaEngine) CheckVersion(ctx context.Context) error {
	got, err := e.version(ctx)
	if err != nil {
		return err
	}
	return checkVersion(got, MinOllamaVersion)
}

// checkVersion compares two version strings and returns an error when got is
// older than want. It is split out from CheckVersion so tests can call it
// without a server.
func checkVersion(got, want string) error {
	g, err := parseVersion(got)
	if err != nil {
		return fmt.Errorf("read Ollama version: %w", err)
	}
	w, err := parseVersion(want)
	if err != nil {
		return fmt.Errorf("read minimum Ollama version: %w", err)
	}
	// Arrays of the same type compare element by element in a loop; the
	// first difference decides.
	for i := range g {
		if g[i] > w[i] {
			return nil
		}
		if g[i] < w[i] {
			return fmt.Errorf("found Ollama %s, but Meru needs %s or newer (the first release with log probabilities); upgrade Ollama", got, want)
		}
	}
	return nil
}

// parseVersion turns "0.12.11", "v0.34.0" or "0.13.0-rc1" into three
// numbers. It drops a leading "v" and anything after "-" or "+", and treats
// a missing minor or patch number as zero. It fails on anything else.
//
// [3]int is an array: a fixed-length list whose size is part of its type.
func parseVersion(s string) ([3]int, error) {
	var out [3]int
	v := strings.TrimPrefix(strings.TrimSpace(s), "v")
	// strings.Cut splits at the first "-" and reports whether it found one;
	// we only keep the part before it.
	v, _, _ = strings.Cut(v, "-")
	v, _, _ = strings.Cut(v, "+")
	parts := strings.Split(v, ".")
	if v == "" || len(parts) > 3 {
		return out, fmt.Errorf("version %q is not in the form X.Y.Z", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, fmt.Errorf("version %q is not in the form X.Y.Z", s)
		}
		out[i] = n
	}
	return out, nil
}
