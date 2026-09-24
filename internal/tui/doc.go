// Package tui is the terminal UI behind `meru chat`.
//
// It uses Bubble Tea, which splits an interactive program into three parts: a
// model struct holding what is on screen, an Update method that turns each
// incoming message (a key press, a window resize, a streamed token) into a new
// model, and a View method that draws the model as text. Bubble Tea runs the
// loop: it waits for a message, calls Update, calls View, and repaints.
//
// A question travels to merud through rpc.Do in a goroutine. Each event that
// comes back enters the loop through program.Send, so the answer grows on
// screen token by token. See ARCHITECTURE.md, "The shape" -> "Terminal UI".
//
// The package holds no model or store logic; it draws what merud sends. It
// also leaves out, for now: markdown rendering, colour beyond one dim style,
// mouse scrolling, and tool approval prompts (v0.3).
package tui
