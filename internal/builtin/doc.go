// Package builtin holds the tools built into merud, as one dispatch.Backend.
// There are eight: configure, which adds an MCP server to config.toml when
// you ask in chat ("connect my Gmail"); remember, which saves one fact about
// you as a memory file; write_file, which saves a file the model made
// inside [skills] output_dir; read_file, list_folder and grep, which read
// the [index] folders; and web_search and web_url_read, which search the
// web through the user's SearXNG and read one public page. See
// ARCHITECTURE.md, "First run and setup", "Approving a tool call", "Memory",
// "Built-in skills" and "Web search".
//
// configure always asks. dispatch shows the user the exact arguments and
// offers only "approve once" and "deny", whatever [builtin] confirm says,
// because config grants lasting trust and the model mustn't grant any to
// itself. It writes through catalog.AppendServer, the same code `meru mcp
// add` uses, so the two paths can't drift apart.
//
// remember saves without asking unless [builtin] confirm lists it. It
// writes through memory.Store.Add, the same code `meru memory add` reaches
// through merud, and records the chat's session as the memory's source.
// After a save it runs merud's onRemember hook, which syncs the new memory
// into the store so the next turn can recall it.
//
// write_file asks before each call, because the shipped [builtin] confirm
// lists it. It writes only inside the output folder, through an os.Root:
// no absolute path, no "..", no symbolic link, at most 1 MiB, and it
// replaces a file only when the model passes overwrite. It returns the
// absolute path, so the model can tell you where the file is.
//
// read_file, list_folder and grep only read, and run without asking unless
// [builtin] confirm lists them. They reach only the [index] folders, through
// the indexer's own Check, Walk and ReadText, so they skip what the indexer
// skips: symlinks, secrets, hidden, ignored, binary and oversized files. The
// model can read no file that search couldn't already put in its prompt.
//
// web_search talks only to [web] searxng_url, which config holds to
// loopback, with no proxy and no redirects. web_url_read exists only when
// [web] read_pages is true. It is the one tool that makes merud connect off
// this machine, so its dialer checks every address after DNS, just before
// the connection opens, and refuses loopback, private, link-local and
// other non-public addresses, redirects included. Both run without asking
// unless [builtin] confirm lists them.
//
// What it doesn't do: it never takes an API key. A server that needs a key
// not yet in secrets.toml isn't written; the tool tells the model to send
// the user to `meru mcp add <name>` in a terminal, so keys never pass
// through the model or the transcript. For the same reason remember refuses
// a fact that holds a value from secrets.toml. The package also doesn't
// restart servers itself: it calls the onChange hook merud gives it, and
// merud decides how to reload.
package builtin
