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
// Questions typed while a turn runs wait in a queue of up to five, and the
// chat sends each one when the turn before it ends. merud still gets one
// turn at a time; the queue lives here.
//
// A line typed with a leading "/" is a command for the chat itself and never
// reaches the model (commands.go names them all, and /help lists them). The
// commands give the chat what the desktop app has, through the ops the app
// sends: /chats reopens a past chat, /retry asks again, /scope sets where a
// question looks, /attach adds a file or an image, /save saves an answer or
// the chat, and /used shows what an answer used. /usage, /me, /mcp,
// /folders, /skills, /model, /log and /about open boxes over the
// conversation that match the app's Settings (box.go). Every change goes to
// merud, which writes config.toml, secrets.toml and the memory folder; this
// package writes no file of Meru's.
//
// Each code block in a finished answer gets a "⧉ copy N" label. /copy N and
// Ctrl-Y put a block on the system clipboard, and so does a click on its
// label while [chat] mouse_copy is on, its default (code.go, copy.go,
// clipboard.go).
//
// Each web or file link in a finished answer shows its URL cut to fit its
// line, and a click opens the full URL in terminals that support OSC 8
// links. With NO_COLOR the full URL shows as plain text (links.go).
//
// The package holds no model or store logic; it draws what merud sends. It
// captures the mouse only with [chat] mouse_copy on; capture takes
// click-and-drag selection away from the terminal, and false gives it back.
package tui
