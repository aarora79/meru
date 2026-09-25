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
//   - obsidian: https://github.com/MarkusPfundstein/mcp-obsidian,
//     mcp-obsidian 0.2.2; names from src/mcp_obsidian/tools.py. The README
//     lists them without the "obsidian_" prefix, but the server sends it.
//
// The catalog holds two servers, one per kind of example the docs use:
// mail and calendar (google) and notes (obsidian). Web search is a
// built-in tool, through a SearXNG instance the user runs, so it needs no
// catalog entry (ARCHITECTURE.md, "Web search").
//
// google is the one url entry. Its README calls stdio legacy, and its
// OAuth 2.1 mode needs HTTP, so the user runs it as a Streamable HTTP
// server. merud never starts, restarts or watches it; it connects to the
// URL, like any client (ARCHITECTURE.md, "MCP"). The server offers 120-odd
// tools; --tools limits the process to four services, --tool-tier to 45
// tools of those, and allow limits the model to a handful.
//
// Google also runs its own MCP servers for Gmail, Drive, Docs and Calendar
// (developer preview since 2026-05-01; see
// https://developers.google.com/workspace/guides/configure-mcp-servers).
// They are remote only, at *mcp.googleapis.com, and need an OAuth sign-in
// that Meru's client doesn't do. workspace-mcp runs on this machine with
// your own OAuth client, so the catalog keeps it.
//
// The commands aren't pinned to a version: uvx fetches the latest
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
	// a few words for `meru mcp list`, such as "Obsidian's Local REST API
	// plugin, and uv".
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
//
// --tool-tier extended loads the core and extended tools of those four
// services, 45 of them (core/tool_tiers.yaml in workspace-mcp 1.29). It is
// the smallest tier that holds every tool in Allow: get_gmail_thread_content
// and get_gmail_attachment_content sit in the extended tier, so a server
// started with --tool-tier core offers neither, and meru tools warns that
// allow names a tool google doesn't offer. Without the flag the server
// loads every tool of the four services, or the tier that
// WORKSPACE_MCP_TOOL_TIER names. The flag wins over that variable, so the
// user's shell can't change the tier.
//
// USER_GOOGLE_EMAIL makes each tool's user_google_email argument
// optional, with that address as the default (core/server.py in
// workspace-mcp 4.0.9), so the model needn't know it.
//
// WORKSPACE_ATTACHMENT_DIR tells the server where get_gmail_attachment_content
// saves an attachment (core/attachment_storage.py in workspace-mcp 1.29).
// It points at the attachments folder under the default [skills]
// output_dir, which Meru's read_file may read, so the model can read a
// PDF it fetched from a mail. A shell expands the "~" in the assignment,
// and the server expands it again with Python's expanduser, so the line
// also works from launchd or systemd, where no shell runs. The server
// deletes each saved file after an hour.
const googleStart = "USER_GOOGLE_EMAIL=<your Google address> " +
	"WORKSPACE_ATTACHMENT_DIR=~/meru-output/attachments " +
	"GOOGLE_OAUTH_CLIENT_ID=<your client ID> GOOGLE_OAUTH_CLIENT_SECRET=<your client secret> " +
	"uvx workspace-mcp --transport streamable-http --tools gmail calendar drive docs --tool-tier extended"

// installUV is the Install text for the servers that run with uvx.
const installUV = "uvx downloads the server the first time it starts. " +
	"Install uv, which provides uvx: https://docs.astral.sh/uv/getting-started/installation/"

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
				{
					Kind: NeedNote,
					Prompt: "The server saves a mail attachment the model asks for in ~/meru-output/attachments, where read_file can read it, " +
						"and deletes it after an hour. If you changed [skills] output_dir, point WORKSPACE_ATTACHMENT_DIR at its attachments folder.",
				},
			},
			Requires: "a Google OAuth client, uv, and the server running (you start it)",
			// get_gmail_attachment_content comes last: the router names a
			// server by the nouns in its first allowed tools, and "drive"
			// matters more there than "attachment".
			Allow: []string{
				"search_gmail_messages", "get_gmail_message_content", "get_gmail_thread_content", "send_gmail_message",
				"get_events", "manage_event",
				"search_drive_files", "get_doc_content",
				"get_gmail_attachment_content",
			},
			Confirm: []string{"send_gmail_message", "manage_event"},
			Install: installUV + " Then start the server yourself: " + googleStart,
			Docs:    "https://github.com/taylorwilsdon/google_workspace_mcp",
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
