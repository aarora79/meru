// This file turns the model's log probabilities into a distribution over
// the four routes, and picks the route by either rule: the top letter, or
// the two yes/no questions. It is pure arithmetic, with no I/O, so the tests
// can check it number by number.

package router

import (
	"math"

	"github.com/aarora79/meru/internal/engine"
)

// probs turns the alternatives at one position into a distribution over
// routes. It returns nil when no alternative is a route letter.
//
// Ollama reports each token's natural logarithm. math.Exp turns it back into
// a probability. Dividing the log probability by a temperature above 1
// flattens the distribution, and one below 1 sharpens it. `make router-eval`
// fits the value on labelled questions; with the current prompt MiniCPM5-2B
// needs 1.25. Temperature never changes which route wins, only how sure the
// router claims to be.
//
// When two tokens map to the same route, such as "A" and " A", their
// probabilities add: both are ways of saying A.
//
// map[Route]float64 is Go's hash map, here from a route to its probability.
// Reading a missing key gives the zero value, 0.
func probs(pos engine.PositionLogProbs, temp float64) map[Route]float64 {
	raw := map[Route]float64{}
	for _, alt := range pos.Top {
		r, ok := routeForLetter(alt.Token)
		if !ok {
			continue // not one of our letters; ignore it
		}
		raw[r] += math.Exp(alt.LogProb / temp)
	}
	if len(raw) == 0 {
		return nil
	}
	sum := 0.0
	for _, v := range raw {
		sum += v
	}
	if sum == 0 {
		// Every letter's probability rounded to zero, so there is nothing
		// to normalise. Report it as no letters at all.
		return nil
	}
	for r := range raw {
		raw[r] /= sum // normalise so the routes sum to 1
	}
	return raw
}

// best returns the most likely route in p and its probability. On a tie the
// route whose letter comes first wins, so the same input always gives the
// same answer; Go walks a map in random order, so we walk options instead.
// It returns "" and 0 for an empty p.
func best(p map[Route]float64) (Route, float64) {
	var win Route
	top := -1.0
	for _, o := range options {
		v, ok := p[o.route] // ok reports whether the key is present
		if ok && v > top {
			win, top = o.route, v
		}
	}
	if win == "" {
		return "", 0
	}
	return win, top
}

// marginal applies RuleMarginal to p. It returns the route and how sure the
// weaker of its two answers was.
//
// B and D both search, so P(search) = P(B) + P(D); C and D both call tools,
// so P(tools) = P(C) + P(D). Each is a yes at or above its threshold. Take
// "what was the last email I sent?" at direct 0.049, search 0.473, tools
// 0.260, search+tools 0.218: the top letter is search, and that turn offered
// no mail tools. Asked as two questions, P(tools) is 0.478, above a 0.25
// bar, so the route is search+tools. See docs/fast-router.md, "Decision
// rules", for how the thresholds were picked.
func marginal(p map[Route]float64, searchT, toolsT float64) (Route, float64) {
	ps := p[RouteSearch] + p[RouteSearchTools]
	pt := p[RouteTools] + p[RouteSearchTools]
	search, tools := ps >= searchT, pt >= toolsT
	// A no is as sure as the chance of a yes is small.
	if !search {
		ps = 1 - ps
	}
	if !tools {
		pt = 1 - pt
	}
	return routeFor(search, tools), min(ps, pt)
}

// routeFor returns the route that offers a search when search is true and
// tools when tools is true.
func routeFor(search, tools bool) Route {
	switch {
	case search && tools:
		return RouteSearchTools
	case search:
		return RouteSearch
	case tools:
		return RouteTools
	}
	return RouteDirect
}
