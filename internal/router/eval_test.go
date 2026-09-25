// This file scores the router against labelled questions: it loads
// testdata/routes.jsonl, splits it into a fit set and a held-out set, and
// turns a list of model answers into a report (accuracy, per-route precision
// and recall, the confusion matrix, calibration error, fallback rate and
// latency). It also scores the two decision rules against each other and
// ranks the marginal rule's thresholds. It has no build tag, so `go test` checks the arithmetic with
// fixtures; eval_integration_test.go feeds it answers from the real model.

package router

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// labelled is one row of testdata/routes.jsonl: a question, the route a
// person would pick, and why. History holds earlier turns for follow-ups
// that only make sense with them.
type labelled struct {
	Q       string `json:"q"`
	Route   Route  `json:"route"`
	Why     string `json:"why"`
	History []struct {
		Q string `json:"q"`
		A string `json:"a"`
	} `json:"history"`
	// Tag groups rows for the report. "connected" marks questions that need
	// a connected tool server (mail, calendar, Drive, the notes vault, a
	// local command) or the clock.
	Tag string `json:"tag"`
}

// turn builds the router's input for this row: the history as alternating
// user and assistant messages, oldest first, then the question.
func (l labelled) turn() Turn {
	var hist []engine.Message
	for _, h := range l.History {
		hist = append(hist,
			engine.Message{Role: engine.RoleUser, Content: h.Q},
			engine.Message{Role: engine.RoleAssistant, Content: h.A})
	}
	return Turn{History: hist, Question: l.Q, Folders: evalFolders, Tools: evalTools}
}

// evalFolders stands in for the user's [index] folders on every labelled
// row, so rows can ask about "meru" or "blog" by name the way a user
// asks about their own projects.
var evalFolders = []string{"~/notes", "~/repos/meru", "~/repos/blog"}

// evalTools stands in for what the user has connected on every labelled
// row: the catalog's google and obsidian servers with their allow lists,
// a git-log command, and the built-ins with web search on. It is what
// agent.ConnectedTools gives for that config; TestConnectedTools in
// internal/agent checks the same text.
var evalTools = []string{"google (gmail, message, thread, event, drive)", "obsidian (vault)",
	"git-log", "web search"}

// loadLabelled reads a JSONL file of labelled questions, one JSON object per
// line. It fails on a bad line or on a route that isn't one of the four.
func loadLabelled(path string) ([]labelled, error) {
	f, err := os.Open(path) // #nosec G304 -- a test fixture path, not user input
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []labelled
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var l labelled
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if _, ok := ParseRoute(string(l.Route)); !ok || l.Q == "" {
			return nil, fmt.Errorf("%s:%d: want a question and one of the four routes, got %q", path, n, l.Route)
		}
		rows = append(rows, l)
	}
	return rows, sc.Err()
}

// split divides rows into a fit set (about 70%) and a held-out set (about
// 30%). It works per route, in file order: of every ten rows with the same
// label, the 3rd, 6th and 9th go to the held-out set. Splitting per route
// keeps each route's share the same in both sets, and the rule depends only
// on the file, so every run gets the same split.
func split(rows []labelled) (fit, held []labelled) {
	seen := map[Route]int{}
	for _, r := range rows {
		i := seen[r.Route]
		seen[r.Route]++
		switch i % 10 {
		case 2, 5, 8:
			held = append(held, r)
		default:
			fit = append(fit, r)
		}
	}
	return fit, held
}

// sample is one labelled question after the model answered it: the label,
// the raw one-token completion and how long Decide took. Keeping the raw
// completion lets the report replay decide at any temperature and threshold
// without asking the model again.
type sample struct {
	Want    Route
	Comp    engine.Completion
	Latency time.Duration
}

// routeIndex gives each route its row and column in the confusion matrix, in
// letter order.
func routeIndex(r Route) int {
	for i, o := range options {
		if o.route == r {
			return i
		}
	}
	return -1
}

// report is the score of one run at one temperature and threshold.
type report struct {
	N int
	// TopAccuracy counts the model's most likely route, before any fallback.
	TopAccuracy float64
	// Accuracy counts the route Decide returned, fallback included.
	Accuracy float64
	// Missed is the share of turns whose returned route lacks something the
	// label needs, such as "direct" for a question that needs a search. A
	// fallback to search+tools never misses; it only costs more.
	Missed float64
	// FallbackRate is the share of turns that took the fallback, whether for
	// low confidence or a degraded answer.
	FallbackRate float64
	// Confusion[want][got] counts the model's top pick against the label.
	Confusion [4][4]int
	// Precision and Recall are per route, for the top pick, in letter order.
	Precision, Recall [4]float64
	// MeanConfRight and MeanConfWrong are the mean top-pick confidence when
	// the top pick was right and when it was wrong.
	MeanConfRight, MeanConfWrong float64
	// ECE is the expected calibration error of the top-pick confidence.
	ECE float64
	// P50 and P95 are latency percentiles over the samples.
	P50, P95 time.Duration
}

// needs reports whether route r retrieves from files and whether it calls
// tools. It lets the report tell a cheap miss from a harmless extra.
func needs(r Route) (search, tools bool) {
	return r == RouteSearch || r == RouteSearchTools, r == RouteTools || r == RouteSearchTools
}

// score replays decide over every sample with cfg and returns the report.
func score(samples []sample, cfg Config) report {
	rep := report{N: len(samples)}
	if len(samples) == 0 {
		return rep
	}
	var right, top, missed, fallback int
	var confs []float64
	var correct []bool
	var sumRight, sumWrong float64
	var nRight, nWrong int
	var lat []time.Duration
	for _, s := range samples {
		d := decide(s.Comp, cfg)
		win, conf := best(d.Probs)
		if d.Outcome == OutcomeDegraded {
			win, conf = "", 0 // no real distribution, so no top pick
		}
		ok := win == s.Want
		if d.Route == s.Want {
			right++
		}
		if d.Outcome != OutcomeOK {
			fallback++
		}
		ns, nt := needs(s.Want)
		gs, gt := needs(d.Route)
		if (ns && !gs) || (nt && !gt) {
			missed++
		}
		if w, g := routeIndex(s.Want), routeIndex(win); w >= 0 && g >= 0 {
			rep.Confusion[w][g]++
		}
		if ok {
			top++
			sumRight += conf
			nRight++
		} else {
			sumWrong += conf
			nWrong++
		}
		confs = append(confs, conf)
		correct = append(correct, ok)
		lat = append(lat, s.Latency)
	}
	n := float64(len(samples))
	rep.Accuracy = float64(right) / n
	rep.TopAccuracy = float64(top) / n
	rep.Missed = float64(missed) / n
	rep.FallbackRate = float64(fallback) / n
	rep.MeanConfRight = ratio(sumRight, nRight)
	rep.MeanConfWrong = ratio(sumWrong, nWrong)
	rep.ECE = ece(confs, correct, 10)
	for i := range 4 {
		var col, row int
		for j := range 4 {
			col += rep.Confusion[j][i]
			row += rep.Confusion[i][j]
		}
		rep.Precision[i] = ratio(float64(rep.Confusion[i][i]), col)
		rep.Recall[i] = ratio(float64(rep.Confusion[i][i]), row)
	}
	rep.P50 = percentile(lat, 0.50)
	rep.P95 = percentile(lat, 0.95)
	return rep
}

// ratio returns num/den, or 0 when den is 0.
func ratio(num float64, den int) float64 {
	if den == 0 {
		return 0
	}
	return num / float64(den)
}

// ece is the expected calibration error: it sorts the answers into equal
// width confidence bins, and in each bin compares the mean confidence with
// the share that was right. The result is the average gap, weighted by how
// many answers fell in each bin. 0 means the confidence can be taken at its
// word: of the answers given at 0.8, eight in ten were right.
func ece(conf []float64, correct []bool, bins int) float64 {
	if len(conf) == 0 || bins < 1 {
		return 0
	}
	sumConf := make([]float64, bins)
	sumOK := make([]float64, bins)
	count := make([]int, bins)
	for i, c := range conf {
		b := min(int(c*float64(bins)), bins-1) // conf 1.0 goes in the top bin
		b = max(b, 0)
		sumConf[b] += c
		if correct[i] {
			sumOK[b]++
		}
		count[b]++
	}
	total := 0.0
	for b := range bins {
		if count[b] == 0 {
			continue
		}
		gap := math.Abs(sumConf[b]-sumOK[b]) / float64(count[b])
		total += gap * float64(count[b]) / float64(len(conf))
	}
	return total
}

// percentile returns the p-th percentile (0 to 1) of ds by the nearest-rank
// method, or 0 for an empty list.
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// fitTemperature returns the temperature from grid with the lowest ECE on
// samples, and the ECE at each grid point. Temperature never changes the top
// pick, so it moves only the calibration numbers.
func fitTemperature(samples []sample, cfg Config, grid []float64) (float64, []float64) {
	best, bestECE := cfg.Temperature, math.Inf(1)
	eces := make([]float64, len(grid))
	for i, t := range grid {
		c := cfg
		c.Temperature = t
		eces[i] = score(samples, c).ECE
		if eces[i] < bestECE {
			best, bestECE = t, eces[i]
		}
	}
	return best, eces
}

// write prints r as a plain-text report under a heading.
func (r report) write(w io.Writer, heading string) {
	fmt.Fprintf(w, "== %s (n=%d)\n", heading, r.N)
	fmt.Fprintf(w, "accuracy %.3f (top pick %.3f)  missed %.3f  fallback %.3f\n",
		r.Accuracy, r.TopAccuracy, r.Missed, r.FallbackRate)
	fmt.Fprintf(w, "mean confidence right %.3f wrong %.3f  ECE %.3f\n", r.MeanConfRight, r.MeanConfWrong, r.ECE)
	fmt.Fprintf(w, "latency p50 %v p95 %v\n", r.P50.Round(100*time.Microsecond), r.P95.Round(100*time.Microsecond))
	fmt.Fprintf(w, "%-13s %9s %6s   confusion (rows want, columns got)\n", "route", "precision", "recall")
	fmt.Fprintf(w, "%-13s %9s %6s  ", "", "", "")
	for _, o := range options {
		fmt.Fprintf(w, " %6.6s", o.route)
	}
	fmt.Fprintln(w)
	for i, o := range options {
		fmt.Fprintf(w, "%-13s %9.3f %6.3f  ", o.route, r.Precision[i], r.Recall[i])
		for j := range 4 {
			fmt.Fprintf(w, " %6d", r.Confusion[i][j])
		}
		fmt.Fprintln(w)
	}
}

// TestLabelledSet checks the fixture: it parses, every route has about the
// same number of rows, and the split holds out about 30% of each route.
func TestLabelledSet(t *testing.T) {
	rows, err := loadLabelled("testdata/routes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 100 {
		t.Errorf("%d labelled rows, want at least 100", len(rows))
	}
	fit, held := split(rows)
	perRoute := map[Route][2]int{}
	for _, r := range fit {
		c := perRoute[r.Route]
		c[0]++
		perRoute[r.Route] = c
	}
	for _, r := range held {
		c := perRoute[r.Route]
		c[1]++
		perRoute[r.Route] = c
	}
	for _, o := range options {
		c := perRoute[o.route]
		if c[0]+c[1] < 25 {
			t.Errorf("route %s has %d rows, want at least 25", o.route, c[0]+c[1])
		}
		share := float64(c[1]) / float64(c[0]+c[1])
		if share < 0.25 || share > 0.35 {
			t.Errorf("route %s holds out %.2f of its rows, want about 0.3", o.route, share)
		}
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Q] && len(r.History) == 0 {
			t.Errorf("duplicate question %q", r.Q)
		}
		seen[r.Q] = true
	}
}

func TestSplitIsStable(t *testing.T) {
	var rows []labelled
	for i := range 20 {
		rows = append(rows, labelled{Q: fmt.Sprint(i), Route: RouteSearch})
	}
	_, held := split(rows)
	var got []string
	for _, r := range held {
		got = append(got, r.Q)
	}
	want := []string{"2", "5", "8", "12", "15", "18"}
	if !slices.Equal(got, want) {
		t.Errorf("held out %v, want %v", got, want)
	}
}

func TestTurnFromHistory(t *testing.T) {
	l := labelled{Q: "and then?"}
	l.History = append(l.History, struct {
		Q string `json:"q"`
		A string `json:"a"`
	}{"first", "one"})
	turn := l.turn()
	if turn.Question != "and then?" || len(turn.History) != 2 ||
		turn.History[0].Role != engine.RoleUser || turn.History[1].Content != "one" {
		t.Errorf("turn = %+v", turn)
	}
}

func TestECE(t *testing.T) {
	tests := []struct {
		name    string
		conf    []float64
		correct []bool
		want    float64
	}{
		{"empty", nil, nil, 0},
		{"perfect", []float64{1, 1}, []bool{true, true}, 0},
		{"always wrong at 0.9", []float64{0.9, 0.9}, []bool{false, false}, 0.9},
		// Bin 0.8-0.9 holds two answers at 0.8, one right: gap 0.3.
		// Bin 0.3-0.4 holds two answers at 0.3, none right: gap 0.3.
		{"two bins", []float64{0.8, 0.8, 0.3, 0.3}, []bool{true, false, false, false}, 0.3},
		// 1.0 goes in the top bin rather than off the end.
		{"one at 1.0", []float64{1.0, 0.95}, []bool{true, false}, math.Abs(0.975 - 0.5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ece(tt.conf, tt.correct, 10); !near(got, tt.want) {
				t.Errorf("ece = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPercentile(t *testing.T) {
	ms := func(n ...int) []time.Duration {
		var d []time.Duration
		for _, x := range n {
			d = append(d, time.Duration(x)*time.Millisecond)
		}
		return d
	}
	lat := ms(50, 10, 40, 20, 30, 60, 70, 80, 90, 100)
	if got := percentile(lat, 0.5); got != 50*time.Millisecond {
		t.Errorf("p50 = %v, want 50ms", got)
	}
	if got := percentile(lat, 0.95); got != 100*time.Millisecond {
		t.Errorf("p95 = %v, want 100ms", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("p50 of nothing = %v, want 0", got)
	}
	if lat[0] != 50*time.Millisecond {
		t.Error("percentile sorted its input in place")
	}
}

// completion wraps alternatives in a one-token Completion.
func completion(pos engine.PositionLogProbs) engine.Completion {
	return engine.Completion{LogProbs: []engine.PositionLogProbs{pos}}
}

func TestScore(t *testing.T) {
	ln := math.Log
	samples := []sample{
		// Right and sure.
		{RouteDirect, completion(alts("A", ln(0.9), "B", ln(0.1))), 10 * time.Millisecond},
		// Wrong: search wanted, direct picked at 0.6. Misses the search.
		{RouteSearch, completion(alts("A", ln(0.6), "B", ln(0.4))), 20 * time.Millisecond},
		// Top pick right (tools, 0.4) but below the floor, so fallback.
		{RouteTools, completion(alts("C", ln(0.4), "D", ln(0.3), "A", ln(0.3))), 30 * time.Millisecond},
		// Degraded: one letter only.
		{RouteSearchTools, completion(alts("D", ln(0.9))), 40 * time.Millisecond},
	}
	rep := score(samples, testConfig())
	if rep.N != 4 || !near(rep.Accuracy, 0.5) || !near(rep.TopAccuracy, 0.5) {
		t.Errorf("accuracy %v top %v, want 0.5 and 0.5", rep.Accuracy, rep.TopAccuracy)
	}
	if !near(rep.FallbackRate, 0.5) || !near(rep.Missed, 0.25) {
		t.Errorf("fallback %v missed %v, want 0.5 and 0.25", rep.FallbackRate, rep.Missed)
	}
	// The degraded sample has no distribution, so it lands nowhere in the
	// matrix; the other three do.
	want := [4][4]int{{1, 0, 0, 0}, {1, 0, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 0}}
	if rep.Confusion != want {
		t.Errorf("confusion = %v, want %v", rep.Confusion, want)
	}
	if !near(rep.Precision[0], 0.5) || !near(rep.Recall[0], 1) || !near(rep.Recall[1], 0) {
		t.Errorf("precision %v recall %v", rep.Precision, rep.Recall)
	}
	if !near(rep.MeanConfRight, (0.9+0.4)/2) || !near(rep.MeanConfWrong, 0.6/2) {
		t.Errorf("mean confidence right %v wrong %v", rep.MeanConfRight, rep.MeanConfWrong)
	}
	if rep.P50 != 20*time.Millisecond || rep.P95 != 40*time.Millisecond {
		t.Errorf("p50 %v p95 %v", rep.P50, rep.P95)
	}
	var b strings.Builder
	rep.write(&b, "fixture")
	if !strings.Contains(b.String(), "accuracy 0.500") || !strings.Contains(b.String(), "search+tools") {
		t.Errorf("report text:\n%s", b.String())
	}
}

func TestFitTemperature(t *testing.T) {
	// Every answer is given at 0.99 but only half are right, so the model
	// is overconfident and a temperature above 1 must lower the ECE.
	var samples []sample
	for i := range 10 {
		want := RouteDirect
		if i%2 == 1 {
			want = RouteSearch
		}
		samples = append(samples, sample{Want: want, Comp: completion(alts("A", math.Log(0.99), "B", math.Log(0.01)))})
	}
	grid := []float64{1, 2, 4, 8}
	best, eces := fitTemperature(samples, testConfig(), grid)
	if best <= 1 {
		t.Errorf("fitted temperature %v, want above 1; ECE by grid %v", best, eces)
	}
	if eces[0] <= eces[len(eces)-1] {
		t.Errorf("ECE should fall as temperature rises here: %v", eces)
	}
}

// A decision rule turns one saved completion into a route. The harness
// compares rules by replaying each one over the same completions, so every
// rule sees exactly the same model output.
type rule func(comp engine.Completion) Route

// topRule replays decide under RuleTop with cfg's floor and fallback: the
// most likely letter wins, and a winner below cfg.MinConfidence takes
// cfg.Fallback.
func topRule(cfg Config) rule {
	cfg.Rule = RuleTop // cfg is a copy, so the caller's Config is untouched
	return func(comp engine.Completion) Route { return decide(comp, cfg).Route }
}

// marginalRule replays decide under RuleMarginal with the two thresholds
// given. A degraded distribution still takes cfg.Fallback.
func marginalRule(cfg Config, searchT, toolsT float64) rule {
	cfg.Rule, cfg.SearchThreshold, cfg.ToolsThreshold = RuleMarginal, searchT, toolsT
	return func(comp engine.Completion) Route { return decide(comp, cfg).Route }
}

// ruleReport scores one rule on one set. Every rate is a share of all N
// rows, so the rates of two rules on the same set compare directly.
type ruleReport struct {
	N        int
	Accuracy float64 // the route matches the label exactly
	// Missed counts routes that lack a search or tools the label needs.
	Missed float64
	// OverSearch and OverTools count routes that offer a search, or tools,
	// the label doesn't need. Extra tools cost more than an extra search:
	// their schemas lengthen the prompt, and the lite model handles a long
	// tool list worse.
	OverSearch, OverTools float64
	// Changed counts rows where this rule's route differs from the base
	// rule's.
	Changed int
}

// scoreRule replays r and base over samples and scores r against the
// labels, counting the rows where the two disagree.
func scoreRule(samples []sample, r, base rule) ruleReport {
	rep := ruleReport{N: len(samples)}
	if len(samples) == 0 {
		return rep
	}
	var right, missed, overS, overT int
	for _, s := range samples {
		got := r(s.Comp)
		if got != base(s.Comp) {
			rep.Changed++
		}
		if got == s.Want {
			right++
		}
		ns, nt := needs(s.Want)
		gs, gt := needs(got)
		if (ns && !gs) || (nt && !gt) {
			missed++
		}
		if gs && !ns {
			overS++
		}
		if gt && !nt {
			overT++
		}
	}
	n := float64(len(samples))
	rep.Accuracy = float64(right) / n
	rep.Missed = float64(missed) / n
	rep.OverSearch = float64(overS) / n
	rep.OverTools = float64(overT) / n
	return rep
}

// gridPoint is one pair of marginal thresholds with its scores on the fit
// and held-out sets.
type gridPoint struct {
	Search, Tools float64
	Fit, Held     ruleReport
}

// rankGrid sorts points best first, judged on the fit set only. A point
// whose tool over-offer is at most toolsLimit comes before one above it.
// Among those, the lowest missed rate wins, then the highest accuracy,
// then the lowest tool over-offer, then the lowest search over-offer. When
// the fit set can't tell two points apart, the higher tools threshold wins,
// then the higher search threshold: of two rules that score the same, the
// one that offers less costs less on questions the set lacks. The held-out
// scores play no part, so they stay an honest check of the pick.
func rankGrid(points []gridPoint, toolsLimit float64) []gridPoint {
	out := slices.Clone(points)
	ok := func(g gridPoint) bool { return g.Fit.OverTools <= toolsLimit+1e-9 }
	// SortFunc sorts by the comparison below: negative puts a first,
	// positive puts b first. The thresholds settle every tie, so the
	// ranking is the same on every run.
	slices.SortFunc(out, func(a, b gridPoint) int {
		if ok(a) != ok(b) {
			if ok(a) {
				return -1
			}
			return 1
		}
		for _, d := range []float64{
			a.Fit.Missed - b.Fit.Missed,
			b.Fit.Accuracy - a.Fit.Accuracy,
			a.Fit.OverTools - b.Fit.OverTools,
			a.Fit.OverSearch - b.Fit.OverSearch,
			b.Tools - a.Tools,
			b.Search - a.Search,
		} {
			if d < -1e-9 {
				return -1
			}
			if d > 1e-9 {
				return 1
			}
		}
		return 0
	})
	return out
}

// write prints r as one row of the rules table.
func (r ruleReport) write(w io.Writer, name, set string) {
	fmt.Fprintf(w, "%-24s %-5s %8.3f %7.3f %11.3f %10.3f %8d\n",
		name, set, r.Accuracy, r.Missed, r.OverSearch, r.OverTools, r.Changed)
}

func TestScoreRule(t *testing.T) {
	ln := math.Log
	samples := []sample{
		// Top picks search; the marginal rule at 0.45 adds tools. Right.
		{Want: RouteSearchTools, Comp: completion(alts("A", ln(0.05), "B", ln(0.47), "C", ln(0.26), "D", ln(0.22)))},
		// Direct wanted. Top picks direct; marginal at 0.45 agrees.
		{Want: RouteDirect, Comp: completion(alts("A", ln(0.8), "B", ln(0.1), "C", ln(0.1)))},
		// Tools wanted, but both rules see mostly search: a miss and an
		// extra search for both.
		{Want: RouteTools, Comp: completion(alts("A", ln(0.1), "B", ln(0.8), "C", ln(0.1)))},
		// Search wanted; the tool letters add up to 0.5, so the marginal
		// rule over-offers tools.
		{Want: RouteSearch, Comp: completion(alts("B", ln(0.5), "C", ln(0.25), "D", ln(0.25)))},
		// Degraded: one letter. Both rules fall back to search+tools.
		{Want: RouteDirect, Comp: completion(alts("A", ln(0.9)))},
	}
	cfg := testConfig()
	top := topRule(cfg)
	if rep := scoreRule(samples, top, top); rep.Changed != 0 || !near(rep.Accuracy, 0.4) ||
		!near(rep.Missed, 0.4) || !near(rep.OverSearch, 0.4) || !near(rep.OverTools, 0.2) {
		t.Errorf("top rule: %+v", rep)
	}
	rep := scoreRule(samples, marginalRule(cfg, 0.45, 0.45), top)
	// The marginal rule fixes the first row and breaks the fourth, so
	// accuracy holds at 0.4 while the missed rate halves.
	if rep.N != 5 || rep.Changed != 2 || !near(rep.Accuracy, 0.4) || !near(rep.Missed, 0.2) ||
		!near(rep.OverSearch, 0.4) || !near(rep.OverTools, 0.4) {
		t.Errorf("marginal rule: %+v", rep)
	}
	var b strings.Builder
	rep.write(&b, "marginal", "fit")
	if !strings.Contains(b.String(), "0.400") {
		t.Errorf("row text: %q", b.String())
	}
}

func TestRankGrid(t *testing.T) {
	pt := func(s, tl, missed, acc, overT float64) gridPoint {
		return gridPoint{Search: s, Tools: tl, Fit: ruleReport{Missed: missed, Accuracy: acc, OverTools: overT}}
	}
	points := []gridPoint{
		pt(0.2, 0.2, 0.00, 0.50, 0.40), // lowest missed, but too many tools
		pt(0.3, 0.3, 0.10, 0.70, 0.10),
		pt(0.4, 0.2, 0.10, 0.80, 0.10), // ties with 0.4/0.4 below, which wins on its higher tools bar
		pt(0.4, 0.4, 0.10, 0.80, 0.10), // same missed as 0.3/0.3, better accuracy
		pt(0.5, 0.5, 0.20, 0.90, 0.00),
	}
	got := rankGrid(points, 0.15)
	var order [][2]float64
	for _, g := range got {
		order = append(order, [2]float64{g.Search, g.Tools})
	}
	want := [][2]float64{{0.4, 0.4}, {0.4, 0.2}, {0.3, 0.3}, {0.5, 0.5}, {0.2, 0.2}}
	if !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
	if points[0].Search != 0.2 {
		t.Error("rankGrid sorted its input in place")
	}
}
