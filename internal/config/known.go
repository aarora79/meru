// This file lists the answer models we have tried with Meru, for the
// desktop app's Settings, Models. Each entry names the model as Ollama
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
// The list grows only when we test another model. It holds the main
// model of each profile, so Settings can always switch back to it. We
// also tried qwen3.8:27b, a dense 27B: in our benchmark it passed about
// as many tasks as the mixture-of-experts models but took about eight
// times as long per question, so it isn't offered here. See
// ARCHITECTURE.md, "Models we tried".
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
			Name:  "gemma4:26b-a4b-it-qat",
			Label: "Gemma 4 26B QAT",
			Size:  "15 GB",
			Good:  "The Gemma 4 26B mixture of experts at 4 bits, small enough for a 32 GB Mac: 120 of 150 benchmark tasks passed.",
			Bad:   "Weak across several tools (13 of 36) and slow to start: a median of 17.6 seconds to the first word.",
			// QAT: Google trained it to lose little when stored at 4 bits.
			Capabilities: []string{"completion", "vision", "tools", "thinking"},
		},
		{
			Name:  "qwen3.6:35b",
			Label: "Qwen 3.6 35B",
			Size:  "23 GB",
			Good:  "The 8-bit model's accuracy at 4 bits: 133 of 150 benchmark tasks passed, with a median of 5.7 seconds to the first word. It fits a 48 GB Mac.",
			Bad:   "About 4 seconds slower per task than the 8-bit build. Now and then it writes a tool call Ollama can't read; merud retries.",
			// A mixture of experts: about 3B of its 36B parameters work on
			// each token, which is why it runs faster than a dense 27B.
			Capabilities: []string{"completion", "vision", "tools", "thinking"},
		},
		{
			Name:  "gemma4:26b-mxfp8",
			Label: "Gemma 4 26B MXFP8",
			Size:  "28 GB",
			Good:  "The most accurate in our benchmark: 138 of 150 tasks passed, and 28 of 36 that needed several tools.",
			Bad:   "Slow to start: a median of 15.6 seconds to the first word, since Ollama reads the whole tool list again for each new question.",
			// About 4B of its 26B parameters work on each token. Ollama
			// runs the 8-bit MXFP8 weights on its MLX backend.
			Capabilities: []string{"completion", "vision", "tools", "thinking"},
		},
		{
			Name:  "qwen3.6:35b-a3b-mxfp8",
			Label: "Qwen 3.6 35B-A3B MXFP8",
			Size:  "38 GB",
			Good:  "The fastest in our benchmark: a median of 6.8 seconds a question, 4.1 to the first word, and 133 of 150 tasks passed.",
			Bad:   "It passed 22 of 36 tasks that needed several tools, fewer than gemma4:26b-mxfp8, and it needs a 64 GB Mac.",
			// The full profile's main model: the 8-bit build of the same
			// mixture of experts as qwen3.6:35b, on Ollama's MLX backend.
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
