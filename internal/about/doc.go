// Package about holds what Meru says about itself: the one line that says
// what it is, its version, its license and the project's pages. Two
// clients show these, the desktop app's About section and the chat's
// /about box, so they live here once and the two can't drift apart. See
// ARCHITECTURE.md, "Desktop app" and "Terminal UI".
//
// The package imports only the standard library, so either client may use
// it without pulling in anything that talks to a model or stores data. It
// fetches nothing: the links are for a person to open, and no program of
// Meru's opens them on its own.
package about
