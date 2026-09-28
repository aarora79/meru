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
	{Name: "/incognito", Description: "Start an incognito chat: Meru keeps no record of it"},
	{Name: "/chats", Args: "[words]", Description: "Find a past chat in the sidebar"},
	{Name: "/delete", Description: "Delete this chat for good, after you confirm"},
	{Name: "/folder", Args: "[new <name> | rename <old> -> <new> | delete <name>]", Description: "List, add, rename or delete chat folders"},
	{Name: "/move", Args: "[folder]", Description: "Move this chat to a folder; alone, out of its folder"},
	{Name: "/tag", Args: "<tags>", Description: "Tag this chat"},
	{Name: "/untag", Args: "<tags>", Description: "Take tags off this chat"},
	{Name: "/retry", Description: "Ask the newest question again"},
	{Name: "/scope", Args: "<auto | files | mail | web | talk>", Description: "Set where Meru looks"},
	{Name: "/attach", Description: "Attach files or images"},
	{Name: "/save", Args: "[chat]", Description: "Save the newest answer as a note, or share the chat as a file"},
	{Name: "/used", Description: "What the newest answer used"},
	{Name: "/copy", Args: "[N | answer]", Description: "Copy code block N, the newest answer's last block, or the whole answer"},
	{Name: "/usage", Description: "How much Meru has been used"},
	{Name: "/me", Description: "What Meru knows about you"},
	{Name: "/mcp", Description: "Your connections and their tools"},
	{Name: "/folders", Description: "The folders Meru searches"},
	{Name: "/skills", Description: "Turn skills on or off"},
	{Name: "/model", Args: "[name | save]", Description: "Switch to a model set, save it as the default, or show the sets"},
	{Name: "/log", Description: "The latest tool calls"},
	{Name: "/about", Description: "Version, license and links"},
	{Name: "/help", Description: "Every command"},
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
