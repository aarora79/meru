// This file holds `meru check`: it reads a file of questions with the
// answers you expect, asks merud each one in turn, and prints PASS or FAIL
// for each, then a summary by category. Rerun it after a change to see what
// got better or worse on your own files and models. checkfile.go holds the
// file format and the grading; this file runs the questions and prints.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// errChecksFailed tells run that at least one question failed. The summary
// already says how many, so run exits 1 without printing it.
var errChecksFailed = errors.New("some checks failed")

// checkResult is what `meru check` reports for one question: one line of
// --json output and one record in a results file. The answer text rides
// along, so two saved runs can be compared line by line.
type checkResult struct {
	// Run is when the run started (RFC 3339); every question in a run
	// shares it.
	Run      string `json:"run"`
	ID       string `json:"id"`
	Category string `json:"category"`
	Question string `json:"question"`
	// Group is the question's session group from the file, and Session the
	// ID merud gave the session it ran in.
	Group   string `json:"session_group,omitempty"`
	Session string `json:"session,omitempty"`
	Pass    bool   `json:"pass"`
	// Reasons says why a question failed, one line per want field.
	Reasons []string `json:"reasons,omitempty"`
	// Denied lists the approvals `meru check` refused, as
	// "approval denied: <tool>".
	Denied  []string `json:"denied,omitempty"`
	Route   string   `json:"route,omitempty"`
	Tools   []string `json:"tools,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Seconds float64  `json:"seconds"`
	Answer  string   `json:"answer"`
	// ModelSet and Model name the model set in use and its answer model
	// when the run started, as merud's models op reported them; both are
	// empty when merud didn't say.
	ModelSet string `json:"model_set,omitempty"`
	Model    string `json:"model,omitempty"`
	// The turn's stats from its done event: time to first token, time to
	// last token, time per output token, and the main model's tokens.
	TTFTMillis int64   `json:"ttft_ms,omitempty"`
	TTLTMillis int64   `json:"ttlt_ms,omitempty"`
	TPOTMillis float64 `json:"tpot_ms,omitempty"`
	TokensIn   int     `json:"tokens_in,omitempty"`
	TokensOut  int     `json:"tokens_out,omitempty"`
}

// checkCmd runs `meru check [file] [--only id,category] [--json] [--save]`.
// args are the words after "check"; flags may come before or after the
// file. With no file it reads checks.jsonl beside the socket, which is
// ~/.meru/checks.jsonl by default.
//
// It fails fast on a bad check file or when merud can't be reached, and
// returns errChecksFailed when any question fails.
func checkCmd(ctx context.Context, socket string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("meru check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	only := flags.String("only", "", "run only these ids or categories, comma-separated")
	asJSON := flags.Bool("json", false, "print one JSON result per question instead of the table")
	save := flags.Bool("save", false, "append the results to checks-results/<date>.jsonl")
	// The flag package stops at the first word that isn't a flag, so parse
	// again after each such word. That lets the file come first or last.
	var words []string
	for rest := args; ; {
		if err := flags.Parse(rest); err != nil {
			return err
		}
		if flags.NArg() == 0 {
			break
		}
		words = append(words, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(words) > 1 {
		return errors.New("usage: meru check [file] [--only id,category] [--json] [--save]")
	}

	home := filepath.Dir(socket) // merud's home: config, socket and checks
	path := filepath.Join(home, "checks.jsonl")
	if len(words) == 1 {
		path = words[0]
	}
	questions, err := readChecks(path, len(words) == 1)
	if err != nil {
		return err
	}
	if questions, err = selectChecks(questions, *only); err != nil {
		return err
	}

	started := time.Now()
	results, err := runChecks(ctx, socket, questions, started, func(r checkResult) error {
		if *asJSON {
			return writeJSONLine(stdout, r)
		}
		return writeCheckLine(stdout, r, questions)
	})
	if err != nil {
		return err
	}
	if !*asJSON {
		if err := writeCheckSummary(stdout, results, time.Since(started)); err != nil {
			return err
		}
	}
	if *save {
		saved, err := saveResults(filepath.Join(home, "checks-results"), started, results)
		if err != nil {
			return err
		}
		// With --json, stdout holds only the records, so the note goes to
		// stderr.
		note := stdout
		if *asJSON {
			note = stderr
		}
		fmt.Fprintf(note, "Results saved to %s\n", saved)
	}
	for _, r := range results {
		if !r.Pass {
			return errChecksFailed
		}
	}
	return nil
}

// readChecks opens and parses the check file at path. named says the user
// gave the path, which changes the hint when the file is missing: a
// question such as `meru check my email` reaches here with "my" as the
// file.
func readChecks(path string, named bool) ([]checkQuestion, error) {
	f, err := os.Open(path) // #nosec G304 -- the user names the file to read on their own command line
	if errors.Is(err, os.ErrNotExist) {
		if named {
			return nil, fmt.Errorf("no check file %s; to ask a question that starts with \"check\", put it in quotes", path)
		}
		return nil, fmt.Errorf("no check file at %s; docs/running.md shows the format and docs/examples/checks.example.jsonl has a start", path)
	}
	if err != nil {
		return nil, err
	}
	// defer runs f.Close() when this function returns. We only read the
	// file, so its Close error can't lose data.
	defer f.Close()
	return parseChecks(f, path)
}

// runChecks asks merud each question in order and calls report with each
// result as soon as it is graded, so a long run shows progress. Questions
// in the same session group share one merud session; every other question
// starts a new one. It returns every result, or the first error from the
// connection or from report.
func runChecks(ctx context.Context, socket string, questions []checkQuestion, started time.Time,
	report func(checkResult) error) ([]checkResult, error) {
	sessions := map[string]string{} // session group → merud's session ID
	set, model := activeModels(ctx, socket)
	var results []checkResult
	for _, q := range questions {
		rec, sessionID, denied, err := askCheck(ctx, socket, q.Question, sessions[q.Session])
		if err != nil {
			return results, fmt.Errorf("%s: %w", q.ID, err)
		}
		if q.Session != "" && sessions[q.Session] == "" {
			sessions[q.Session] = sessionID
		}
		reasons := grade(q.Want, rec)
		r := checkResult{
			Run: started.Format(time.RFC3339), ID: q.ID, Category: q.Category, Question: q.Question,
			Group: q.Session, Session: sessionID, Pass: len(reasons) == 0, Reasons: reasons,
			Denied: denied, Route: rec.Route, Tools: rec.Tools, Sources: rec.Sources,
			Seconds: rec.Seconds, Answer: rec.Answer, ModelSet: set, Model: model,
			TTFTMillis: rec.TTFTMillis, TTLTMillis: rec.TTLTMillis, TPOTMillis: rec.TPOTMillis,
			TokensIn: rec.TokensIn, TokensOut: rec.TokensOut,
		}
		results = append(results, r)
		if err := report(r); err != nil {
			return results, err
		}
	}
	return results, nil
}

// activeModels asks merud which model set is in use and which model
// answers, so each result says what produced it. A merud that can't say,
// such as one older than the models op, gives two empty strings: the
// results still count, with no model on them.
func activeModels(ctx context.Context, socket string) (set, model string) {
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpModels}, nil) {
		if err != nil {
			return "", ""
		}
		if ev.Type == rpc.EventModels && ev.Models != nil {
			set, model = ev.Models.Active, ev.Models.Main
		}
	}
	return set, model
}

// askCheck asks merud one question, in the session named by session or in
// a new one when session is empty. It returns what the turn did, the
// session ID merud used and the approvals it refused.
//
// Checks run with nobody watching, so every approval gets a deny, and the
// deny goes on the record as "approval denied: <tool>". A question that
// needs an approved tool then fails, which is what it should do: its
// answer came without the tool.
//
// It fails only when the connection does. An error event from merud ends
// up in the record, and the question fails on it.
func askCheck(ctx context.Context, socket, question, session string) (turnRecord, string, []string, error) {
	var rec turnRecord
	var answer strings.Builder
	var denied []string
	deny := func(_ context.Context, a rpc.Approval) (rpc.Choice, error) {
		denied = append(denied, "approval denied: "+a.Name)
		return rpc.ChoiceDeny, nil
	}
	req := rpc.Request{Op: rpc.OpAsk, Text: question, Session: session, Source: rpc.SourceCLI}
	start := time.Now()
	var tookMillis int64
	for ev, err := range rpc.Do(ctx, socket, req, deny) {
		if err != nil {
			return rec, session, denied, err
		}
		switch ev.Type {
		case rpc.EventSession:
			session = ev.Session
		case rpc.EventRoute:
			rec.Route = ev.Route
		case rpc.EventSources:
			for _, c := range ev.Sources {
				rec.Sources = appendNew(rec.Sources, c.Path)
			}
		case rpc.EventToolCall:
			if ev.Tool != nil {
				rec.Tools = appendNew(rec.Tools, ev.Tool.Name)
			}
		case rpc.EventToolResult:
			// A call that was refused never ran, so it can't satisfy a
			// "tools" want.
			if ev.Tool != nil && ev.Tool.Outcome != "declined" && ev.Tool.Outcome != "denied" {
				rec.Ran = appendNew(rec.Ran, ev.Tool.Name)
			}
		case rpc.EventToken:
			answer.WriteString(ev.Text)
		case rpc.EventDone:
			tookMillis = ev.DurationMillis
			rec.TTFTMillis, rec.TTLTMillis, rec.TPOTMillis = ev.TTFTMillis, ev.TTLTMillis, ev.TPOTMillis
			rec.TokensIn, rec.TokensOut = ev.TokensIn, ev.TokensOut
		case rpc.EventError:
			rec.Error = ev.Error
		}
	}
	rec.Answer = answer.String()
	// merud's own time for the turn leaves out the socket and this loop.
	// An error event carries none, so fall back to the time we waited.
	rec.Seconds = float64(tookMillis) / 1000
	if tookMillis == 0 {
		rec.Seconds = time.Since(start).Seconds()
	}
	return rec, session, denied, nil
}

// appendNew appends s to list unless list already holds it.
func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// writeCheckLine prints one question's result:
//
//	PASS  direct-capital  direct     direct         1.2s  -
//	FAIL  web-go          web        tools         31.0s  web_search, web_fetch
//	      answer lacks all of: 1.27.1
//
// The columns hold the verdict, the id, the category, the route, the time
// and the tools the model asked for, without their server prefix. The id
// and category columns are as wide as the longest in questions, so the
// lines line up while they print one at a time. Each reason for a FAIL and
// each refused approval follows on its own indented line. It fails only
// when w does.
func writeCheckLine(w io.Writer, r checkResult, questions []checkQuestion) error {
	idWidth, catWidth := 0, 0
	for _, q := range questions {
		idWidth = max(idWidth, len(q.ID))
		catWidth = max(catWidth, len(q.Category))
	}
	lk := newLook(w)
	verdict := lk.good.Render("PASS")
	if !r.Pass {
		verdict = lk.bad.Render("FAIL")
	}
	route, tools := r.Route, strings.Join(shortNames(r.Tools), ", ")
	if route == "" {
		route = "-"
	}
	if tools == "" {
		tools = "-"
	}
	// %-*s pads to the width given just before the value; %6.1f right-aligns
	// the seconds in six characters.
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-*s  %-*s  %-12s %6.1fs  %s\n",
		verdict, idWidth, r.ID, catWidth, r.Category, route, r.Seconds, tools)
	for _, reason := range r.Reasons {
		b.WriteString("      " + reason + "\n")
	}
	for _, d := range r.Denied {
		b.WriteString("      " + lk.amber.Render(d) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeCheckSummary prints how many questions passed in each category, in
// the order the categories first appear, then the total and the run's
// time:
//
//	direct     3/3
//	web        1/2
//
//	4 of 5 passed in 2m 10s
//
// It fails only when w does.
func writeCheckSummary(w io.Writer, results []checkResult, took time.Duration) error {
	var order []string
	passed, total := map[string]int{}, map[string]int{}
	width, allPassed := 0, 0
	for _, r := range results {
		if total[r.Category] == 0 {
			order = append(order, r.Category)
			width = max(width, len(r.Category))
		}
		total[r.Category]++
		if r.Pass {
			passed[r.Category]++
			allPassed++
		}
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, c := range order {
		fmt.Fprintf(&b, "%-*s  %d/%d\n", width, c, passed[c], total[c])
	}
	fmt.Fprintf(&b, "\n%d of %d passed in %s\n", allPassed, len(results), minutes(took))
	_, err := io.WriteString(w, b.String())
	return err
}

// minutes writes a run's length as "42s" under a minute and "6m 05s" from
// there up.
func minutes(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}

// writeJSONLine writes r as one line of JSON.
func writeJSONLine(w io.Writer, r checkResult) error {
	line, err := compactJSON(r)
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}

// saveResults appends one JSON line per result to <dir>/<YYYY-MM-DD>.jsonl,
// named for the day the run started, and returns the file's path. Every
// record carries the run's start time, so the runs of one day stay apart
// in the file. The folder and file are private to the user (0700 and
// 0600), since answers quote your own files.
func saveResults(dir string, started time.Time, results []checkResult) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("save results: %w", err)
	}
	path := filepath.Join(dir, started.Format(time.DateOnly)+".jsonl")
	var b strings.Builder
	for _, r := range results {
		line, err := compactJSON(r)
		if err != nil {
			return "", fmt.Errorf("save results: %w", err)
		}
		b.Write(line)
		b.WriteString("\n")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- a dated file under the Meru home
	if err != nil {
		return "", fmt.Errorf("save results: %w", err)
	}
	if _, err := io.WriteString(f, b.String()); err != nil {
		_ = f.Close() // the write error says more than Close's would
		return "", fmt.Errorf("save results to %s: %w", path, err)
	}
	// A write can fail late, on Close, so its error counts too.
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("save results to %s: %w", path, err)
	}
	return path, nil
}
