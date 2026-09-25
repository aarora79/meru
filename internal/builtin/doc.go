// Package builtin holds the tools built into merud, as one dispatch.Backend.
// There are ten: configure, which adds an MCP server to config.toml when
// you ask in chat ("connect my Gmail"); datetime, which reads the clock;
// remember, which saves one fact about you as a memory file; write_file,
// which saves a file the model made inside [skills] output_dir; read_file,
// list_folder and grep, which read the [index] folders and the output
// folder; search_files, which
// runs Meru's hybrid search over them; and web_search and web_fetch, which
// search the web through the user's SearXNG and read, answer from or
// download one public page.
//
// [builtin] tools in config.toml lists the ones the model may use; all ten
// by default. A tool it leaves out isn't offered, listed or run. A listed
// tool whose setting is missing, such as the file tools with no [index]
// folders, stays off too, and Off says why so merud can log it. See
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
// [builtin] confirm lists them. They reach only the [index] folders and
// [skills] output_dir, through the indexer's own Check, Walk and ReadText,
// so they skip what the indexer skips: symlinks, secrets, hidden, ignored,
// binary and oversized files. Outside the output folder, the model can read
// no file that search couldn't already put in its prompt. The output folder
// holds what write_file wrote, what web_fetch downloaded, and the mail
// attachments the google server saves in its attachments folder; read_file
// takes an attachment's bare saved filename.
//
// search_files runs retrieve.Search, the search a turn runs before the
// answer, through the FileSearcher merud hands UseSearch, and returns
// numbered excerpts with their citations in Result.Sources. It takes its
// numbers from dispatch.CiteNumbers, so they follow the excerpts already in
// the turn. It only reads the index, and runs without asking unless
// [builtin] confirm lists it. With [index] retrieval = "agentic" it is how
// a turn finds text by meaning, since no search runs before the answer.
//
// web_search talks only to [web] searxng_url, which config holds to
// loopback, with no proxy and no redirects. It runs without asking unless
// [builtin] confirm lists it. web_fetch is on while [builtin] tools lists it.
// It is the one tool that makes merud connect off this machine, so its
// dialer checks every address after DNS, just before the connection opens,
// and refuses loopback, private, link-local and other non-public addresses,
// redirects included. With a prompt, it asks the fast model to answer from
// the page. With save, it downloads the file into <output_dir>/downloads.
// Its guard, ConfirmCall, lets it run without asking only for a URL that a
// web_search result or the user's own question showed in the same session;
// any other URL, and every download, asks first, because a URL the model
// made up can carry the user's data out.
//
// What it doesn't do: it never takes an API key. A server that needs a key
// not yet in secrets.toml isn't written; the tool tells the model to send
// the user to `meru mcp add <name>` in a terminal, so keys never pass
// through the model or the transcript. For the same reason remember refuses
// a fact that holds a value from secrets.toml. The package also doesn't
// restart servers itself: it calls the onChange hook merud gives it, and
// merud decides how to reload.
package builtin
