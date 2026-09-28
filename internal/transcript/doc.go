// Package transcript writes and reads session transcripts: one JSON Lines
// (JSONL) file per session under ~/.meru/sessions/YYYY/MM/, one JSON object
// per line, one line per event.
//
// Transcripts are Meru's source of truth for conversations (see
// ARCHITECTURE.md, "Storage" and "Session transcripts"). A later database
// indexes them and can always be rebuilt from them.
//
// It also holds what the user sets on a chat: its folder and tags, in
// "meta" lines, the list of chat folders in folders.json, and Delete, which
// removes a chat's whole file when the user asks. An incognito session
// keeps its lines in memory and writes no file at all.
//
// What this package deliberately doesn't do: it never rewrites or deletes a
// line once written (Delete removes a whole file, never a line), keeps no
// index or cache, and knows nothing about models.
package transcript
