// This file holds the model recommendation by memory: which models the
// Mac installer suggests for a Mac with a given amount of memory. It sits
// here, next to the profiles, so a change to the default models changes
// one Go file and the installer follows. See ARCHITECTURE.md, "Installer".

package config

import "slices"

// Recommendation is the set of models suggested for Macs with at least
// MinMemoryGB of memory.
type Recommendation struct {
	// MinMemoryGB is the least memory, in GB, this set suits.
	MinMemoryGB int
	// Profile names the profile that fills the tiers, such as "lite".
	Profile string
	// Main, when not "", overrides the profile's answer model, as
	// [models] main does in config.toml.
	Main string
	// Label and Why are for people: a short name for the set, and one
	// line on why it suits this much memory.
	Label, Why string
	// Download says about how much the models take on disk.
	Download string
}

// Recommendations returns the table, smallest Mac first. It builds a new
// slice on each call, so a caller can't change the table.
//
// The memory lines come from the benchmark in ARCHITECTURE.md, "Models we
// tried": Qwen 3.6 35B in MXFP8 peaked at about 47 GB, more than a 48 GB
// Mac lets the GPU use, so it starts at 64 GB. The 15 GB Gemma 4 build
// fits from 32 GB.
func Recommendations() []Recommendation {
	return []Recommendation{
		{
			MinMemoryGB: 0,
			Profile:     "lite",
			Label:       "Lite",
			Why:         "MiniCPM5-2B answers and routes, and nomic-embed-text reads your files. Both fit in 16 GB beside your other apps.",
			Download:    "about 2 GB",
		},
		{
			MinMemoryGB: 32,
			Profile:     "lite",
			Main:        "gemma4:26b-a4b-it-qat",
			Label:       "Lite with Gemma 4 26B for answers",
			Why:         "Gemma 4 26B writes the answers and calls tools. This 4-bit build takes about 15 GB, so it fits on 32 GB beside your other apps.",
			Download:    "about 2 GB for lite, plus about 15 GB for the answer model",
		},
		{
			MinMemoryGB: 64,
			Profile:     "lite",
			Main:        "qwen3.6:35b-a3b-mxfp8",
			Label:       "Lite with Qwen 3.6 35B for answers",
			Why:         "Qwen 3.6 35B writes the answers: it calls tools, reads pictures and thinks. Only about 3B of its parameters work on each token, so it answers fast, but it needs 64 GB.",
			Download:    "about 2 GB for lite, plus about 38 GB for the answer model",
		},
	}
}

// Recommend returns the recommendation for a Mac with memGB of memory:
// the last row whose MinMemoryGB it meets.
func Recommend(memGB int) Recommendation {
	table := Recommendations()
	best := table[0]
	for _, r := range table {
		if memGB >= r.MinMemoryGB {
			best = r
		}
	}
	return best
}

// Models returns the models r needs, each once, in the order fast, main,
// embed. It fails, returning nil, only for a profile Load doesn't know.
func (r Recommendation) Models() []string {
	m, ok := ProfileModels(r.Profile)
	if !ok {
		return nil
	}
	if r.Main != "" {
		m.Main = r.Main
	}
	var names []string
	for _, n := range []string{m.Fast, m.Main, m.Embed} {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}
