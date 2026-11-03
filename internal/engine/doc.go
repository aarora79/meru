// Package engine is Meru's only doorway to a model runtime.
//
// It defines the four-method Engine interface (see ARCHITECTURE.md, "Engine
// layer") and two implementations: OllamaEngine, which talks to a local
// Ollama over HTTP on loopback, and OpenAIEngine, which talks to the answer
// models in [[endpoints]] through the OpenAI chat completions API, on this
// machine or the user's own network. Engines holds them and sends
// each call to the engine its model names ("@name" for an endpoint). The rest
// of Meru calls the interface and never sees any runtime's own JSON. Images
// ride on a Message; Capabilities, outside the interface, says whether a
// model can look at them.
//
// What this package deliberately doesn't do: it holds no provider's SDK or
// address (the user types every URL), makes no decision about which model to
// use (that's config and the router), and never sends a prompt anywhere but
// this machine or an address on the user's own network.
package engine
