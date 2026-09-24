// Package builtin holds the tools built into merud, as one dispatch.Backend.
// There are three: configure, which adds an MCP server to config.toml when
// you ask in chat ("connect my Gmail"); remember, which saves one fact about
// you as a memory file; and write_file, which saves a file the model made
// inside [skills] output_dir. See ARCHITECTURE.md, "First run and setup",
// "Approving a tool call", "Memory" and "Built-in skills".
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
// What it doesn't do: it never takes an API key. A server that needs a key
// not yet in secrets.toml isn't written; the tool tells the model to send
// the user to `meru mcp add <name>` in a terminal, so keys never pass
// through the model or the transcript. For the same reason remember refuses
// a fact that holds a value from secrets.toml. The package also doesn't
// restart servers itself: it calls the onChange hook merud gives it, and
// merud decides how to reload.
package builtin
