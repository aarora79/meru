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
// screen token by token. Lip Gloss styles the screen, and Glamour renders
// each finished answer as Markdown. See ARCHITECTURE.md, "The shape" ->
// "Terminal UI".
//
// When merud asks whether a tool call may run, the same goroutine sends the
// question into the loop and waits; Update shows an approval box and sends
// the user's choice back on a channel. See ARCHITECTURE.md, "Approving a
// tool call".
//
// A line typed with a leading "/" is a command for the chat itself and never
// reaches the model. /usage opens a box with merud's usage numbers, and the
// header shows the last hour of them. /me shows what Meru knows about the
// user, and /mcp the state of each MCP server, the same table `meru mcp`
// prints (MCPTable).
//
// The package holds no model or store logic; it draws what merud sends. It
// leaves out mouse scrolling.
package tui
