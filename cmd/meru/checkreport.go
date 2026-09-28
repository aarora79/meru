// This file holds `meru check report`: it reads the result files that
// `meru check --json` or `--save` wrote and prints one Markdown page that
// compares the model sets: pass rates overall and by category, and the
// timings. The charts are Mermaid blocks, which GitHub draws, so the page
// needs no image files. It sends nothing to merud.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// setStats is everything the report says about one model set.
type setStats struct {
	Name   string
	Model  string
	Runs   int     // distinct runs, one per `meru check` pass
	Total  int     // results, across every run
	Passed int     // results that passed
	Low    float64 // lowest pass rate of a single run, in percent
	High   float64 // highest pass rate of a single run, in percent
	// ByCategory maps a category to [passed, total].
	ByCategory map[string][2]int
	TTFT, TTLT []float64 // seconds, one per result that had them
	TPOT       []float64 // milliseconds per output token
	TokensIn   []float64
	TokensOut  []float64
}

// checkReportCmd runs `meru check report <file>...`. It reads every result
// in the files, groups them by model set, and writes the Markdown page to
// stdout. It fails when no file is named, a file can't be read, or a line
// isn't a result.
func checkReportCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: meru check report <results.jsonl> [more results files]")
	}
	var results []checkResult
	for _, path := range args {
		rs, err := readResults(path)
		if err != nil {
			return err
		}
		results = append(results, rs...)
	}
	if len(results) == 0 {
		return errors.New("check report: the files hold no results")
	}
	sets := summarize(results)
	_, err := io.WriteString(stdout, reportMarkdown(sets, categoriesOf(results), time.Now()))
	return err
}

// readResults reads one results file: one checkResult per line, as
// `meru check --json` prints and `--save` appends. Blank lines are
// skipped. It fails on the first line that isn't a result.
func readResults(path string) ([]checkResult, error) {
	f, err := os.Open(path) // #nosec G304 -- the user names the results file on their own command line
	if err != nil {
		return nil, fmt.Errorf("check report: %w", err)
	}
	defer f.Close()
	var out []checkResult
	sc := bufio.NewScanner(f)
	// A result carries the whole answer, which can run past the scanner's
	// 64 KB default, so allow lines up to 4 MB.
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r checkResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("check report: %s line %d: %w", path, n, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("check report: %s: %w", path, err)
	}
	return out, nil
}

// summarize groups results by model set, in order of first appearance, and
// works out each set's numbers. A result with no model set groups under
// its model, or under "unknown" when it has neither.
func summarize(results []checkResult) []setStats {
	var order []string
	byName := map[string]*setStats{}
	runs := map[string]map[string][2]int{} // set → run → [passed, total]
	for _, r := range results {
		name := r.ModelSet
		if name == "" {
			name = r.Model
		}
		if name == "" {
			name = "unknown"
		}
		s, ok := byName[name]
		if !ok {
			s = &setStats{Name: name, Model: r.Model, ByCategory: map[string][2]int{}}
			byName[name] = s
			runs[name] = map[string][2]int{}
			order = append(order, name)
		}
		pass := 0
		if r.Pass {
			pass = 1
		}
		s.Total++
		s.Passed += pass
		c := s.ByCategory[r.Category]
		s.ByCategory[r.Category] = [2]int{c[0] + pass, c[1] + 1}
		run := runs[name][r.Run]
		runs[name][r.Run] = [2]int{run[0] + pass, run[1] + 1}
		// A turn that failed before its done event has no stats; leave it
		// out of the timings rather than count it as zero.
		if r.TTLTMillis > 0 {
			s.TTFT = append(s.TTFT, float64(r.TTFTMillis)/1000)
			s.TTLT = append(s.TTLT, float64(r.TTLTMillis)/1000)
		}
		if r.TPOTMillis > 0 {
			s.TPOT = append(s.TPOT, r.TPOTMillis)
		}
		if r.TokensIn > 0 {
			s.TokensIn = append(s.TokensIn, float64(r.TokensIn))
			s.TokensOut = append(s.TokensOut, float64(r.TokensOut))
		}
	}
	var out []setStats
	for _, name := range order {
		s := byName[name]
		s.Runs = len(runs[name])
		s.Low, s.High = 100, 0
		for _, pt := range runs[name] {
			rate := percent(pt[0], pt[1])
			s.Low, s.High = min(s.Low, rate), max(s.High, rate)
		}
		out = append(out, *s)
	}
	return out
}

// categoriesOf lists the categories in results, sorted, each once.
func categoriesOf(results []checkResult) []string {
	var cats []string
	for _, r := range results {
		if !slices.Contains(cats, r.Category) {
			cats = append(cats, r.Category)
		}
	}
	sort.Strings(cats)
	return cats
}

// reportMarkdown writes the page: a notice, the pass-rate chart and table,
// pass rates by category, then the timing charts and table. now dates the
// page.
func reportMarkdown(sets []setStats, cats []string, now time.Time) string {
	var b strings.Builder
	b.WriteString("# Meru benchmark results\n\n")
	b.WriteString("> [!IMPORTANT]\n")
	b.WriteString("> These numbers come from the author's private dataset: questions about the author's own files,\n")
	b.WriteString("> notes, mail and calendar, which aren't published. You can't rerun them, and your numbers on\n")
	b.WriteString("> your own data will differ. [README.md](README.md) says how to build a dataset of your own.\n\n")
	fmt.Fprintf(&b, "Generated by `meru check report` on %s.\n\n", now.Format("2 January 2006"))

	b.WriteString("## Pass rate\n\n")
	b.WriteString(barChart("Questions passed (%)", 100, false, func(s setStats) float64 { return percent(s.Passed, s.Total) }, sets))
	b.WriteString("| Model set | Answer model | Runs | Results | Passed | Lowest run | Highest run |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, s := range sets {
		fmt.Fprintf(&b, "| %s | `%s` | %d | %d | %.0f%% | %.0f%% | %.0f%% |\n",
			s.Name, s.Model, s.Runs, s.Total, percent(s.Passed, s.Total), s.Low, s.High)
	}

	b.WriteString("\n## Pass rate by category\n\n")
	b.WriteString("| Category |")
	for _, s := range sets {
		fmt.Fprintf(&b, " %s |", s.Name)
	}
	b.WriteString("\n| --- |" + strings.Repeat(" ---: |", len(sets)) + "\n")
	for _, c := range cats {
		fmt.Fprintf(&b, "| %s |", c)
		for _, s := range sets {
			pt := s.ByCategory[c]
			if pt[1] == 0 {
				b.WriteString(" — |")
				continue
			}
			fmt.Fprintf(&b, " %.0f%% (%d/%d) |", percent(pt[0], pt[1]), pt[0], pt[1])
		}
		b.WriteString("\n")
	}

	b.WriteString("\n## Speed\n\n")
	b.WriteString("Times count from when `merud` got the question, so they include routing, search and tool rounds.\n")
	b.WriteString("TTFT is the time to the answer's first token and TTLT the time to its last. TPOT is the model's\n")
	b.WriteString("writing time per output token.\n\n")
	b.WriteString(barChart("Median time to last token (s)", 0, true, func(s setStats) float64 { return median(s.TTLT) }, sets))
	b.WriteString(barChart("Median time to first token (s)", 0, true, func(s setStats) float64 { return median(s.TTFT) }, sets))
	b.WriteString("| Model set | TTFT p50 | TTFT p95 | TTLT p50 | TTLT p95 | TPOT p50 | Tokens in, p50 | Tokens out, p50 |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, s := range sets {
		fmt.Fprintf(&b, "| %s | %.1f s | %.1f s | %.1f s | %.1f s | %.1f ms | %.0f | %.0f |\n", s.Name,
			median(s.TTFT), quantile(s.TTFT, 0.95), median(s.TTLT), quantile(s.TTLT, 0.95),
			median(s.TPOT), median(s.TokensIn), median(s.TokensOut))
	}
	b.WriteString("\n## Accuracy against time\n\n")
	b.WriteString("Each point is one model set: higher passed more questions, further left answered sooner, so the\n")
	b.WriteString("best sets sit top left. A set keeps its colour in both charts.\n\n")
	b.WriteString(tradeoffChart("Passed by first token", "time to first token",
		func(s setStats) float64 { return median(s.TTFT) }, sets))
	b.WriteString(tradeoffChart("Passed by last token", "time to last token",
		func(s setStats) float64 { return median(s.TTLT) }, sets))
	return b.String()
}

// barChart writes one Mermaid xychart-beta block with one bar per model
// set, best first so the eye reads a ranking. ascending puts the smallest
// value first, for times; otherwise the largest comes first, for pass
// rates. top fixes the y-axis at 0 to top; 0 lets Mermaid pick it.
func barChart(title string, top float64, ascending bool, value func(setStats) float64, sets []setStats) string {
	// Sort a copy, so the tables keep the order summarize gave them.
	sorted := append([]setStats(nil), sets...)
	// SliceStable keeps sets with equal values in their original order.
	sort.SliceStable(sorted, func(i, j int) bool {
		if ascending {
			return value(sorted[i]) < value(sorted[j])
		}
		return value(sorted[i]) > value(sorted[j])
	})
	var b strings.Builder
	quoted := make([]string, len(sorted))
	vals := make([]string, len(sorted))
	for i, s := range sorted {
		quoted[i] = fmt.Sprintf("%q", s.Name)
		vals[i] = fmt.Sprintf("%.1f", value(s))
	}
	b.WriteString("```mermaid\nxychart-beta\n")
	fmt.Fprintf(&b, "    title %q\n", title)
	fmt.Fprintf(&b, "    x-axis [%s]\n", strings.Join(quoted, ", "))
	if top > 0 {
		fmt.Fprintf(&b, "    y-axis 0 --> %g\n", top)
	}
	fmt.Fprintf(&b, "    bar [%s]\n```\n\n", strings.Join(vals, ", "))
	return b.String()
}

// pointColors gives each model set a colour of its own in the trade-off
// charts, by its place in the list; the list repeats after eight sets.
// pointDots are emoji of the same colours, for the key under each chart,
// since a Markdown table can't colour text.
var (
	pointColors = []string{"#1f77b4", "#d62728", "#2ca02c", "#ff7f0e", "#9467bd", "#8b5a2b", "#222222", "#e0b000"}
	pointDots   = []string{"🔵", "🔴", "🟢", "🟠", "🟣", "🟤", "⚫", "🟡"}
)

// tradeoffChart writes one Mermaid quadrantChart block, a point per model
// set with its pass rate up the side and a time along the bottom, then a
// key that names each point's colour with its numbers. Mermaid has no
// plain scatter chart; a quadrant chart places coloured points on two
// axes, which is what this needs. Its axes run from 0 to 1, so the chart
// scales each value: time from 0 to the slowest set rounded up to 10 s,
// and pass rate from the lowest set rounded down to 10% up to 100%, which
// spreads points that sit close. The title carries both ranges, because
// Mermaid centres each axis label under half the chart, not at its end.
func tradeoffChart(title, timeName string, seconds func(setStats) float64, sets []setStats) string {
	maxTime, minPass := 0.0, 100.0
	for _, s := range sets {
		maxTime = math.Max(maxTime, seconds(s))
		minPass = math.Min(minPass, percent(s.Passed, s.Total))
	}
	xTop := math.Max(10, math.Ceil(maxTime/10)*10)
	yLow := math.Min(90, math.Floor(minPass/10)*10)

	var b strings.Builder
	b.WriteString("```mermaid\nquadrantChart\n")
	fmt.Fprintf(&b, "    title %s: 0 to %g s, %g%% to 100%%\n", title, xTop, yLow)
	fmt.Fprintf(&b, "    x-axis \"Sooner\" --> \"Later %s\"\n", timeName)
	b.WriteString("    y-axis \"Fewer passed\" --> \"More passed\"\n")
	for i, s := range sets {
		x := seconds(s) / xTop
		y := (percent(s.Passed, s.Total) - yLow) / (100 - yLow)
		fmt.Fprintf(&b, "    %s: [%.3f, %.3f] color: %s, radius: 7\n", s.Name, x, y, pointColors[i%len(pointColors)])
	}
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "| | Model set | Passed | Median %s |\n| --- | --- | ---: | ---: |\n", timeName)
	for i, s := range sets {
		fmt.Fprintf(&b, "| %s | %s | %.0f%% | %.1f s |\n", pointDots[i%len(pointDots)], s.Name, percent(s.Passed, s.Total), seconds(s))
	}
	b.WriteString("\n")
	return b.String()
}

// percent returns part of whole in percent, and 0 when whole is 0.
func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

// median returns the middle value of xs, and 0 for none.
func median(xs []float64) float64 { return quantile(xs, 0.5) }

// quantile returns the q-th quantile of xs by the nearest-rank rule: the
// smallest value with at least q of the values at or below it. It returns
// 0 for none. It sorts a copy, so xs keeps its order.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[max(i, 0)]
}
