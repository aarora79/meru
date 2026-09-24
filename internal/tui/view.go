// This file draws the chat screen: View puts the header, the conversation,
// the input box and the help line together, and the helpers below draw each
// turn. Finished answers go through Glamour, which turns Markdown into
// styled terminal text.

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aarora79/meru/internal/rpc"
)

// streamCursor ends an answer while it streams, to show more is coming.
const streamCursor = "▍"

// View draws the whole screen. Bubble Tea calls it after every Update and
// repaints what changed.
func (m Model) View() string {
	rule := m.style.rule.Render(strings.Repeat("─", m.width))
	input := m.style.inputBox.Render(m.input.View())
	// While an approval box is open, the help line lists the keys that
	// answer it instead.
	helpLine := m.help.View(m.keys)
	if m.approval != nil {
		helpLine = m.help.View(newApprovalKeys(m.approval.ask.Choices))
	}
	return strings.Join([]string{m.header(), rule, m.conversation.View(), input, helpLine}, "\n")
}

// header draws the top line: the name, the setup details (profile, main
// model, documents in the index) and the session on the left, and whether
// merud is reachable on the right. When the line is
// too narrow, the details shrink first, then disappear.
func (m Model) header() string {
	brand := m.style.brand.Render("Meru मेरु")

	var status string
	switch m.link {
	case linkUp:
		status = m.style.online.Render("● connected")
	case linkDown:
		status = m.style.offline.Render("● merud not running")
	default:
		status = m.style.dim.Render("● connecting…")
	}

	var parts []string
	for _, p := range []string{m.info.Profile, m.info.Model, docCount(m.index), shortSession(m.session)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	details := strings.Join(parts, " · ")

	// The details get what is left after the name, the status, one space
	// after the name and at least one before the status.
	room := m.width - 2 - lipgloss.Width(brand) - lipgloss.Width(status)
	if room < 6 {
		details = "" // too narrow to say anything useful
	} else {
		details = ansi.Truncate(details, room, "…")
	}
	left := brand
	if details != "" {
		left += " " + m.style.dim.Render(details)
	}
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(status), 1)
	return ansi.Truncate(left+strings.Repeat(" ", gap)+status, m.width, "")
}

// shortSession trims a session ID for the header. IDs look like
// 2026-09-23T101500-ab12, the start time plus four random hex digits. The
// date adds little on screen, so the header keeps what follows the "T".
func shortSession(id string) string {
	if id == "" {
		return ""
	}
	if _, rest, ok := strings.Cut(id, "T"); ok {
		return "session " + rest
	}
	return "session " + id
}

// renderConversation draws every turn, with a blank line before each. An
// empty conversation shows a one-line hint instead.
func (m *Model) renderConversation() string {
	if len(m.turns) == 0 {
		return "\n" + m.style.raw.Render(m.style.dim.Render("Ask a question below. The answer comes from models on this machine."))
	}
	var b strings.Builder
	for i := range m.turns {
		b.WriteString("\n")
		b.WriteString(m.renderTurn(&m.turns[i]))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderTurn draws one turn: the "You" label and the question, then the
// "Meru" label with the route badge, the answer, and closing lines that
// depend on how the turn ended: for a finished answer, the files it cites
// and its stats.
func (m *Model) renderTurn(t *exchange) string {
	// Text inside the turn wraps to the width left after the indent.
	width := max(m.width-answerIndent, 10)
	lines := []string{
		m.style.you.Render("You"),
		// The question box draws a bar and one space of padding.
		m.style.question.Render(ansi.Wrap(t.question, width-2, "")),
		"",
		m.style.meru.Render("Meru") + m.badge(t),
	}
	for _, tc := range t.tools {
		lines = append(lines, m.style.raw.Render(m.style.dim.Render(ansi.Truncate(toolText(tc), width, "…"))))
	}
	// The approval box belongs to the turn that is streaming, the newest.
	asking := m.approval != nil && t == &m.turns[len(m.turns)-1]
	if asking {
		lines = append(lines, m.approvalBoxView(width))
	}

	switch {
	case asking && t.answer == "":
		// The box says what Meru waits for; the spinner would only repeat it.
	case t.state == stateActive && t.answer == "":
		// The spinner's frames end in a space, so none goes between.
		lines = append(lines, m.style.raw.Render(m.spin.View()+m.style.dim.Render("thinking…")))
	case t.state == stateActive:
		// While the answer streams it stays raw text: Markdown half
		// written can't be rendered well, and rendering it on every token
		// would cost more than the model takes to write one.
		text := ansi.Wrap(t.answer, width-1, "")
		lines = append(lines, m.style.raw.Render(text+m.style.cursor.Render(streamCursor)))
	case t.answer != "":
		lines = append(lines, m.renderedAnswer(t))
	}

	switch t.state {
	case stateDone:
		if src := m.sourcesBlock(t, width); src != "" {
			lines = append(lines, src)
		}
		if s := statsLine(t); s != "" {
			lines = append(lines, m.style.raw.Render(m.style.dim.Render(ansi.Wrap(s, width, ""))))
		}
	case stateStopped:
		lines = append(lines, m.style.raw.Render(m.style.dim.Render("stopped")))
	case stateFailed:
		// The box's border and padding take four columns.
		msg := ansi.Wrap("error: "+t.err, max(width-4, 1), "")
		lines = append(lines, m.style.errorBox.Render(msg))
	}
	return strings.Join(lines, "\n")
}

// toolText writes one tool call as the turn shows it:
//
//	→ notes.search              while it runs
//	✓ notes.search · 120 ms     when it worked
//	✗ mail.send · declined      when it didn't
func toolText(tc toolCall) string {
	switch tc.outcome {
	case "":
		return "→ " + tc.name
	case "ok":
		return "✓ " + tc.name + " · " + millis(tc.millis)
	}
	return "✗ " + tc.name + " · " + tc.outcome
}

// millis writes a duration in milliseconds: "120 ms" under a second,
// "1.2s" from there up, as the stats line writes seconds.
func millis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return seconds(ms)
}

// sourcesBlock draws the files a finished answer cites, dim, under the
// answer: a "Sources" line, then one numbered line per file, as the answer
// numbers them. It returns "" when there are none to show (see rpc.Cited). Each line wraps
// to width, so a long path can't push the screen out of shape.
func (m *Model) sourcesBlock(t *exchange, width int) string {
	cited := rpc.Cited(t.answer, t.sources, len(t.tools) > 0)
	if len(cited) == 0 {
		return ""
	}
	lines := []string{"Sources"}
	for _, c := range cited {
		lines = append(lines, ansi.Wrap(c.String(), width, ""))
	}
	return m.style.raw.Render(m.style.dim.Render(strings.Join(lines, "\n")))
}

// badge draws the route next to the "Meru" label, such as "direct · 0.91".
// A route the router fell back to is drawn in amber and says "fallback", so
// it still stands out with colour turned off.
func (m *Model) badge(t *exchange) string {
	if t.route == "" {
		return ""
	}
	text := fmt.Sprintf("%s · %.2f", t.route, t.confidence)
	if t.fallback {
		return "  " + m.style.badgeAmber.Render(text+" · fallback")
	}
	return "  " + m.style.badge.Render(text)
}

// statsLine formats the stats merud sent with "done", such as
// "0.8s to first token · 41.3 tok/s · 2.4s". It returns "" when merud sent
// none, as an older merud does.
//
// Tokens per second divides the answer's tokens by the time the model spent
// writing them, as the model runtime measured it. Without that figure it
// falls back to the time from the first token to the end, which also counts
// the trip through merud.
func statsLine(t *exchange) string {
	s := t.stats
	var parts []string
	if s.TTFTMillis > 0 {
		parts = append(parts, fmt.Sprintf("%s to first token", seconds(s.TTFTMillis)))
	}
	writing := s.EvalMillis
	if writing <= 0 && s.TTFTMillis > 0 {
		writing = s.DurationMillis - s.TTFTMillis
	}
	if s.TokensOut > 0 && writing > 0 {
		parts = append(parts, fmt.Sprintf("%.1f tok/s", float64(s.TokensOut)/(float64(writing)/1000)))
	}
	if s.DurationMillis > 0 {
		parts = append(parts, seconds(s.DurationMillis))
	}
	return strings.Join(parts, " · ")
}

// seconds formats a count of milliseconds as seconds with one decimal.
func seconds(ms int64) string {
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// renderedAnswer returns a finished answer drawn as Markdown, rendering it
// only when the width has changed since last time. It falls back to wrapped
// plain text if Glamour isn't available or fails.
func (m *Model) renderedAnswer(t *exchange) string {
	if t.rendered != "" && t.renderedWidth == m.width {
		return t.rendered
	}
	out, err := m.renderMarkdown(t.answer)
	if err != nil {
		out = m.style.raw.Render(ansi.Wrap(t.answer, max(m.width-answerIndent, 1), ""))
	}
	t.rendered, t.renderedWidth = out, m.width
	return out
}

// renderMarkdown draws text as Markdown with Glamour, wrapped to the screen
// width: headings, bold, lists and code blocks with their syntax coloured.
//
// A glamour.TermRenderer turns Markdown into terminal text for one style and
// one wrap width. Building one parses the style, so the model keeps it and
// builds a new one only when the width changes. The colour profile comes
// from the Lip Gloss renderer, so NO_COLOR and pipes turn Glamour's colours
// off too.
func (m *Model) renderMarkdown(text string) (string, error) {
	if m.markdown == nil || m.markdownWidth != m.width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(m.look.markdownStyle),
			glamour.WithColorProfile(m.look.renderer.ColorProfile()),
			glamour.WithWordWrap(m.width),
		)
		if err != nil {
			return "", fmt.Errorf("start markdown renderer: %w", err)
		}
		m.markdown, m.markdownWidth = r, m.width
	}
	out, err := m.markdown.Render(text)
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return tidy(out), nil
}

// tidy trims what Glamour adds round its output: the blank lines above and
// below the document, and the spaces it pads each line with to the wrap
// width. A "blank" line may still hold colour codes round its spaces, so
// blankness is judged on the text with the escape codes stripped.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// docCount writes how many documents the search index holds, such as
// "68 docs" or "1 doc". It returns "" before merud has answered, so the
// header leaves the part out rather than show a wrong zero.
func docCount(ix *rpc.IndexStatus) string {
	if ix == nil {
		return ""
	}
	if ix.Documents == 1 {
		return "1 doc"
	}
	return fmt.Sprintf("%d docs", ix.Documents)
}
