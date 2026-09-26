// This file holds the chat screen's state (Model) and Update, the method
// Bubble Tea calls to turn each message into a new state. view.go draws the
// state. Neither file touches the terminal or the socket, so tests drive them
// with plain values.

package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/aarora79/meru/internal/opener"
	"github.com/aarora79/meru/internal/rpc"
)

// The screen, top to bottom: a header line and a rule under it, the
// conversation, the input box, and one line of key help. The conversation
// gets whatever height the others leave.
const (
	headerLines = 2 // the header and the rule under it
	helpLines   = 1
	// inputChromeW and inputChromeH are the input box's width and height
	// beyond the text area: one column or row of border on each side, plus
	// one column of padding on the left and right.
	inputChromeW = 4
	inputChromeH = 2
	// maxInputLines caps how tall the input box grows as a question gains
	// lines. Past that, the text area scrolls.
	maxInputLines = 5
	// maxQueue caps how many questions wait behind the running turn. Each
	// one is a whole turn with the model, which can take a minute, so a
	// longer line of them is more likely a slip, such as a held-down Enter,
	// than a plan. Five also fit on the screen under the running turn.
	maxQueue = 5
)

// Info is what meru read from config.toml for the chat screen: what the
// header shows about merud's setup, and the [chat] settings. It can be
// empty when the file doesn't load.
type Info struct {
	Profile string // "lite" or "full"
	Model   string // the main model, such as "minicpm5:2b"
	// MouseCopy is [chat] mouse_copy: the chat captures the mouse, a
	// click on a "⧉ copy N" label copies that code block, and a click on
	// a link opens it.
	MouseCopy bool
}

// look is how the screen draws itself: a Lip Gloss renderer that knows the
// terminal's colour support, and the Glamour style for rendering answers.
type look struct {
	renderer      *lipgloss.Renderer
	markdownStyle string // a Glamour built-in style: "dark", "light" or "notty"
	// links makes each source line a clickable link to its file
	// (rpc.Hyperlink). It is on when styling is, so NO_COLOR and the
	// tests' plain-text renderer get plain lines.
	links bool
}

// turnState says where one question and its answer stand.
type turnState int

// The states of a turn. iota counts up from zero inside a const block, so
// each name gets the next number without us writing it out.
const (
	stateActive  turnState = iota // sent; waiting for the answer or streaming it
	stateDone                     // merud finished the answer
	stateStopped                  // the user pressed Ctrl-C
	stateFailed                   // merud or the connection reported an error
)

// exchange is one question and its answer, as the screen shows them.
type exchange struct {
	question string
	state    turnState

	route      string   // empty until merud's "route" event
	confidence float64  // the router's confidence in route
	fallback   bool     // the router wasn't sure and fell back
	skills     []string // the skills the turn loaded, from the "route" event

	// sources lists the excerpts from the user's files that merud put in
	// the prompt, from its "sources" event; nil when the turn didn't
	// search or found nothing.
	sources []rpc.Citation

	// tools lists the turn's tool calls in the order merud reported them.
	tools []toolCall

	answer string    // the answer's raw text, grown token by token
	err    string    // why the turn failed, for stateFailed
	stats  rpc.Event // the closing "done" event and its stats; zero if none came

	// code lists the answer's code blocks, found when the turn ends, and
	// firstBlock is the number of the first one; the rest follow on. The
	// numbers count up through the session, so /copy N names one block on
	// the whole screen. firstBlock is 0 until the turn ends.
	code       []codeBlock
	firstBlock int

	// rendered caches the finished answer as Glamour drew it, and
	// renderedWidth the width it was drawn for. A resize redraws it.
	rendered      string
	renderedWidth int
}

// toolCall is one tool call as the turn shows it: from its "tool_call"
// event, and from its "tool_result" event once that arrives.
type toolCall struct {
	id      string
	name    string // the full name, such as "notes.search"
	outcome string // "" while the call runs, then "ok", "declined" and so on
	millis  int64  // how long the call took, from "tool_result"
}

// pendingApproval is a tool call waiting for the user's answer.
type pendingApproval struct {
	ask rpc.Approval
	// reply takes the choice back to the stream goroutine, which waits on
	// it in approveVia. It has room for one value, so sending never blocks.
	reply chan rpc.Choice
	// selected is the index in ask.Choices that Enter picks; ←/→ move it.
	selected int
}

// link says whether merud answered last time we heard from it.
type link int

// The states of the link to merud, shown on the right of the header.
const (
	linkUnknown link = iota // no ping answer yet
	linkUp                  // merud answered
	linkDown                // the last attempt to reach merud failed
)

// Model is everything the chat screen knows. Bubble Tea keeps one Model and
// replaces it with whatever Update returns.
type Model struct {
	ask  askFunc // sends a request to merud
	send sender  // puts stream events back into the program
	info Info    // what the header shows
	// index is what merud's search index held at the last status check;
	// nil until one answers. The header shows its document count.
	index *rpc.IndexStatus
	// usage is merud's last answer to OpUsage; nil until one answers, and
	// when merud doesn't know the op. The header shows its 1h window.
	usage []rpc.UsageWindow

	look look
	// home is the home folder, for turning "~/..." source paths into
	// file:// links; "" leaves them unlinked.
	home  string
	style styles
	keys  keyMap
	// markdown renders finished answers. renderMarkdown builds it on first
	// use and again when the width changes; markdownWidth is the width it
	// wraps to.
	markdown      *glamour.TermRenderer
	markdownWidth int

	input        textarea.Model // where the user types
	conversation viewport.Model // the scrolling pane above the input
	spin         spinner.Model  // spins while merud thinks
	help         help.Model     // the key help at the bottom

	turns        []exchange
	width        int // terminal width, for wrapping
	height       int
	session      string // from merud's "session" event; empty until the first reply
	lastQuestion string // what Up puts back in the input
	link         link

	// streaming is true from Enter until the turn ends or the user cancels.
	streaming bool
	// queue holds the questions typed while a turn runs, oldest first.
	// When the turn ends, the next one goes to merud; merud still gets one
	// turn at a time.
	queue []string
	// turn counts questions. Events carry the turn they belong to, so events
	// from a cancelled turn can't leak into the next one.
	turn int
	// cancel stops the current turn's request. Calling it closes the socket,
	// which tells merud to stop generating.
	cancel context.CancelFunc
	// approval is the tool call waiting for the user's answer, or nil.
	// While it is set, the approval box shows and the keys answer it
	// instead of typing into the input.
	approval *pendingApproval
	// usageBox is the open /usage box, or nil. While it is set, the box
	// covers the conversation and the keys go to it.
	usageBox *usageBox
	// meBox is the open /me box, or nil, and works the same way.
	meBox *meBox
	// mcpBox is the open /mcp box, or nil, and works the same way.
	mcpBox *mcpBox
	// notice is a dim line that takes the help line's place until the next
	// key press, such as the answer to an unknown /command.
	notice string

	// copy puts a code block on the clipboard (clipboard.go). Tests swap
	// in a fake, so they never touch the real clipboard.
	copy copyFunc
	// open opens a clicked link in the browser (open.go). Tests swap in a
	// fake, so they never start a browser.
	open openFunc
	// blockCount is how many code blocks the session's finished answers
	// hold. The next block found gets number blockCount+1.
	blockCount int
}

// newModel builds the starting screen: an empty conversation and a focused
// input box, sized for an 80x24 terminal until the first resize message.
func newModel(ask askFunc, send sender, info Info, lk look) Model {
	st := newStyles(lk.renderer)
	keys := newKeyMap()

	in := textarea.New()
	in.Placeholder = "Ask Meru anything…"
	in.Prompt = ""
	in.ShowLineNumbers = false
	in.CharLimit = 0 // no limit; merud decides what is too long
	// Enter sends the question, so a new line needs another key.
	in.KeyMap.InsertNewline = keys.Newline
	// The text area's own styles would come from the default renderer, and
	// its default highlights the cursor's line. Plain styles from our
	// renderer keep it quiet and let the tests see plain text.
	plain := lk.renderer.NewStyle()
	in.FocusedStyle = textarea.Style{
		Base: plain, CursorLine: plain, CursorLineNumber: plain, EndOfBuffer: plain,
		LineNumber: plain, Placeholder: st.dim, Prompt: plain, Text: plain,
	}
	in.BlurredStyle = in.FocusedStyle
	in.Cursor.Style = plain // the cursor adds reverse video on top of this
	in.Focus()

	h := help.New()
	h.Styles = help.Styles{
		ShortKey: st.dim.Bold(true), ShortDesc: st.dim, ShortSeparator: st.dim,
		FullKey: st.dim.Bold(true), FullDesc: st.dim, FullSeparator: st.dim, Ellipsis: st.dim,
	}
	h.ShortSeparator = " · "

	m := Model{
		ask:          ask,
		send:         send,
		info:         info,
		look:         lk,
		home:         homeDir(),
		style:        st,
		keys:         keys,
		input:        in,
		conversation: viewport.New(80, 10),
		spin:         spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(st.spinner)),
		help:         h,
		copy:         systemClipboard().copy,
		open:         opener.Open,
	}
	// The viewport's own keys would scroll on j, k, space and the arrows,
	// which the user types into the input. Update scrolls it on PgUp and
	// PgDn itself.
	m.conversation.KeyMap = viewport.KeyMap{}
	m.resize(80, 24)
	return m
}

// Init returns the commands Bubble Tea runs first: blink the cursor, ask
// merud what its index holds, so the header can say whether it is up and
// how many documents it searches, and ask for the usage numbers.
func (m Model) Init() tea.Cmd {
	if m.ask == nil {
		return textarea.Blink
	}
	return tea.Batch(textarea.Blink, pingCmd(m.ask), usageCmd(m.ask), refreshAfter(refreshScanning))
}

// Update turns one message into the next Model plus an optional command for
// Bubble Tea to run.
//
// Update has a value receiver, (m Model): it works on its own copy of the
// model and returns that copy. Bubble Tea keeps the returned one. Helpers
// below use pointer receivers, (m *Model), so they can change the copy in
// place before Update returns it.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A type switch picks a branch by the concrete type stored in msg, an
	// interface, and gives msg that type inside the branch.
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		// Mouse messages come only with [chat] mouse_copy on (run.go).
		return m.handleMouse(msg)
	case copiedMsg:
		m.notice = msg.notice()
		return m, nil
	case openedMsg:
		m.notice = msg.notice()
		return m, nil
	case pingMsg:
		m.link = linkUp
		if msg.err != nil {
			m.link = linkDown
		}
		if msg.index != nil {
			nudged := m.noProfile()
			m.index = msg.index
			// The empty conversation shows the profile nudge, so it
			// redraws when the nudge comes or goes.
			if m.noProfile() != nudged {
				m.refresh()
			}
		}
		return m, nil
	case usageMsg:
		m.applyUsage(msg)
		return m, nil
	case meMsg:
		m.applyMe(msg)
		return m, nil
	case mcpMsg:
		m.applyMCP(msg)
		return m, nil
	case refreshMsg:
		// Check now, and book the next check. Only this branch books one,
		// so there is one chain of checks however many answers arrive.
		return m, tea.Batch(pingCmd(m.ask), usageCmd(m.ask), refreshAfter(nextRefresh(m.index)))
	case eventMsg:
		m.handleEvent(msg)
		return m, nil
	case approvalRequestMsg:
		m.openApproval(msg)
		return m, nil
	case turnDoneMsg:
		next := m.handleDone(msg)
		// Check merud again: the answer may have come while it indexed new
		// files, and a turn that failed may mean merud went away. The
		// answer also changed the usage numbers. next runs the queued
		// question, if one waited; tea.Batch skips a nil command.
		return m, tea.Batch(next, pingCmd(m.ask), usageCmd(m.ask))
	case spinner.TickMsg:
		// Returning no command lets the spinner stop ticking when idle.
		if !m.streaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.refresh() // the spinner is drawn inside the conversation
		return m, cmd
	}
	// Anything else, such as the cursor blink, belongs to the input box.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleKey reacts to a key press. Keys the chat screen doesn't claim go to
// the input box as typing.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = "" // a notice lasts until the next key
	// key.Matches reports whether msg is one of the binding's keys.
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.stopTurn()
		return m, tea.Quit
	case key.Matches(msg, m.keys.Stop):
		if !m.streaming {
			return m, tea.Quit
		}
		// While an answer streams, Ctrl-C stops that turn and keeps the
		// chat open, the way Ctrl-C stops a command in a shell. It drops
		// the queued questions too: a user who stops a turn wants the
		// screen back, not the next answer starting on its own.
		m.stopTurn()
		m.current().state = stateStopped
		m.numberBlocks(m.current())
		m.notice = m.dropQueue()
		m.refresh()
		return m, nil
	case m.approval != nil:
		// The approval box has the keys until the user answers it.
		m.approvalKey(msg)
		return m, nil
	case m.boxOpen():
		// So do the /usage, /me and /mcp boxes, until Esc or q closes them.
		m.boxKey(msg)
		return m, nil
	case key.Matches(msg, m.keys.Send):
		return m.submit()
	case key.Matches(msg, m.keys.Copy):
		return m.copyLast()
	case key.Matches(msg, m.keys.Recall) && m.input.Line() == 0:
		// Up on the input's first line recalls the last question. On a
		// later line it moves the cursor up, as in any editor.
		if m.lastQuestion != "" {
			m.input.SetValue(m.lastQuestion)
			m.layout()
		}
		return m, nil
	case msg.Type == tea.KeyPgUp:
		m.conversation.PageUp()
		return m, nil
	case msg.Type == tea.KeyPgDown:
		m.conversation.PageDown()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout() // a new line may have made the input box taller
	return m, cmd
}

// submit sends the typed question, unless the input is blank. While a turn
// runs, the question waits in the queue instead, and handleDone sends it
// when the turn ends. A line that starts with "/" is a command for the chat
// itself and never goes to the model; it works while an answer streams too.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if strings.HasPrefix(text, "/") {
		return m.command(text)
	}
	if text == "" {
		return m, nil
	}
	if m.streaming && len(m.queue) >= maxQueue {
		// The text stays in the input, so the user can send it later.
		m.notice = fmt.Sprintf("queue full: %d questions wait · press enter again when the next one starts", maxQueue)
		return m, nil
	}
	m.input.Reset()
	m.layout()
	m.lastQuestion = text
	m.conversation.GotoBottom() // asking a question jumps to the newest text
	if m.streaming {
		m.queue = append(m.queue, text)
		m.refresh()
		return m, nil
	}
	return m, m.startTurn(text)
}

// startTurn sends text to merud as a new turn and marks the screen busy.
// It returns the command that runs the turn and the command that starts
// the spinner; tea.Batch runs both.
func (m *Model) startTurn(text string) tea.Cmd {
	m.turn++
	m.streaming = true

	// Each turn gets its own context so Ctrl-C can cancel it without ending
	// the chat. The context lives only in the command below; the model keeps
	// the cancel function, which is all it needs.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	m.turns = append(m.turns, exchange{question: text, state: stateActive})
	m.refresh()
	req := rpc.Request{
		Op:      rpc.OpAsk,
		Session: m.session, // empty on the first question, so merud starts a session
		Text:    text,
		Source:  rpc.SourceTUI,
	}
	return tea.Batch(streamCmd(ctx, m.ask, m.send, m.turn, req), m.spin.Tick)
}

// handleEvent applies one event from merud to the current turn. It ignores
// events from any turn but the one streaming now.
func (m *Model) handleEvent(msg eventMsg) {
	if !m.streaming || msg.turn != m.turn {
		return
	}
	m.link = linkUp // an event means merud is there
	cur := m.current()
	ev := msg.ev
	switch ev.Type {
	case rpc.EventSession:
		m.session = ev.Session
	case rpc.EventRoute:
		cur.route, cur.confidence, cur.fallback = ev.Route, ev.Confidence, ev.Fallback
		cur.skills = nil
		for _, s := range ev.Skills {
			cur.skills = append(cur.skills, s.Name)
		}
	case rpc.EventSources:
		cur.sources = ev.Sources
	case rpc.EventToolCall:
		if ev.Tool != nil {
			cur.tools = append(cur.tools, toolCall{id: ev.Tool.ID, name: ev.Tool.Name})
		}
	case rpc.EventToolResult:
		if ev.Tool != nil {
			cur.finishTool(*ev.Tool)
		}
	case rpc.EventToken:
		cur.answer += ev.Text
	case rpc.EventDone:
		// The turnDoneMsg right behind this event ends the turn; keep the
		// stats for the line under the answer.
		cur.stats = ev
	case rpc.EventError:
		cur.state = stateFailed
		cur.err = ev.Error
	}
	// Unknown types are skipped, so a newer merud can't break an older
	// client.
	m.refresh()
}

// handleDone ends the current turn. A connection error shows in the turn,
// and marks merud as unreachable in the header. A turn the user already
// stopped is ignored, because Ctrl-C ended it.
//
// When questions wait in the queue, handleDone starts the oldest and
// returns the command that runs it; otherwise it returns nil. The next
// question goes even after an error, so each queued question gets its own
// answer or its own error.
func (m *Model) handleDone(msg turnDoneMsg) tea.Cmd {
	if !m.streaming || msg.turn != m.turn {
		return nil
	}
	m.stopTurn()
	cur := m.current()
	switch {
	case msg.err != nil:
		cur.state = stateFailed
		cur.err = msg.err.Error()
		m.link = linkDown
	case cur.state == stateActive:
		cur.state = stateDone
	}
	m.numberBlocks(cur)
	if len(m.queue) == 0 {
		m.refresh()
		return nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	return m.startTurn(next) // startTurn redraws the screen
}

// dropQueue empties the queue and returns a notice that says how many
// questions went, or "" when none waited.
func (m *Model) dropQueue() string {
	n := len(m.queue)
	m.queue = nil
	switch n {
	case 0:
		return ""
	case 1:
		return "dropped 1 queued question"
	}
	return fmt.Sprintf("dropped %d queued questions", n)
}

// current returns a pointer to the newest turn, so callers can change it in
// place. It is only called while a turn exists: after submit has added one.
func (m *Model) current() *exchange {
	return &m.turns[len(m.turns)-1]
}

// finishTool records how the call t ended on the tool line it started. A
// result with no matching call, which a well-behaved merud never sends, gets
// a line of its own.
func (e *exchange) finishTool(t rpc.ToolEvent) {
	// Search from the newest call back: a result most often ends the call
	// that started last.
	for i := len(e.tools) - 1; i >= 0; i-- {
		if e.tools[i].id == t.ID && e.tools[i].outcome == "" {
			e.tools[i].outcome, e.tools[i].millis = t.Outcome, t.DurationMillis
			return
		}
	}
	e.tools = append(e.tools, toolCall{id: t.ID, name: t.Name, outcome: t.Outcome, millis: t.DurationMillis})
}

// stopTurn cancels the running turn, if any, closes any open approval box,
// and marks the screen idle.
func (m *Model) stopTurn() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.closeApproval(rpc.ChoiceDeny)
	m.streaming = false
}

// resize fits every part of the screen to a new window size and re-wraps the
// conversation for the new width.
func (m *Model) resize(width, height int) {
	m.width = max(width, 1)
	m.height = max(height, 1)
	m.input.SetWidth(max(m.width-inputChromeW, 1))
	m.help.Width = m.width
	m.conversation.Width = m.width
	m.layout()
	m.refresh()
}

// layout grows or shrinks the input box to fit its text, up to
// maxInputLines, and gives the conversation the rows that are left. A
// conversation scrolled to the bottom stays at the bottom.
func (m *Model) layout() {
	follow := m.conversation.AtBottom()
	lines := min(max(m.input.LineCount(), 1), maxInputLines)
	m.input.SetHeight(lines)
	m.conversation.Height = max(m.height-headerLines-(lines+inputChromeH)-helpLines, 1)
	if follow {
		m.conversation.GotoBottom()
	}
}

// refresh redraws the conversation. If the user was already at the bottom,
// it follows the new text down; if they scrolled up to read, it leaves them
// where they are.
func (m *Model) refresh() {
	follow := m.conversation.AtBottom()
	m.conversation.SetContent(m.renderConversation())
	if follow {
		m.conversation.GotoBottom()
	}
}

// homeDir returns the user's home folder, or "" when the system can't say.
// Only the source links need it, and they fall back to plain text.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
