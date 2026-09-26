// Package engine is Meru's only doorway to a model runtime.
//
// It defines the four-method Engine interface (see ARCHITECTURE.md, "Engine
// layer") and OllamaEngine, which talks to a local Ollama over HTTP on
// loopback. The rest of Meru calls the interface and never sees Ollama's own
// JSON. Images ride on a Message; OllamaEngine.Capabilities, outside the
// interface, says whether a model can look at them.
//
// What this package deliberately doesn't do: it holds no cloud-model client,
// makes no decision about which model to use (that's config and the router),
// and never talks to anything that isn't on this machine.
package engine
