// This file holds each model's context window: the num_ctx a chat call
// sends for it. merud sizes the answer model's window from what Ollama
// reports about it (FitWindow) and sets it with SetWindow; every other
// model gets [ollama] context_length. See ARCHITECTURE.md, "Context
// length".

package engine

// windowStep rounds a window down to a whole number of 1,024 tokens, so
// the logs and `ollama ps` show round numbers.
const windowStep = 1024

// FitWindow returns the window for a model Ollama described as d: as many
// tokens as the model can read, d.ContextLength, but no more than fit in
// budget bytes of cache, and never fewer than fallback, the window from
// config. A model the cache size can't be worked out for, or a fallback of
// 0, which leaves the window to Ollama, gets fallback.
//
// The budget matters: the cache takes memory for the whole window as soon
// as the model loads, whether a prompt fills it or not. qwen3.6:35b takes
// about 20 KB a token, so its full 262,144 tokens cost 5.4 GB; an older
// model of its size can take ten times that.
func FitWindow(d ModelDetails, budget int64, fallback int) int {
	if fallback <= 0 || d.ContextLength <= 0 || d.KVBytesPerToken <= 0 {
		return fallback
	}
	fits := int(budget / d.KVBytesPerToken)
	n := min(d.ContextLength, fits) / windowStep * windowStep
	// A model that can read less than the fallback still gets the
	// fallback, as it did before; Ollama trims num_ctx to the model.
	return max(n, fallback)
}

// SetWindow makes every later chat call to model ask for n tokens of
// context. Call it before the model loads, or Ollama loads it twice. n of
// 0 or less goes back to ContextLength.
func (e *OllamaEngine) SetWindow(model string, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n <= 0 {
		delete(e.windows, model)
		return
	}
	if e.windows == nil {
		e.windows = map[string]int{}
	}
	e.windows[model] = n
}

// Window returns the num_ctx a chat call to model sends: the window
// SetWindow gave it, or ContextLength.
func (e *OllamaEngine) Window(model string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n, ok := e.windows[model]; ok {
		return n
	}
	return e.ContextLength
}
