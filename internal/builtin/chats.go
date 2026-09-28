// This file lets the read-only file tools read the user's past chats: the
// session transcripts under ~/.meru/sessions, one JSON Lines file per chat.
// A model asked "what did we talk about last month?" had only the three
// sessions that recall put in its prompt, and told the user it had no
// access to its past conversations. With the sessions folder as one more
// folder the file tools read, list_folder, grep and read_file reach every
// chat. The indexer never indexes the folder, so no chat reaches search.
// See ARCHITECTURE.md, "Facts and episodes".

package builtin

import (
	"path/filepath"
	"slices"
)

// sessionsLabel follows the sessions folder where list_folder lists the
// folders the tools read, so the model knows what the files are.
const sessionsLabel = "past chats with the user, one JSONL file per chat"

// ReadSessions lets list_folder, grep and read_file read dir, the folder
// that holds the session transcripts, under the same rules as the other
// folders they read: no symlink followed, secret and binary files skipped,
// and the [index] max_file_mb cap. The indexer's ReadChats adds it, so Scan
// and the watcher never see it and nothing in it reaches the store.
// ReadChats also lets the tools read .jsonl files, but in this folder
// alone: in every other folder they stay skipped, as the indexer skips
// them.
//
// dir sits under the hidden ~/.meru, and the hidden rule would refuse it
// inside an [index] folder. The rules apply from the folder a path sits in
// downwards, though, so as a folder of its own dir is readable, and a
// hidden file inside it is still skipped.
//
// merud calls it once at startup, before any tool call, as ReadChats asks.
// Without it, or with no indexer, the tools don't reach the chats. It is
// a method rather than a parameter of New, as UseSearch is, so the tests
// that build Tools without a sessions folder don't change.
func (t *Tools) ReadSessions(dir string) {
	if t.files == nil || dir == "" {
		return
	}
	t.sessionsDir = filepath.Clean(dir)
	t.files.ReadChats(t.sessionsDir)
}

// isSessions reports whether root, a folder as the indexer's Roots gives
// it, is the sessions folder. Roots resolves symlinks, so the sessions
// folder is resolved too before the two are compared.
func (t *Tools) isSessions(root string) bool {
	if t.sessionsDir == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(t.sessionsDir)
	if err != nil {
		return false // the folder doesn't exist yet
	}
	return resolved == root
}

// fileRoots returns the folders grep searches when the model passes no
// path: every folder the tools read but the sessions folder. A chat holds
// the first 4,000 characters of each tool result, the text of the files
// its tools read included, so a grep of the user's files would match many
// lines twice, once in the file and once in the chat, and the copies would
// use up max_results. The model greps the chats by passing their folder as
// path.
func (t *Tools) fileRoots() []string {
	// DeleteFunc drops, in place, each root the function returns true for.
	return slices.DeleteFunc(t.files.Roots(), t.isSessions)
}
