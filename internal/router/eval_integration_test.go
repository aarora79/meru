//go:build integration

// This file runs every labelled question in testdata/routes.jsonl through
// Decide against the real local Ollama and prints a report for the fit set
// and the held-out set: accuracy, per-route precision and recall, the
// confusion matrix, calibration error, fallback rate and latency. It then
// sweeps the temperature and the confidence floor, and compares the top-letter
// rule with the marginal rule over a grid of thresholds. Run it with
//
//	make router-eval
//
// It skips when Ollama isn't running or the fast model isn't pulled.
// MERU_TEST_OLLAMA and MERU_TEST_FAST_MODEL override the address and the
// model; the rest of the settings are config.toml's defaults.

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/engine"
)

// recorder is an Engine that passes Generate through to a real engine and
// keeps the last completion, so the harness can replay decide on the raw
// log probabilities at other temperatures and thresholds.
type recorder struct {
	engine.Engine
	last engine.Completion
}

// Generate calls the real engine and remembers what it returned.
func (r *recorder) Generate(ctx context.Context, msgs []engine.Message, tools []engine.ToolSpec, opts engine.Options) (engine.Completion, error) {
	c, err := r.Engine.Generate(ctx, msgs, tools, opts)
	r.last = c
	return c, err
}

func TestRouterEval(t *testing.T) {
	// Load with a path that doesn't exist returns the shipped defaults, so
	// the report scores exactly what a fresh install runs.
	defs, err := config.Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	baseURL := envOr("MERU_TEST_OLLAMA", defs.Ollama.BaseURL)
	model := envOr("MERU_TEST_FAST_MODEL", defs.Models.Fast)
	cfg, err := ConfigFrom(defs.Router, model)
	if err != nil {
		t.Fatalf("router config: %v", err)
	}

	rows, err := loadLabelled("testdata/routes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	fitRows, heldRows := split(rows)

	real, err := engine.NewOllama(baseURL, "5m", "", nil, nil)
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	eng := &recorder{Engine: real}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	info, err := real.Info(ctx)
	if err != nil {
		t.Skipf("Ollama not reachable at %s: %v", baseURL, err)
	}
	// The first call loads the model, so its time stays out of the numbers.
	if _, err := Decide(ctx, eng, cfg, Turn{Question: "hello"}); err != nil {
		var apiErr *engine.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			t.Skipf("model %s isn't pulled: %v", model, err)
		}
		t.Fatalf("warm-up Decide: %v", err)
	}
	promptTokens := eng.last.Usage.PromptTokens
	// The prompt's size with the eval's folders, then with its tools too, so
	// the report shows what each line costs on every turn.
	var sized [2]int
	for i, tools := range [][]string{nil, evalTools} {
		if _, err := Decide(ctx, eng, cfg, Turn{Question: "hello", Folders: evalFolders, Tools: tools}); err != nil {
			t.Fatalf("sizing Decide: %v", err)
		}
		sized[i] = eng.last.Usage.PromptTokens
	}

	run := func(rows []labelled) []sample {
		var out []sample
		for _, r := range rows {
			start := time.Now()
			if _, err := Decide(ctx, eng, cfg, r.turn()); err != nil {
				t.Fatalf("Decide(%q): %v", r.Q, err)
			}
			out = append(out, sample{Want: r.Route, Comp: eng.last, Latency: time.Since(start)})
		}
		return out
	}
	fit, held := run(fitRows), run(heldRows)

	var b strings.Builder
	fmt.Fprintf(&b, "Ollama %s, model %s, %d fit rows, %d held-out rows\n", info.RuntimeVersion, model, len(fit), len(held))
	fmt.Fprintf(&b, "prompt tokens for a question with no history: %d; with the folders %d; with folders and tools %d\n",
		promptTokens, sized[0], sized[1])
	fmt.Fprintf(&b, "config: temperature %.2f, min_confidence %.2f, fallback %s\n\n", cfg.Temperature, cfg.MinConfidence, cfg.Fallback)
	score(fit, cfg).write(&b, "fit set, config defaults")
	fmt.Fprintln(&b)
	score(held, cfg).write(&b, "held-out set, config defaults")

	// Temperature sweep. Temperature never changes the top pick, only the
	// confidence, so fitting it on every row leaks nothing into the
	// accuracy numbers; the "all" column is the one to fit on, because 36
	// held-out rows alone give a jumpy ECE.
	all := append(append([]sample(nil), fit...), held...)
	grid := []float64{0.5, 0.75, 0.9, 1, 1.1, 1.25, 1.4, 1.5, 1.75, 2, 2.5, 3, 4, 5}
	fitT, fitECE := fitTemperature(fit, cfg, grid)
	heldT, heldECE := fitTemperature(held, cfg, grid)
	allT, allECE := fitTemperature(all, cfg, grid)
	fmt.Fprintf(&b, "\n== temperature sweep (ECE)\n%-6s %8s %8s %8s\n", "T", "fit", "held", "all")
	for i, tt := range grid {
		fmt.Fprintf(&b, "%-6.2f %8.3f %8.3f %8.3f\n", tt, fitECE[i], heldECE[i], allECE[i])
	}
	fmt.Fprintf(&b, "lowest ECE: fit set at T=%.2f, held-out set at T=%.2f, all rows at T=%.2f\n", fitT, heldT, allT)

	// Confidence floor sweep at the configured temperature: what each floor
	// costs in fallbacks and buys in accuracy. Pick the floor on the fit
	// set; the held-out columns show whether the choice carries over.
	fmt.Fprintf(&b, "\n== min_confidence sweep, T=%.2f\n%-6s %-5s %9s %9s %9s %12s\n",
		cfg.Temperature, "floor", "set", "fallback", "accuracy", "missed", "acc. of ok")
	for _, floor := range []float64{0, 0.3, 0.35, 0.4, 0.45, 0.5, 0.55, 0.6, 0.65, 0.7, 0.8, 0.9} {
		c := cfg
		c.MinConfidence = floor
		for _, set := range []struct {
			name    string
			samples []sample
		}{{"fit", fit}, {"held", held}} {
			rep := score(set.samples, c)
			fmt.Fprintf(&b, "%-6.2f %-5s %9.3f %9.3f %9.3f %12.3f\n", floor, set.name,
				rep.FallbackRate, rep.Accuracy, rep.Missed, accuracyOfOK(set.samples, c))
		}
	}

	// The held-out questions the top pick got wrong, to read by eye.
	fmt.Fprintf(&b, "\n== held-out misses (top pick)\n")
	for i, s := range held {
		d := decide(s.Comp, cfg)
		if win, _ := best(d.Probs); win != s.Want {
			fmt.Fprintf(&b, "want %-12s got %-12s [%s] %q\n", s.Want, win, formatProbs(d.Probs), heldRows[i].Q)
		}
	}

	writeRules(&b, cfg, fit, held, append(append([]labelled(nil), fitRows...), heldRows...), all)
	t.Log("\n" + b.String())
}

// ruleGrid is the thresholds the harness tries for each of the marginal
// rule's two questions.
var ruleGrid = []float64{0.2, 0.25, 0.3, 0.35, 0.4, 0.45, 0.5}

// toolsSlack is how far the marginal rule's tool over-offer on the fit set
// may exceed the top rule's before the grid ranks it last.
const toolsSlack = 0.05

// writeRules compares the shipped top-letter rule with the marginal rule
// over the saved completions. It picks the marginal thresholds on the fit
// set (rankGrid), then prints both rules on both sets, the best grid points,
// the email question that prompted the comparison, the connected-server
// rows, and every row the chosen thresholds route differently. rows and all
// hold the same questions in the same order.
func writeRules(b *strings.Builder, cfg Config, fit, held []sample, rows []labelled, all []sample) {
	top := topRule(cfg)
	topFit := scoreRule(fit, top, top)
	var points []gridPoint
	for _, s := range ruleGrid {
		for _, tl := range ruleGrid {
			m := marginalRule(cfg, s, tl)
			points = append(points, gridPoint{Search: s, Tools: tl,
				Fit: scoreRule(fit, m, top), Held: scoreRule(held, m, top)})
		}
	}
	ranked := rankGrid(points, topFit.OverTools+toolsSlack)
	pick := ranked[0]
	chosen := marginalRule(cfg, pick.Search, pick.Tools)
	name := fmt.Sprintf("marginal s=%.2f t=%.2f", pick.Search, pick.Tools)

	fmt.Fprintf(b, "\n== decision rules, T=%.2f (thresholds picked on the fit set: lowest missed with tool over-offer <= top's %.3f + %.2f)\n",
		cfg.Temperature, topFit.OverTools, toolsSlack)
	fmt.Fprintf(b, "%-24s %-5s %8s %7s %11s %10s %8s\n", "rule", "set", "accuracy", "missed", "over-search", "over-tools", "changed")
	// The connected-server rows, from both sets, as a set of their own.
	var connected []sample
	for i, r := range rows {
		if r.Tag == "connected" {
			connected = append(connected, all[i])
		}
	}
	for _, set := range []struct {
		name    string
		samples []sample
	}{{"fit", fit}, {"held", held}, {"all", all}, {"conn", connected}} {
		scoreRule(set.samples, top, top).write(b, fmt.Sprintf("top, floor %.2f", cfg.MinConfidence), set.name)
		// Other floors, for a fair match: a higher floor also misses less,
		// by falling back to search+tools more often.
		for _, floor := range []float64{0, 0.6, 0.7} {
			c := cfg
			c.MinConfidence = floor
			scoreRule(set.samples, topRule(c), top).write(b, fmt.Sprintf("top, floor %.2f", floor), set.name)
		}
		scoreRule(set.samples, chosen, top).write(b, name, set.name)
	}

	fmt.Fprintf(b, "\n== marginal grid, best 10 by the fit-set rule (changed counts rows against top)\n")
	fmt.Fprintf(b, "%-6s %-6s | %-38s | %s\n", "s", "t", "fit: acc  missed  over-s over-t chg", "held: acc  missed  over-s over-t chg")
	for _, g := range ranked[:min(10, len(ranked))] {
		fmt.Fprintf(b, "%-6.2f %-6.2f | %5.3f %7.3f %7.3f %6.3f %3d | %5.3f %7.3f %7.3f %6.3f %3d\n", g.Search, g.Tools,
			g.Fit.Accuracy, g.Fit.Missed, g.Fit.OverSearch, g.Fit.OverTools, g.Fit.Changed,
			g.Held.Accuracy, g.Held.Missed, g.Held.OverSearch, g.Held.OverTools, g.Held.Changed)
	}

	fmt.Fprintf(b, "\n== connected-server rows (want, top, %s)\n", name)
	for i, r := range rows {
		if r.Tag != "connected" {
			continue
		}
		d := decide(all[i].Comp, cfg)
		fmt.Fprintf(b, "%-12s %-12s %-12s [%s] %q\n", r.Route, top(all[i].Comp), chosen(all[i].Comp), formatProbs(d.Probs), r.Q)
	}

	fmt.Fprintf(b, "\n== rows %s routes differently from top (want, top, marginal)\n", name)
	for i, r := range rows {
		if chosen(all[i].Comp) == top(all[i].Comp) {
			continue
		}
		d := decide(all[i].Comp, cfg)
		fmt.Fprintf(b, "%-12s %-12s %-12s [%s] %q\n", r.Route, top(all[i].Comp), chosen(all[i].Comp), formatProbs(d.Probs), r.Q)
	}
}

// accuracyOfOK is the accuracy over the turns that didn't fall back: how
// often the router is right when it trusts itself.
func accuracyOfOK(samples []sample, cfg Config) float64 {
	var ok, right int
	for _, s := range samples {
		d := decide(s.Comp, cfg)
		if d.Outcome != OutcomeOK {
			continue
		}
		ok++
		if d.Route == s.Want {
			right++
		}
	}
	return ratio(float64(right), ok)
}
