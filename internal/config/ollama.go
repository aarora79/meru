// This file holds the oldest Ollama Meru runs on, and the one check and
// sentence merud, meru setup and the Mac installer share for it. They live
// in config because all three may import it, and none may import engine.
// See ARCHITECTURE.md, "Model tiers".

package config

import (
	"fmt"
	"strconv"
	"strings"
)

// MinOllama is the oldest Ollama Meru runs on. Ollama added the log
// probabilities the router reads in 0.12.11; 0.40.5 is the first release
// tested to serve embeddinggemma-2 and to embed images through /api/embed.
// An older Ollama refuses to pull that model with HTTP 412.
const MinOllama = "0.40.5"

// OllamaUpdate is the one place to get a newer Ollama, as every "too old"
// sentence names it.
const OllamaUpdate = "ollama.com/download"

// OllamaAtLeast reports whether version, as Ollama's /api/version gives
// it, is MinOllama or later. It fails when version isn't a dotted number.
func OllamaAtLeast(version string) (bool, error) {
	return VersionAtLeast(version, MinOllama)
}

// OllamaTooOld is the sentence every client shows for an Ollama older
// than MinOllama, such as "Ollama 0.34.0 is too old for Meru's search
// model. Update it from ollama.com/download."
func OllamaTooOld(version string) string {
	return fmt.Sprintf("Ollama %s is too old for Meru's search model. Update it from %s.", version, OllamaUpdate)
}

// VersionAtLeast reports whether version have is at least want. Both are
// dotted numbers such as "0.40.5", with an optional leading "v". Anything
// after a "-" or "+" (a pre-release or build tag) is ignored, so
// "0.40.5-rc1" counts as 0.40.5. It fails when either string isn't in that
// form.
func VersionAtLeast(have, want string) (bool, error) {
	h, err := parseVersion(have)
	if err != nil {
		return false, err
	}
	w, err := parseVersion(want)
	if err != nil {
		return false, err
	}
	for i := range h {
		if h[i] != w[i] {
			return h[i] > w[i], nil
		}
	}
	return true, nil
}

// parseVersion turns "v1.2.3-rc1" into [1 2 3]. Missing parts count as
// zero, so "0.13" is [0 13 0]. [3]int is an array: a list whose length,
// three, is part of its type.
func parseVersion(s string) ([3]int, error) {
	var v [3]int
	core := strings.TrimPrefix(strings.TrimSpace(s), "v")
	// strings.Cut splits at the first separator; the part after is dropped.
	core, _, _ = strings.Cut(core, "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return [3]int{}, fmt.Errorf("version %q isn't in the form 1.2.3", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, fmt.Errorf("version %q isn't in the form 1.2.3", s)
		}
		v[i] = n
	}
	return v, nil
}
