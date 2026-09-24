// This file holds the step that tries an MCP server before it goes into
// config.toml. merud starts the server for a moment and lists its tools
// (rpc.OpMCPProbe). meru shows them, proposes which the model may use and
// which ask first, and lets the user change that with a short edit line.
// See ARCHITECTURE.md, "Adding an MCP server".

package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/rpc"
)

// A tool's state in the proposal. iota numbers the constants 0, 1, 2.
const (
	toolOff   = iota // left out: the model doesn't see it
	toolAsk          // allowed, and each call asks first (in confirm)
	toolAllow        // allowed, and runs without asking
)

// stateLabel is how the table shows each state.
var stateLabel = [...]string{toolOff: "off", toolAsk: "ask", toolAllow: "allow"}

// editHelp is the one line of help under the table.
const editHelp = "Enter accepts. Or type changes: -name leaves a tool out, " +
	"+name allows it without asking, ?name makes it ask."

// pick is one tool in the proposal: what the server says about it, and the
// state the user will accept or change.
type pick struct {
	tool  rpc.ProbeTool
	state int
}

// probeAndPick tries the server e describes through merud, shows its tools
// and returns e with Allow and Confirm set to what the user accepts. The
// bool is false when the user cancels. When the probe fails, it says why
// and offers to try again, to write the entry anyway (with the catalog's
// lists, or an empty allow list for a server of your own), or to cancel.
// It fails only when input ends.
func (c *console) probeAndPick(ctx context.Context, socket string, e catalog.Entry) (catalog.Entry, bool, error) {
	for {
		fmt.Fprintf(c.out, "Starting %s to see what it offers. The first run of an npx or uvx server downloads it, which can take a minute.\n", e.Name)
		res, err := probe(ctx, socket, e)
		if err == nil {
			return c.pickTools(e, res)
		}
		fmt.Fprintf(c.out, "Meru couldn't start %s: %v\n", e.Name, err)
		anyway := "write it anyway, with no tools allowed"
		if len(e.Allow) > 0 {
			anyway = "write it anyway, with the catalog's tool list"
		}
		for again := false; !again; {
			a, err := c.ask("r) try again   w) " + anyway + "   c) cancel   [r/w/c]")
			if err != nil {
				return e, false, err
			}
			switch strings.ToLower(a) {
			case "r":
				again = true
			case "w":
				return e, true, nil
			case "c":
				return e, false, nil
			}
		}
	}
}

// probe asks merud to start the server e describes and list its tools.
// Env and header values go as they are, so "secret:<name>" references stay
// references and merud resolves them. It fails when merud can't be
// reached, or reports that the server didn't start or answer.
func probe(ctx context.Context, socket string, e catalog.Entry) (*rpc.ProbeResult, error) {
	req := rpc.Request{Op: rpc.OpMCPProbe, Server: &rpc.ProbeServer{
		Name:    e.Name,
		Command: e.Command,
		Args:    e.Args,
		Env:     e.Env,
		URL:     e.URL,
		Network: e.Network,
	}}
	var res *rpc.ProbeResult
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return nil, err
		}
		switch ev.Type {
		case rpc.EventProbe:
			res = ev.Probe
		case rpc.EventError:
			return nil, errors.New(ev.Error)
		}
	}
	if res == nil {
		return nil, errors.New("merud sent no list of tools")
	}
	return res, nil
}

// pickTools shows what the server offers, proposes a state for each tool,
// and loops until the user presses Enter on the table. It returns e with
// Allow and Confirm set from the accepted table, in the server's order.
func (c *console) pickTools(e catalog.Entry, res *rpc.ProbeResult) (catalog.Entry, bool, error) {
	who := strings.TrimSpace(res.ServerName + " " + res.ServerVersion)
	if who == "" {
		who = e.Name
	}
	if len(res.Tools) == 0 {
		fmt.Fprintf(c.out, "%s offers no tools, so the model gets nothing from it.\n", who)
		e.Allow, e.Confirm = nil, nil
		return e, true, nil
	}
	fmt.Fprintf(c.out, "%s offers %d tools.\n", who, len(res.Tools))
	picks := propose(e, res.Tools)
	if missing := unoffered(e.Allow, res.Tools); len(missing) > 0 {
		fmt.Fprintf(c.out, "The catalog allows %s, but this version of the server doesn't offer them; they stay out.\n", strings.Join(missing, ", "))
	}
	for {
		fmt.Fprint(c.out, pickTable(picks))
		fmt.Fprintln(c.out, editHelp)
		a, err := c.ask(">")
		if err != nil {
			return e, false, err
		}
		if a == "" {
			break
		}
		if err := applyEdits(picks, a); err != nil {
			fmt.Fprintln(c.out, err)
		}
	}
	e.Allow, e.Confirm = nil, nil
	for _, p := range picks {
		if p.state != toolOff {
			e.Allow = append(e.Allow, p.tool.Name)
		}
		if p.state == toolAsk {
			e.Confirm = append(e.Confirm, p.tool.Name)
		}
	}
	return e, true, nil
}

// propose returns a starting state for each tool the server offers.
//
// For a catalog entry, the catalog's lists win: a tool in confirm asks, a
// tool in allow runs, and a tool the catalog doesn't name stays off, since
// nobody has read it. The server's hints only show in the table.
//
// For a server of your own, the hints decide: a read-only tool runs without asking,
// and every other tool, destructive or not, is allowed but asks each time.
// A hint is the server's claim, so a tool without one counts as one that
// may change something.
func propose(e catalog.Entry, tools []rpc.ProbeTool) []pick {
	fromCatalog := len(e.Allow) > 0
	picks := make([]pick, len(tools))
	for i, t := range tools {
		state := toolAsk
		switch {
		case fromCatalog && slices.Contains(e.Confirm, t.Name):
			state = toolAsk
		case fromCatalog && slices.Contains(e.Allow, t.Name):
			state = toolAllow
		case fromCatalog:
			state = toolOff
		case isTrue(t.ReadOnly):
			state = toolAllow
		}
		picks[i] = pick{tool: t, state: state}
	}
	return picks
}

// unoffered returns the names in allow that no tool in tools has.
func unoffered(allow []string, tools []rpc.ProbeTool) []string {
	var out []string
	for _, name := range allow {
		if !slices.ContainsFunc(tools, func(t rpc.ProbeTool) bool { return t.Name == name }) {
			out = append(out, name)
		}
	}
	return out
}

// applyEdits changes picks as the edit line a says. a holds words such as
// "-send_mail ?read_mail +search"; the first character picks the state and
// the rest names the tool. It checks every word before it changes anything,
// and fails on a word it can't read or a tool the server doesn't offer.
func applyEdits(picks []pick, a string) error {
	type edit struct{ at, state int }
	var edits []edit
	for _, word := range strings.Fields(a) {
		states := map[byte]int{'-': toolOff, '+': toolAllow, '?': toolAsk}
		state, ok := states[word[0]]
		if !ok || len(word) < 2 {
			return fmt.Errorf("%q: start each change with -, + or ?, then the tool's name", word)
		}
		at := slices.IndexFunc(picks, func(p pick) bool { return p.tool.Name == word[1:] })
		if at < 0 {
			return fmt.Errorf("the server offers no tool called %q", word[1:])
		}
		edits = append(edits, edit{at, state})
	}
	for _, ed := range edits {
		picks[ed.at].state = ed.state
	}
	return nil
}

// pickTable renders the proposal as one line per tool: its state, its name,
// the start of its description and its hint.
//
//	allow  read_file    Read the complete contents of a file   read-only
//	ask    write_file   Create a new file or overwrite an ...   may delete
func pickTable(picks []pick) string {
	nameWidth, descWidth := 0, 0
	for _, p := range picks {
		nameWidth = max(nameWidth, len([]rune(p.tool.Name)))
		descWidth = max(descWidth, len([]rune(shortLine(p.tool.Description, descMax))))
	}
	var b strings.Builder
	for _, p := range picks {
		// %-5s pads the label to five columns. %-*s pads to a width given
		// as an argument: the * takes it from the argument before the text.
		fmt.Fprintf(&b, "  %-5s  %-*s  %-*s  %s\n", stateLabel[p.state], nameWidth, p.tool.Name,
			descWidth, shortLine(p.tool.Description, descMax), hint(p.tool))
	}
	return b.String()
}

// descMax is the most of a tool's description the table shows, so a row
// fits a terminal.
const descMax = 50

// hint names what the server says about a tool: "read-only" (readOnlyHint),
// "may delete" (destructiveHint), "changes things" (annotated, but neither),
// or "no hint" when the tool has no annotations. merud's MCP library reads a
// missing readOnlyHint as false, so ReadOnly is nil only for a tool with no
// annotations at all; Destructive is nil whenever the server left it out.
func hint(t rpc.ProbeTool) string {
	switch {
	case isTrue(t.ReadOnly):
		return "read-only"
	case isTrue(t.Destructive):
		return "may delete"
	case t.ReadOnly != nil:
		return "changes things"
	}
	return "no hint"
}

// isTrue reports whether a hint is set and true. A nil pointer is a hint
// the server left out.
func isTrue(b *bool) bool {
	return b != nil && *b
}

// shortLine returns the first line of s, with runs of spaces squeezed, cut to
// at most n characters with "..." at the end when it is longer.
func shortLine(s string, n int) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	r := []rune(strings.Join(strings.Fields(first), " "))
	if len(r) > n {
		return string(r[:n-3]) + "..."
	}
	return string(r)
}
