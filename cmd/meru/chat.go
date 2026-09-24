// This file plugs the terminal UI into the meru command. The chat screen
// itself lives in internal/tui; main.go calls runChat for `meru chat`.

package main

import "github.com/aarora79/meru/internal/tui"

// init runs once when the program starts, before main. It fills in the
// runChat hook that main.go declares, so main.go needn't know about Bubble Tea.
func init() {
	runChat = tui.Run
}
