// This file holds what one-shot `meru "..."` shows about tools: the prompt
// that asks whether a tool call may run, and the dim lines that report each
// call as it starts and ends. Both write to stderr, so stdout holds only the
// answer and `meru "..." > answer.txt` stays clean. See ARCHITECTURE.md,
// "Approving a tool call".

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// maxArgLines caps how many lines of a call's arguments the prompt shows.
// Twenty lines fit on most terminals with the question still in view.
const maxArgLines = 20

// toolArgsWidth caps the arguments on a "→" line, so each call takes one
// line of an 80-column terminal or not much more.
const toolArgsWidth = 60

// prompter asks the user about tool calls on the terminal. Its approve
// method has the shape of rpc.ApproveFunc, so ask can hand it to rpc.Do.
//
// It reads answers from standard input, and only when standard input is a
// terminal. We don't open /dev/tty instead: Windows has no such file, and a
// script that pipes text into meru should get a deny, not a prompt it can't
// see.
type prompter struct {
	in       *bufio.Reader
	out      io.Writer // where the prompt goes: stderr
	terminal bool      // whether in is a terminal someone can type at
	look     look
}

// newPrompter returns a prompter that reads from in and writes to out.
// terminal says whether in is a terminal; tests pass a scripted reader and
// choose it themselves.
func newPrompter(in io.Reader, out io.Writer, terminal bool) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out, terminal: terminal, look: newLook(out)}
}

// isTerminal reports whether f is a terminal. os.ModeCharDevice marks a
// character device, which is what a terminal is. /dev/null is one too, so
// `meru "..." < /dev/null` still prompts; it reads end-of-file at once, and
// end-of-file counts as deny.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// approve shows the tool call a and asks the user to pick one of
// a.Choices. Empty or unknown input asks again; end-of-file denies. When
// standard input isn't a terminal, it denies without asking and says so in
// one line. It fails only when ctx ends, as it does on Ctrl-C.
func (p *prompter) approve(ctx context.Context, a rpc.Approval) (rpc.Choice, error) {
	if !p.terminal {
		fmt.Fprintf(p.out, "Denied %s: standard input isn't a terminal, so nobody could approve it.\n", a.Name)
		return rpc.ChoiceDeny, nil
	}
	if len(a.Choices) == 0 {
		fmt.Fprintf(p.out, "Denied %s: merud offered no choices.\n", a.Name)
		return rpc.ChoiceDeny, nil
	}

	fmt.Fprintf(p.out, "Meru wants to run %s (%s)", p.look.bold.Render(a.Name), a.Kind)
	if lines := rpc.ArgsLines(a.Args, maxArgLines); len(lines) > 0 {
		fmt.Fprintln(p.out, " with:")
		for _, l := range lines {
			fmt.Fprintln(p.out, "  "+l)
		}
	} else {
		fmt.Fprintln(p.out, " with no arguments.")
	}

	question := fmt.Sprintf("Run %s? %s: ", a.Name, choiceMenu(a.Choices))
	for {
		fmt.Fprint(p.out, question)
		line, err := p.readLine(ctx)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if c, ok := pickChoice(line, a.Choices); ok {
			return c, nil
		}
		if err != nil {
			// End of input, or a read that failed: nobody can answer.
			fmt.Fprintln(p.out)
			fmt.Fprintf(p.out, "Denied %s: no answer.\n", a.Name)
			return rpc.ChoiceDeny, nil
		}
	}
}

// readLine reads one line of input, or gives up when ctx ends.
//
// A read from a terminal blocks until the user presses Enter, and nothing
// can interrupt it, so it runs in its own goroutine and hands the result
// back on a channel. select then waits for whichever comes first: the line
// or the end of ctx. If ctx wins, the goroutine stays blocked until the next
// line or end-of-file; meru exits right after a cancelled turn, which ends
// it. The channel has room for one value, so the goroutine never waits to
// hand it over.
func (p *prompter) readLine(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := p.in.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// choiceKey returns the key that picks c: its first letter, "o", "s" or
// "d".
func choiceKey(c rpc.Choice) string {
	if c == "" {
		return ""
	}
	return string(c)[:1]
}

// choiceMenu writes the offered choices for the prompt with each key in
// brackets, such as "[o]nce  [s]ession  [d]eny".
func choiceMenu(choices []rpc.Choice) string {
	labels := make([]string, 0, len(choices))
	for _, c := range choices {
		k := choiceKey(c)
		labels = append(labels, "["+k+"]"+strings.TrimPrefix(string(c), k))
	}
	return strings.Join(labels, "  ")
}

// pickChoice matches what the user typed with one of choices: its key
// ("o") or its whole word ("once"), in any case. ok is false for empty or
// unknown input, and for a choice merud didn't offer.
func pickChoice(input string, choices []rpc.Choice) (rpc.Choice, bool) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", false
	}
	for _, c := range choices {
		if input == string(c) || input == choiceKey(c) {
			return c, true
		}
	}
	return "", false
}

// toolLine writes a "tool_call" or "tool_result" event as one line for
// stderr:
//
//	→ notes.search {"query":"garden"}
//	✓ notes.search 120 ms
//	✗ mail.send declined
//
// It returns "" for an event without a Tool.
func toolLine(ev rpc.Event) string {
	t := ev.Tool
	if t == nil {
		return ""
	}
	if ev.Type == rpc.EventToolCall {
		return strings.TrimSpace("→ " + t.Name + " " + rpc.ArgsLine(t.Args, toolArgsWidth))
	}
	if t.Outcome == "ok" {
		return "✓ " + t.Name + " " + millis(t.DurationMillis)
	}
	return "✗ " + t.Name + " " + t.Outcome
}

// millis writes a duration in milliseconds the way a person reads it:
// "120 ms" under a second, "1.2s" from there up.
func millis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
