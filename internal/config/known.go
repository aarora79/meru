// This file lists the answer models we have tried with Meru, for the
// desktop app's Library, Models. Each entry names the model as Ollama
// does and says what it did well and badly in our tests. See
// ARCHITECTURE.md, "Models we tried".

package config

import "slices"

// KnownModel is one model we tried as the answer model ([models] main).
// The facts come from our own runs in September 2026, on an M4 Max with
// 64 GB, with MiniCPM5-2B as the fast model; they are no promise for
// other computers.
type KnownModel struct {
	// Name is the model as Ollama names it, the name `ollama pull` takes
	// and [models] main holds, such as "gemma3:12b".
	Name string
	// Label is a short name for people, such as "Gemma 3 12B".
	Label string
	// Size is how much disk the download takes, as `ollama list` shows it.
	Size string
	// Good and Bad say in one line each what the model did well and
	// badly as the answer model.
	Good, Bad string
	// Capabilities are what Ollama's /api/show listed for the model, such
	// as "vision" and "tools". merud reports Ollama's own list for a model
	// it has pulled; this one stands in for a model not pulled yet.
	Capabilities []string
}

// KnownModels returns the models we tried as the answer model, smallest
// first. It returns a new slice each call, so a caller can't change the
// list for the next one.
//
// The list holds three models and grows only when we test another. We
// also tried qwen3.8:27b, the full profile's main model: its answers were
// the best grounded, but a question that fetched web pages took about
// five minutes, so it isn't offered here.
func KnownModels() []KnownModel {
	return []KnownModel{
		{
			Name:  "hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M",
			Label: "MiniCPM5-2B",
			Size:  "1.6 GB",
			Good:  "Fast: about 55 seconds for a question that searched the web over many rounds. Meru's router uses it.",
			Bad:   "As the answer model it made up command flags and misread what tools sent back.",
			// Ollama lists no vision for it, so it can't look at pictures.
			Capabilities: []string{"completion", "tools", "thinking"},
		},
		{
			Name:  "gemma3:12b",
			Label: "Gemma 3 12B",
			Size:  "8.1 GB",
			Good:  "12.2B parameters and a context of 131,072 tokens; it reads pictures and answers questions from what it knows.",
			Bad:   "Ollama lists no tools for it, so Meru answers without mail, notes, the web or the file tools.",
			// Ollama 0.34 lists completion and vision for it, and neither
			// tools nor thinking.
			Capabilities: []string{"completion", "vision"},
		},
		{
			Name:  "qwen3.6:35b",
			Label: "Qwen 3.6 35B",
			Size:  "23 GB",
			Good:  "About 26 seconds on the same web question, at about 78 tokens a second; it reads pictures, calls tools and thinks.",
			Bad:   "Now and then it writes a tool call Ollama can't read, or thinks and writes nothing; merud retries both.",
			// A mixture of experts: about 3B of its 36B parameters work on
			// each token, which is why it runs faster than a dense 27B.
			Capabilities: []string{"completion", "vision", "tools", "thinking"},
		},
	}
}

// FindKnownModel returns the known model called name, and whether there
// is one.
func FindKnownModel(name string) (KnownModel, bool) {
	list := KnownModels()
	// slices.IndexFunc returns the index of the first entry the function
	// accepts, or -1.
	i := slices.IndexFunc(list, func(m KnownModel) bool { return m.Name == name })
	if i < 0 {
		return KnownModel{}, false
	}
	return list[i], true
}
