// Package router picks the route for one turn: answer directly, search the
// user's files, call tools, or both.
//
// It asks the fast model for a single token and reads the route from the
// probabilities the model gave the four option letters, instead of parsing
// text the model wrote. See ARCHITECTURE.md, "Agent loop" → "Routing", and
// the full design in docs/fast-router.md.
//
// What it doesn't do: rewrite the query or pick skills (those need generated
// text and stay in their own call), fit the temperature at run time (the
// test harness behind `make router-eval` fits it offline and the value goes
// in config), or treat an unclear answer as an error. An unsure model gets
// the fallback route and an outcome that says why.
package router
