// This file holds `meru log`: it asks merud for the latest rows of the
// tool_calls audit log and prints one line per call, newest first. merud
// reads the log; this side only formats it.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// defaultLogRows is how many calls `meru log` shows without -n.
const defaultLogRows = 20

// logArgsWidth caps the arguments at the end of each line, so a call with
// long arguments still fits on one line of a wide terminal.
const logArgsWidth = 60

// logArgvWidth caps a local command's argv instead. It is wider, because
// the argv is the audit record, and an absolute path alone can take 40
// characters.
const logArgvWidth = 160

// logCmd runs `meru log [-n N] [-v]`. args are the words after "log". -n
// sets how many calls to show and -v adds each call's result under it. It
// fails on bad usage, when merud can't be reached, or when merud replies
// with an error.
func logCmd(ctx context.Context, socket string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("meru log", flag.ContinueOnError)
	flags.SetOutput(stderr)
	n := flags.Int("n", defaultLogRows, "how many calls to show")
	verbose := flags.Bool("v", false, "show each call's result too")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 || *n < 1 {
		return errors.New("usage: meru log [-n N] [-v], with N at least 1")
	}

	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpLog, Limit: *n}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventLog:
			if err := writeLog(stdout, ev.Log, *verbose); err != nil {
				return err
			}
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	return nil
}

// writeLog prints the calls as aligned columns: local time, session, kind,
// tool, outcome, approval, duration and arguments.
//
//	2026-09-24 10:17:21  101500-ab12  mcp  notes.search  ok  session  120 ms  {"query":"garden"}
//
// The approval column shows "-" when nobody was asked, and "by meru" for a
// call merud made on its own before the model's first round, such as the
// web search for a question that asks for the web.
//
// With verbose, each call's result follows on its own indented line. It
// fails only when w does.
func writeLog(w io.Writer, entries []rpc.LogEntry, verbose bool) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "No tool calls yet.")
		return err
	}
	// A tabwriter lines up columns: each tab ends a cell, and Flush pads
	// every cell in a column to the widest one. It writes into a buffer
	// first, so the result lines can go between the rows afterwards
	// without breaking the columns.
	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	for _, e := range entries {
		approval := e.Approval
		switch {
		case approval == "" && e.Caller != "":
			approval = "by " + e.Caller // merud made the call; no model chose it
		case approval == "":
			approval = "-" // nobody was asked
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s.%s\t%s\t%s\t%s\t%s\n",
			localTime(e.Time), shortID(e.Session), e.Kind, e.Server, e.Tool,
			e.Outcome, approval, millis(e.DurationMillis), argsCell(e))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	rows := strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n")
	var b strings.Builder
	for i, row := range rows {
		// The last cell may be empty, which leaves padding at the end.
		b.WriteString(strings.TrimRight(row, " ") + "\n")
		if verbose {
			// strings.Fields splits on any run of spaces and new lines, so
			// a result of many lines prints as one.
			result := strings.Join(strings.Fields(entries[i].Result), " ")
			if result == "" {
				result = "(no result)"
			}
			b.WriteString("    " + result + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// argsCell returns the last cell of a log line: the arguments as compact
// JSON, cut to logArgsWidth, or for a local command the program and
// arguments it ran, as a reader would type them, cut to logArgvWidth. A
// command's row holds {"argv":[...],"params":{...}}; a row without an argv,
// such as a call whose arguments didn't render, shows its JSON.
func argsCell(e rpc.LogEntry) string {
	if e.Kind == "command" {
		var a struct {
			Argv []string `json:"argv"`
		}
		if err := json.Unmarshal(e.Args, &a); err == nil && len(a.Argv) > 0 {
			return rpc.Cut(rpc.ArgvLine(a.Argv), logArgvWidth)
		}
	}
	return rpc.ArgsLine(e.Args, logArgsWidth)
}

// localTime turns an RFC 3339 time from merud into this machine's local
// time, such as "2026-09-24 10:17:21". A time that doesn't parse comes back
// as it is.
func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format(time.DateTime)
}

// shortID trims a session ID for a log line. IDs look like
// 2026-09-23T101500-ab12: the start time plus four random hex digits. The
// line already shows the date, so it keeps what follows the "T".
func shortID(id string) string {
	if _, rest, ok := strings.Cut(id, "T"); ok {
		return rest
	}
	return id
}
