// This file holds the catalog itself: the Entry and Need types and the list
// of starter servers.
//
// Tool names come from each server's source code, because tools are
// deny-by-default and an allow entry that doesn't match a real tool name
// gives the model nothing. Checked on 2026-09-24 against:
//
//   - brave: https://github.com/brave/brave-search-mcp-server, package
//     @brave/brave-search-mcp-server 2.1.4; names from src/tools/web and
//     src/tools/news (index.ts, `export const name`).
//   - fetch: https://github.com/modelcontextprotocol/servers/tree/main/src/fetch,
//     mcp-server-fetch 0.6.3; name from src/mcp_server_fetch/server.py.
//   - gmail, calendar, drive: https://github.com/taylorwilsdon/google_workspace_mcp,
//     workspace-mcp 1.28.0; names from core/tool_tiers.yaml and the async
//     functions in gmail/gmail_tools.py, gcalendar/calendar_tools.py,
//     gdrive/drive_tools.py and gdocs/docs_tools.py.
//   - obsidian: https://github.com/MarkusPfundstein/mcp-obsidian,
//     mcp-obsidian 0.2.2; names from src/mcp_obsidian/tools.py. The README
//     lists them without the "obsidian_" prefix, but the server sends it.
//
// One Google Workspace server covers Gmail, Calendar, Drive and Docs. The
// catalog still has three entries that run it with a different --tools
// flag. You add only the services you want, and each server process asks
// Google for the permissions of its own services and no others. The cost is
// one small process per entry.
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
	// Network allows a URL that isn't loopback.
	Network bool
	// Env holds the environment for a stdio server. A "secret:<name>" value
	// comes from secrets.toml when merud starts the server.
	Env map[string]string
	// Needs lists what to ask the user, in order.
	Needs []Need
	// Allow lists the tools the model may call; Confirm the allowed tools
	// that ask before each call.
	Allow   []string
	Confirm []string
	// Install says what to install first, printed by "Show me how".
	Install string
	// Docs is the server's upstream page.
	Docs string
}

// googleNeeds returns what every Google Workspace entry needs: an OAuth
// client ID and secret, then a sign-in on first use. It builds a new slice
// on each call, so no two entries share one.
func googleNeeds(api string) []Need {
	help := "In Google Cloud Console, turn on the " + api + ", then create an OAuth client of type " +
		"\"Desktop app\" under APIs & Services > Credentials. Steps: https://workspacemcp.com/quick-start"
	return []Need{
		// #nosec G101 -- names of secrets.toml entries, not credentials
		{Kind: NeedAPIKey, SecretName: "google_oauth_client_id", Prompt: "Paste your Google OAuth client ID", Help: help},
		// #nosec G101 -- names of secrets.toml entries, not credentials
		{Kind: NeedAPIKey, SecretName: "google_oauth_client_secret", Prompt: "Paste your Google OAuth client secret", Help: help},
		{Kind: NeedNote, Prompt: "The first time Meru uses this server, it opens a browser window or gives you a link. Sign in to Google there."},
	}
}

// googleEnv returns the env block every Google Workspace entry uses.
func googleEnv() map[string]string {
	return map[string]string{
		"GOOGLE_OAUTH_CLIENT_ID":     secrets.Prefix + "google_oauth_client_id",
		"GOOGLE_OAUTH_CLIENT_SECRET": secrets.Prefix + "google_oauth_client_secret",
	}
}

// installUV is the Install text for the servers that run with uvx.
const installUV = "uvx downloads the server the first time merud starts it. " +
	"Install uv, which provides uvx: https://docs.astral.sh/uv/getting-started/installation/"

// Entries returns the catalog, in the order setup offers it. It builds the
// list on each call, so a caller that changes an entry changes only its
// own copy.
func Entries() []Entry {
	google := "https://github.com/taylorwilsdon/google_workspace_mcp"
	return []Entry{
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
			Allow:   []string{"brave_web_search", "brave_news_search"},
			Install: "npx downloads the server the first time merud starts it. Install Node.js, which provides npx: https://nodejs.org/",
			Docs:    "https://github.com/brave/brave-search-mcp-server",
		},
		{
			Name:        "fetch",
			Title:       "Web page fetch",
			Description: "Reads a web page and hands it to the model as Markdown.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"mcp-server-fetch"},
			Allow:       []string{"fetch"},
			Install:     installUV,
			Docs:        "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch",
		},
		{
			Name:        "gmail",
			Title:       "Gmail",
			Description: "Searches and reads your mail; drafting and sending ask first.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"workspace-mcp", "--tools", "gmail"},
			Env:         googleEnv(),
			Needs:       googleNeeds("Gmail API"),
			Allow: []string{
				"search_gmail_messages", "get_gmail_message_content", "get_gmail_messages_content_batch",
				"get_gmail_thread_content", "list_gmail_labels", "draft_gmail_message", "send_gmail_message",
			},
			Confirm: []string{"draft_gmail_message", "send_gmail_message"},
			Install: installUV,
			Docs:    google,
		},
		{
			Name:        "calendar",
			Title:       "Google Calendar",
			Description: "Lists your calendars and events; creating or changing an event asks first.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"workspace-mcp", "--tools", "calendar"},
			Env:         googleEnv(),
			Needs:       googleNeeds("Google Calendar API"),
			Allow:       []string{"list_calendars", "get_events", "query_freebusy", "manage_event"},
			Confirm:     []string{"manage_event"},
			Install:     installUV,
			Docs:        google,
		},
		{
			Name:        "drive",
			Title:       "Google Drive and Docs",
			Description: "Searches and reads your Drive files and Docs; creating or editing a doc asks first.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"workspace-mcp", "--tools", "drive", "docs"},
			Env:         googleEnv(),
			Needs:       googleNeeds("Google Drive API and Google Docs API"),
			Allow: []string{
				"search_drive_files", "get_drive_file_content", "list_drive_items",
				"get_doc_content", "get_doc_as_markdown", "search_docs", "list_docs_in_folder",
				"create_doc", "modify_doc_text",
			},
			Confirm: []string{"create_doc", "modify_doc_text"},
			Install: installUV,
			Docs:    google,
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
