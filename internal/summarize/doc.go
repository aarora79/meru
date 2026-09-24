// Package summarize writes the episode memory of ARCHITECTURE.md, "Facts
// and episodes". Once a session has had no question for [agent]
// summary_idle, merud asks the fast model for a one- or two-sentence
// summary of it and appends that as a summary line to the transcript. The
// store replays the line into sessions and summary_fts, and this package
// embeds it into session_vec, so a later turn can recall the session by
// meaning.
//
// A Summarizer runs as one goroutine in merud. Every minute it summarizes
// at most five quiet sessions, newest first, and embeds the summaries that
// lack a vector.
//
// What this package doesn't do: it doesn't search. retrieve.SearchSessions
// recalls past sessions, and the agent decides when a turn does. It never
// rewrites a transcript either: a session that grows after its summary gets
// a new summary line later, and the newest one wins.
package summarize
