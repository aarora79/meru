// This file holds `meru run --json "question"`, the headless mode: it sends
// one question and writes each event merud sends back to stdout as one line
// of JSON, so a script can drive merud as an agent without importing any of
// its code. See ARCHITECTURE.md, "merud as an agent harness".

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// isRunCmd reports whether args ask for the headless mode: "run" followed by
// the --json flag. Only that pair counts, so a question that starts with
// "run", such as `meru run the tests`, still goes to merud as a question.
func isRunCmd(args []string) bool {
	return len(args) >= 2 && args[0] == "run" && (args[1] == "--json" || args[1] == "-json")
}

// runCmd runs `meru run --json <question...>`. args are the words after
// "run". It writes each event of the reply to stdout as one JSON line, in
// the shape merud sent it: "session", "route", "sources", "token",
// "tool_call", "tool_result", "notice" and, last, "done" with the turn's
// timings and token counts, or "error".
//
// No one can answer an approval in this mode, so a tool call that asks
// first is denied, as it is for any client with no one to ask. The
// approval event still goes to stdout before the deny, so the script sees
// which call was refused; the call's "tool_result" follows with the outcome
// "declined".
//
// It fails when the arguments are wrong, when merud can't be reached, when
// stdout can't be written, and when merud ends the turn with an error
// event, which it has written to stdout first. run turns each failure into
// exit status 1.
func runCmd(ctx context.Context, socket string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("meru run", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // the error below says what went wrong
	asJSON := flags.Bool("json", false, "write each event as one JSON line")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	question := strings.Join(flags.Args(), " ")
	if !*asJSON || strings.TrimSpace(question) == "" {
		return errors.New(`usage: meru run --json "question"`)
	}

	// A json.Encoder writes each value followed by a newline, which makes
	// the output JSON Lines: one event per line, easy to read with jq or a
	// line-by-line loop in any language.
	enc := json.NewEncoder(stdout)
	// Tokens and tool results hold text such as "<" and "&". By default the
	// encoder writes those as < and &, which is valid JSON but
	// hard to read in a terminal.
	enc.SetEscapeHTML(false)

	// deny writes the approval event and answers deny. It has the shape of
	// rpc.ApproveFunc, so rpc.Do calls it for each approval event.
	deny := func(_ context.Context, a rpc.Approval) (rpc.Choice, error) {
		if err := enc.Encode(rpc.Event{Type: rpc.EventApproval, Approval: &a}); err != nil {
			return rpc.ChoiceDeny, err
		}
		return rpc.ChoiceDeny, nil
	}

	req := rpc.Request{Op: rpc.OpAsk, Text: question, Source: rpc.SourceCLI}
	for ev, err := range rpc.Do(ctx, socket, req, deny) {
		if err != nil {
			return err
		}
		if err := enc.Encode(ev); err != nil {
			return err // stdout closed, as with `meru run --json ... | head -1`
		}
		if ev.Type == rpc.EventError {
			return errors.New(ev.Error)
		}
	}
	return nil
}
