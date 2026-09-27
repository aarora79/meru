// This file holds the slash commands the app's composer understands, the
// same ones `meru chat` has, and Quit, which /exit calls. A line that
// starts with "/" runs in the page and never reaches the model.

package desktop

import "errors"

// Command is one slash command, as the composer's menu lists it.
type Command struct {
	// Name is what the user types, such as "/copy", and Args the argument
	// it takes, such as "[N]", or "".
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
	// Description says in a few words what it does.
	Description string `json:"description"`
}

// commandList is every slash command, in the order `meru chat` lists them
// (internal/tui/commands.go). TestCommandsMatchChat fails when the two
// lists differ, so a command added to one can't go missing from the other.
var commandList = []Command{
	{Name: "/new", Description: "Start a new chat; the queued questions go too"},
	{Name: "/usage", Description: "How much Meru has been used"},
	{Name: "/me", Description: "What Meru knows about you"},
	{Name: "/mcp", Description: "Your connections and their tools"},
	{Name: "/model", Args: "[name | save]", Description: "Switch to a model set, save it as the default, or show the sets"},
	{Name: "/copy", Args: "[N]", Description: "Copy code block N, or the newest answer's last block"},
	{Name: "/exit", Description: "Close Meru"},
}

// Commands returns the slash commands for the composer's menu. It returns
// a new slice each call, so the page can't change the list.
func (b *Bridge) Commands() []Command {
	return append([]Command(nil), commandList...)
}

// Quit closes the app, for /exit. The running turn stops first, as it does
// when the window closes (see ServiceShutdown).
func (b *Bridge) Quit() error {
	if b.quit == nil {
		return errors.New("this build can't close itself; close the window")
	}
	b.quit()
	return nil
}
