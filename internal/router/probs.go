// This file turns the model's log probabilities into a distribution over
// the four routes, and picks the winner. It is pure arithmetic, with no I/O,
// so the tests can check it number by number.

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
// flattens the distribution; small models are overconfident, and a fitted
// value for a 2B model usually lands between 2 and 2.5. Temperature never
// changes which route wins, only how sure the router claims to be.
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
