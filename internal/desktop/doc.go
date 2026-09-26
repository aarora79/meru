// Package desktop is everything in the desktop app, cmd/meru-desktop, that
// doesn't need a window: the Bridge that the page calls, the plain views it
// sends the page, and the page itself (HTML, CSS and JavaScript, embedded
// from web/): the chat, the Library of settings and the Setup screen.
//
// The app is a thin client, like `meru` and `meru chat`. The Bridge talks
// to merud over the Unix socket with rpc.Do and holds no model, store or
// tool logic: a question goes to merud, the events come back, and the
// Bridge hands each one to the page. Every tool call still runs in merud,
// through dispatch; the app only answers merud's approval questions. A
// setting changes the same way: the Bridge sends an op, and merud writes
// config.toml, secrets.toml or the memory folder. See
// ARCHITECTURE.md, "Desktop app".
//
// The package doesn't import Wails. cmd/meru-desktop binds a *Bridge to
// the window and gives it Wails' event call as its emit function, so
// everything here builds and tests without cgo or a WebView.
//
// What it leaves out on purpose: no Markdown rendering in Go (the page
// renders and sanitizes it), no clipboard code (the page uses Wails' own),
// and no state that outlives the process. The transcripts in merud are the
// record of every conversation.
package desktop
