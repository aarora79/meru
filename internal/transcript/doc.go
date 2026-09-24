// Package transcript writes and reads session transcripts: one JSON Lines
// (JSONL) file per session under ~/.meru/sessions/YYYY/MM/, one JSON object
// per line, one line per event.
//
// Transcripts are Meru's source of truth for conversations (see
// ARCHITECTURE.md, "Storage" and "Session transcripts"). A later database
// indexes them and can always be rebuilt from them.
//
// What this package deliberately doesn't do: it never rewrites or deletes a
// line once written, keeps no index or cache, and knows nothing about models.
// v0.1 writes only "user" and "assistant" lines; tool calls, approvals and
// summaries arrive with the milestones that need them.
package transcript
