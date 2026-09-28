// This file holds the slash commands the chat's input box understands. A
// line that starts with "/" runs here and never reaches the model. Each
// command that opens a box or talks to merud lives in a file of its own;
// this file names them all and sends each line to the right one.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aarora79/meru/internal/rpc"
)

// commandList names the slash commands the chat understands, in the order
// /help lists them. The desktop
// app's composer has the same commands in the same order;
// TestCommandsMatchChat in internal/desktop reads this line and fails when
// the two differ.
const commandList = "/new, /incognito, /chats, /delete, /folder, /move, /tag, /untag, /retry, /scope, /attach, /save, /used, /copy, /usage, /me, /mcp, /folders, /skills, /model, /log, /about, /help, /exit"

// commandHelp is the /help box's line for each command, in commandList's
// order: what to type, and what it does. TestCommandHelp fails when a
// command in commandList has no line here.
//
// Each line stays short enough that the two columns fit 80 columns.
var commandHelp = []struct{ use, what string }{
	{"/new", "start a new session; the queue goes"},
	{"/incognito", "a new chat Meru keeps no record of"},
	{"/chats [words]", "past chats; enter reopens one"},
	{"/delete", "delete this chat; twice to confirm"},
	{"/folder [new|rename|delete]", "chat folders: list, add, rename, delete"},
	{"/move [folder]", "move this chat; alone, out of its folder"},
	{"/tag <tags>", "tag this chat"},
	{"/untag <tags>", "take tags off this chat"},
	{"/retry", "ask the newest question again"},
	{"/scope [name]", "look in auto, files, mail, web or talk"},
	{"/attach [path]", "attach a file or image; alone, clear"},
	{"/save [chat]", "save the newest answer, or the chat"},
	{"/used", "what the newest answer used"},
	{"/copy [N|answer]", "copy a code block, or the whole answer"},
	{"/usage [by model]", "how much you use Meru"},
	{"/me [add|prefer <text>]", "what Meru knows about you; add to it"},
	{"/mcp", "tools: Off, Ask or Allow; add a server"},
	{"/folders [add <path>]", "the folders Meru searches"},
	{"/skills", "turn skills on or off"},
	{"/model [name|save]", "model sets: show, switch, save"},
	{"/log", "the latest tool calls"},
	{"/about", "version, license, links, Meru's folder"},
	{"/help", "this list"},
	{"/exit", "quit, like ctrl+d"},
}

// command runs a line that starts with "/" instead of sending it as a
// question. /new and /exit run here; every other command has a function
// in the file that holds its box.
//
// Commands that only open a box, copy text or talk to merud run at once,
// even while a turn runs; none of them waits in the queue. /retry is a
// question, so it queues like one. A model switch, a reopened chat and a
// save wait for the turn to end.
//
// Any other command leaves the text in the input, so the user can fix a
// typo, and shows one dim line that points at /help. The whole list no
// longer fits on one line of 80 columns.
func (m Model) command(text string) (tea.Model, tea.Cmd) {
	// strings.Cut splits text at the first space: the command's name
	// before it, and its argument, if any, after it.
	name, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	if name != "/delete" {
		m.deleteArmed = "" // only a second /delete in a row deletes
	}
	switch name {
	case "/new":
		m.input.Reset()
		leave := m.leaveCmd()
		m.newSession()
		m.layout()
		return m, leave
	case "/incognito":
		return m.incognitoCommand()
	case "/chats":
		return m.chatsCommand(arg)
	case "/delete":
		return m.deleteCommand()
	case "/folder":
		return m.folderCommand(arg)
	case "/move":
		return m.moveCommand(arg)
	case "/tag":
		return m.tagCommand(arg, false)
	case "/untag":
		return m.tagCommand(arg, true)
	case "/retry":
		return m.retryCommand()
	case "/scope":
		return m.scopeCommand(arg)
	case "/attach":
		return m.attachCommand(arg)
	case "/save":
		return m.saveCommand(arg)
	case "/used":
		return m.usedCommand()
	case "/copy":
		return m.copyCommand(arg)
	case "/usage":
		return m.usageCommand(arg)
	case "/me":
		return m.meCommand(arg)
	case "/mcp":
		m.openBox()
		m.mcpBox = &mcpBox{loading: true}
		return m, requestCmd(m.ask, tagConns, rpc.Request{Op: rpc.OpConnections}, rpc.EventConnections, readTimeout)
	case "/folders":
		return m.foldersCommand(arg)
	case "/skills":
		m.openBox()
		m.skillsBox = &skillsBox{loading: true}
		return m, requestCmd(m.ask, tagSkills, rpc.Request{Op: rpc.OpSkills}, rpc.EventSkills, readTimeout)
	case "/model":
		return m.modelCommand(arg)
	case "/log":
		m.openBox()
		m.logBox = &logBox{loading: true}
		return m, requestCmd(m.ask, tagLog, rpc.Request{Op: rpc.OpLog, Limit: logLimit}, rpc.EventLog, readTimeout)
	case "/about":
		m.openBox()
		m.aboutBox = &scrollBox{}
		return m, nil
	case "/help":
		m.openBox()
		m.helpBox = &scrollBox{}
		return m, nil
	case "/exit":
		m.stopTurn()
		return m, tea.Quit
	}
	m.notice = "unknown command " + name + " · /help lists the commands"
	return m, nil
}

// newSession clears the screen and forgets the session, so merud starts a
// new one with the next question. A conversation that went wrong, such as
// a model that keeps saying it knows nothing, otherwise carries on into
// every later answer, because each turn's prompt holds the ones before it.
// The old session stays on disk; only this screen lets go of it. An
// incognito chat has nothing on disk, and the caller's leaveCmd tells
// merud to forget what it holds.
//
// A streaming answer stops first. Counting up m.turn makes the events it
// may still send belong to no turn, so a late "session" event can't bring
// the old session back. Queued questions go too: the user typed them for
// the old conversation, and sending them into the new one would carry it
// on. The notice says how many went.
func (m *Model) newSession() {
	m.stopTurn()
	m.turn++
	m.turns = nil
	m.blockCount = 0 // the code blocks went with the turns
	m.session = ""
	m.incognito = false // /incognito sets it again after this
	m.deleteArmed = ""
	m.attached = nil // as the app's New chat takes the chips off
	m.notice = "new session: the next question starts fresh"
	if dropped := m.dropQueue(); dropped != "" {
		m.notice += " · " + dropped
	}
	m.refresh()
}
