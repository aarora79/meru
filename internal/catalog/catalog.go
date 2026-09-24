// This file holds the catalog itself: the Entry and Need types and the list
// of starter servers.
//
// Tool names come from each server's source code, because tools are
// deny-by-default and an allow entry that doesn't match a real tool name
// gives the model nothing. Checked on 2026-09-24 against:
//
//   - google: https://github.com/taylorwilsdon/google_workspace_mcp,
//     workspace-mcp 1.28.0 (commit 138effb, 2026-09-23); names from
//     core/tool_tiers.yaml and the functions in gmail/gmail_tools.py,
//     gcalendar/calendar_tools.py, gdrive/drive_tools.py and
//     gdocs/docs_tools.py. main.py reads --tools as a list, so one process
//     serves several services. With --transport streamable-http it listens
//     on port 8000 (WORKSPACE_MCP_PORT changes it) and serves MCP at /mcp;
//     without OAuth 2.1 it binds 127.0.0.1 unless WORKSPACE_MCP_HOST says
//     otherwise. It reads GOOGLE_OAUTH_CLIENT_ID and
//     GOOGLE_OAUTH_CLIENT_SECRET, and on first use sends a sign-in link
//     whose callback is http://localhost:8000/oauth2callback.
//   - brave: https://github.com/brave/brave-search-mcp-server, package
//     @brave/brave-search-mcp-server 2.1.4; names from src/tools/web and
//     src/tools/news (index.ts, `export const name`).
//   - obsidian: https://github.com/MarkusPfundstein/mcp-obsidian,
//     mcp-obsidian 0.2.2; names from src/mcp_obsidian/tools.py. The README
//     lists them without the "obsidian_" prefix, but the server sends it.
//
// The catalog holds three servers, one per kind of example the docs use:
// mail and calendar (google), web search (brave) and notes (obsidian).
//
// google is the one url entry. Its README calls stdio legacy, and its
// OAuth 2.1 mode needs HTTP, so the user runs it as a Streamable HTTP
// server. merud never starts, restarts or watches it; it connects to the
// URL, like any client (ARCHITECTURE.md, "MCP"). The server offers 120-odd
// tools; --tools limits the process to four services, and allow limits the
// model to a handful of those.
//
// Google also runs its own MCP servers for Gmail, Drive, Docs and Calendar
// (developer preview since 2026-05-01; see
// https://developers.google.com/workspace/guides/configure-mcp-servers).
// They are remote only, at *mcp.googleapis.com, and need an OAuth sign-in
// that Meru's client doesn't do. workspace-mcp runs on this machine with
// your own OAuth client, so the catalog keeps it.
//
// The commands aren't pinned to a version: npx and uvx fetch the latest
// release, which keeps security fixes coming. A later release that adds a
// tool gives the model nothing new, because allow names each tool.

package catalog

import (
	"maps"
	"slices"

	"github.com/aarora79/meru/internal/secrets"
)

// Kinds of Need.
const (
	// NeedAPIKey asks for a key and stores it in secrets.toml under
	// SecretName. The key never goes into config.toml.
	NeedAPIKey = "api_key"
	// NeedPath asks for a file or folder path and puts it in the entry's
	// env variable Env.
	NeedPath = "path"
	// NeedURL asks for a URL and puts it in the entry's env variable Env.
	NeedURL = "url"
	// NeedNote asks nothing; it tells the user something they must do, such
	// as sign in to Google in a browser.
	NeedNote = "note"
)

// Transports an Entry can use.
const (
	TransportStdio = "stdio" // merud starts Command
	TransportHTTP  = "http"  // merud connects to URL
)

// Need is one thing a server needs from the user before it can run.
type Need struct {
	// Kind is NeedAPIKey, NeedPath, NeedURL or NeedNote.
	Kind string
	// SecretName is the secrets.toml entry an api_key goes into.
	SecretName string
	// Env is the env variable a path or url answer goes into.
	Env string
	// Prompt is the question, or for a note the thing to do.
	Prompt string
	// Help says where to get the answer, such as the page that issues keys.
	Help string
}

// Entry is one server the catalog knows, or one the user named by hand
// (see Custom).
type Entry struct {
	// Name is the entry's name in config.toml, and the prefix of its tools:
	// "<name>.<tool>".
	Name string
	// Title and Description are for people: a short name and one line on
	// what the server gives the model.
	Title       string
	Description string
	// Transport is TransportStdio (Command and Args) or TransportHTTP (URL).
	Transport string
	Command   string
	Args      []string
	URL       string
	// Remote lets merud connect to a URL that isn't loopback.
	Remote bool
	// Start is the command the user runs to start an HTTP server. merud
	// never runs it; `meru mcp add` prints it.
	Start string
	// Env holds the environment for a stdio server. A "secret:<name>" value
	// comes from secrets.toml when merud starts the server. A url entry has
	// none: merud doesn't start that server, so it can't set its
	// environment.
	Env map[string]string
	// Needs lists what to ask the user, in order. Requires sums them up in
	// a few words for `meru mcp list`, such as "a Brave Search API key".
	Needs    []Need
	Requires string
	// Allow lists the tools the model may call; Confirm the allowed tools
	// that ask before each call.
	Allow   []string
	Confirm []string
	// AlwaysConfirm lists allowed tools that ask before every call, with no
	// approval for the session: the tools that run commands.
	AlwaysConfirm []string
	// Install says what to install first, printed by "Show me how".
	Install string
	// Docs is the server's upstream page.
	Docs string
}

// googleStart is the command that starts the google server. The user runs
// it, in a terminal or from launchd or systemd, with their own OAuth
// client; merud never does. --tools limits the process to the four
// services the allow list draws from.
const googleStart = "GOOGLE_OAUTH_CLIENT_ID=<your client ID> GOOGLE_OAUTH_CLIENT_SECRET=<your client secret> " +
	"uvx workspace-mcp --transport streamable-http --tools gmail calendar drive docs"

// installUV is the Install text for the servers that run with uvx.
const installUV = "uvx downloads the server the first time it starts. " +
	"Install uv, which provides uvx: https://docs.astral.sh/uv/getting-started/installation/"

// installNode is the Install text for the servers that run with npx.
const installNode = "npx downloads the server the first time merud starts it. " +
	"Install Node.js, which provides npx: https://nodejs.org/"

// Entries returns the catalog, in the order setup offers it. It builds the
// list on each call, so a caller that changes an entry changes only its
// own copy.
func Entries() []Entry {
	return []Entry{
		{
			Name:  "google",
			Title: "Gmail, Google Calendar, Drive and Docs",
			Description: "Searches and reads your mail, lists calendar events, searches Drive and reads Docs; " +
				"sending mail and changing an event ask first. You start and run this server; Meru never does.",
			Transport: TransportHTTP,
			URL:       "http://127.0.0.1:8000/mcp",
			Start:     googleStart,
			Needs: []Need{
				{
					Kind: NeedNote,
					Prompt: "You start this server and keep it running; Meru only connects to it. " +
						"Start it in another terminal, or from launchd or systemd: " + googleStart,
					Help: "In Google Cloud Console, turn on the Gmail, Calendar, Drive and Docs APIs, then create an OAuth client " +
						"of type \"Desktop app\" under APIs & Services > Credentials. Steps: https://workspacemcp.com/quick-start",
				},
				{Kind: NeedNote, Prompt: "The first time the model uses a Google tool, the server gives you a link. Sign in to Google there."},
			},
			Requires: "a Google OAuth client, uv, and the server running (you start it)",
			Allow: []string{
				"search_gmail_messages", "get_gmail_message_content", "get_gmail_thread_content", "send_gmail_message",
				"get_events", "manage_event",
				"search_drive_files", "get_doc_content",
			},
			Confirm: []string{"send_gmail_message", "manage_event"},
			Install: installUV + " Then start the server yourself: " + googleStart,
			Docs:    "https://github.com/taylorwilsdon/google_workspace_mcp",
		},
		{
			Name:        "brave",
			Title:       "Web search (Brave Search)",
			Description: "Searches the web and the news with the Brave Search API.",
			Transport:   TransportStdio,
			Command:     "npx",
			Args:        []string{"-y", "@brave/brave-search-mcp-server", "--transport", "stdio"},
			Env:         map[string]string{"BRAVE_API_KEY": secrets.Prefix + "brave_api_key"},
			// #nosec G101 -- the name of a secrets.toml entry, not a credential
			Needs: []Need{{
				Kind:       NeedAPIKey,
				SecretName: "brave_api_key",
				Prompt:     "Paste your Brave Search API key",
				Help:       "Get a key at https://brave.com/search/api/ (the free plan works).",
			}},
			Requires: "a Brave Search API key, and Node.js",
			Allow:    []string{"brave_web_search", "brave_news_search"},
			Install:  installNode,
			Docs:     "https://github.com/brave/brave-search-mcp-server",
		},
		{
			Name:        "obsidian",
			Title:       "Obsidian",
			Description: "Lists, searches and reads the notes in your open Obsidian vault; appending to a note asks first.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"mcp-obsidian"},
			Env: map[string]string{
				"OBSIDIAN_API_KEY": secrets.Prefix + "obsidian_api_key",
				"OBSIDIAN_HOST":    "127.0.0.1",
				"OBSIDIAN_PORT":    "27124",
			},
			Needs: []Need{
				{
					Kind:       NeedAPIKey,
					SecretName: "obsidian_api_key",
					Prompt:     "Paste the API key from Obsidian's Local REST API plugin",
					Help: "In Obsidian, install and turn on the community plugin \"Local REST API\", " +
						"then copy the key from its settings page.",
				},
				{Kind: NeedNote, Prompt: "Obsidian must be running when Meru uses these tools."},
			},
			Requires: "Obsidian's Local REST API plugin, and uv",
			Allow: []string{
				"obsidian_list_files_in_vault", "obsidian_list_files_in_dir", "obsidian_get_file_contents",
				"obsidian_simple_search", "obsidian_append_content",
			},
			Confirm: []string{"obsidian_append_content"},
			Install: "Turn on Obsidian's \"Local REST API\" plugin. " + installUV,
			Docs:    "https://github.com/MarkusPfundstein/mcp-obsidian",
		},
	}
}

// Find returns the catalog entry called name, and false when there is none.
func Find(name string) (Entry, bool) {
	for _, e := range Entries() {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Names returns the catalog's entry names, in catalog order.
func Names() []string {
	var names []string
	for _, e := range Entries() {
		names = append(names, e.Name)
	}
	return names
}

// SecretNames returns the secrets.toml entries e refers to in its env and
// Needs, sorted, each once.
func (e Entry) SecretNames() []string {
	set := map[string]bool{}
	for _, v := range e.Env {
		if name, ok := secrets.Name(v); ok {
			set[name] = true
		}
	}
	for _, n := range e.Needs {
		if n.Kind == NeedAPIKey {
			set[n.SecretName] = true
		}
	}
	// maps.Keys yields the keys in random order; slices.Sorted collects
	// and sorts them.
	return slices.Sorted(maps.Keys(set))
}
