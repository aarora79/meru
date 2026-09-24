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
//   - filesystem: https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem,
//     npm @modelcontextprotocol/server-filesystem 2026.8.31; names and hints
//     from src/filesystem/index.ts (server.registerTool). The server marks
//     ten tools read-only and four as writing. It also offers read_file,
//     marked deprecated in favour of read_text_file, so the catalog leaves
//     it out. Meru's MCP client offers the server no roots, so the folders
//     on the command line are the ones it may use.
//   - shell: https://github.com/tumf/mcp-shell-server, PyPI mcp-shell-server
//     1.1.12 (2026-09-19); one tool, shell_execute, from
//     src/mcp_shell_server/server.py. See "Why mcp-shell-server" below.
//   - google, gmail, calendar, drive: https://github.com/taylorwilsdon/google_workspace_mcp,
//     workspace-mcp 1.28.0 (2026-09-21); names from core/tool_tiers.yaml and
//     the functions in gmail/gmail_tools.py, gcalendar/calendar_tools.py,
//     gdrive/drive_tools.py and gdocs/docs_tools.py. main.py reads --tools
//     as a list, so one process can serve several services.
//   - obsidian: https://github.com/MarkusPfundstein/mcp-obsidian,
//     mcp-obsidian 0.2.2; names from src/mcp_obsidian/tools.py. The README
//     lists them without the "obsidian_" prefix, but the server sends it.
//   - windows: https://github.com/CursorTouch/Windows-MCP, PyPI windows-mcp
//     0.8.5 (2026-08-01); names and hints from src/windows_mcp/tools/*.py.
//     Since 0.8.5 the command needs the word "serve".
//
// One Google Workspace server covers Gmail, Calendar, Drive and Docs. The
// google entry runs it once for all four. The gmail, calendar and drive
// entries run it for one service each, so you can add only the services you
// want, and each process asks Google for the permissions of its own
// services and no others.
//
// Google also runs its own MCP servers for Gmail, Drive, Docs and Calendar
// (developer preview since 2026-05-01; see
// https://developers.google.com/workspace/guides/configure-mcp-servers).
// They are remote only, at *mcp.googleapis.com, and need an OAuth sign-in
// that Meru's client doesn't do. workspace-mcp runs on this machine with
// your own OAuth client, so the catalog keeps it.
//
// Why mcp-shell-server for shell: it runs only the programs named in its
// ALLOW_COMMANDS env variable, runs them with no shell between (it checks
// each part of a pipeline), and has one tool. Its 2026 releases each fixed
// a way around the allow list, so someone maintains it. Desktop Commander
// has 26 tools and sends usage data to its makers unless told not to;
// mcp-server-commands runs any command at all.
//
// Two servers send usage data by default: windows-mcp (to PostHog) and
// Desktop Commander, which the catalog doesn't carry. The windows entry
// sets ANONYMIZED_TELEMETRY=false, the switch windows-mcp reads.
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
	// NeedText asks for a line of text, such as a list of commands, and
	// puts it in the entry's env variable Env. When Env already has a
	// value, that is the default: Enter keeps it.
	NeedText = "text"
	// NeedNote asks nothing; it tells the user something they must do, such
	// as sign in to Google in a browser.
	NeedNote = "note"
	// NeedFolders asks for one or more folders and adds them to the end of
	// Args as absolute paths. On the command line they follow the entry's
	// name: meru mcp add filesystem ~/notes. See WithArgs.
	NeedFolders = "folders"
)

// Transports an Entry can use.
const (
	TransportStdio = "stdio" // merud starts Command
	TransportHTTP  = "http"  // merud connects to URL
)

// Need is one thing a server needs from the user before it can run.
type Need struct {
	// Kind is NeedAPIKey, NeedPath, NeedURL, NeedText, NeedNote or
	// NeedFolders.
	Kind string
	// SecretName is the secrets.toml entry an api_key goes into.
	SecretName string
	// Env is the env variable a path, url or text answer goes into.
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
	// Needs lists what to ask the user, in order. Requires sums them up in
	// a few words for `meru mcp list`, such as "a Brave Search API key".
	Needs    []Need
	Requires string
	// OS names the one system the server runs on, as a runtime.GOOS value
	// such as "windows". Empty means it runs everywhere.
	OS string
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

// The six functions below return the tool lists of the Google services.
// The google entry joins all three services; each one-service entry uses
// its own. They are functions, not variables, so no two entries share a
// slice.

// gmailAllow returns the Gmail tools the model may call.
func gmailAllow() []string {
	return []string{
		"search_gmail_messages", "get_gmail_message_content", "get_gmail_messages_content_batch",
		"get_gmail_thread_content", "list_gmail_labels", "draft_gmail_message", "send_gmail_message",
	}
}

// gmailConfirm returns the Gmail tools that ask first.
func gmailConfirm() []string { return []string{"draft_gmail_message", "send_gmail_message"} }

// calendarAllow returns the Calendar tools the model may call.
func calendarAllow() []string {
	return []string{"list_calendars", "get_events", "query_freebusy", "manage_event"}
}

// calendarConfirm returns the Calendar tools that ask first.
func calendarConfirm() []string { return []string{"manage_event"} }

// driveAllow returns the Drive and Docs tools the model may call.
func driveAllow() []string {
	return []string{
		"search_drive_files", "get_drive_file_content", "list_drive_items",
		"get_doc_content", "get_doc_as_markdown", "search_docs", "list_docs_in_folder",
		"create_doc", "modify_doc_text",
	}
}

// driveConfirm returns the Drive and Docs tools that ask first.
func driveConfirm() []string { return []string{"create_doc", "modify_doc_text"} }

// googleRequires is the Requires text of every Google entry.
const googleRequires = "a Google OAuth client, a sign-in, and uv"

// installUV is the Install text for the servers that run with uvx.
const installUV = "uvx downloads the server the first time merud starts it. " +
	"Install uv, which provides uvx: https://docs.astral.sh/uv/getting-started/installation/"

// installNode is the Install text for the servers that run with npx.
const installNode = "npx downloads the server the first time merud starts it. " +
	"Install Node.js, which provides npx: https://nodejs.org/"

// shellCommands is the shell entry's starting ALLOW_COMMANDS: programs that
// read and report, and have no option that writes a file or runs another
// program (sort -o and find -exec do, so both stay out). The user can
// change the list when adding the entry. Even these can read any file you
// can, which is why every call asks first.
const shellCommands = "ls,pwd,cat,head,tail,wc,grep,date,df,du,uname,which"

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
			Requires: "a Brave Search API key, and Node.js",
			Allow:    []string{"brave_web_search", "brave_news_search"},
			Install:  installNode,
			Docs:     "https://github.com/brave/brave-search-mcp-server",
		},
		{
			Name:        "fetch",
			Title:       "Web page fetch",
			Description: "Reads a web page and hands it to the model as Markdown.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"mcp-server-fetch"},
			Requires:    "uv",
			Allow:       []string{"fetch"},
			Install:     installUV,
			Docs:        "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch",
		},
		{
			Name:  "filesystem",
			Title: "Files and folders",
			Description: "Lists, searches and reads files in the folders you name; " +
				"writing, editing, moving and making folders ask first.",
			Transport: TransportStdio,
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-filesystem"},
			Needs: []Need{{
				Kind:   NeedFolders,
				Prompt: "Folders the server may read and change",
			}},
			Requires: "one or more folders, and Node.js",
			Allow: []string{
				"read_text_file", "read_media_file", "read_multiple_files", "list_directory",
				"list_directory_with_sizes", "directory_tree", "search_files", "get_file_info",
				"list_allowed_directories",
				"write_file", "edit_file", "create_directory", "move_file",
			},
			Confirm: []string{"write_file", "edit_file", "create_directory", "move_file"},
			Install: installNode,
			Docs:    "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem",
		},
		{
			Name:  "shell",
			Title: "Shell commands",
			Description: "Runs the programs you allow, with no shell in between; each command asks first. " +
				"An approved command runs as you, with no sandbox, and can read or change anything you can.",
			Transport: TransportStdio,
			Command:   "uvx",
			Args:      []string{"mcp-shell-server"},
			Env:       map[string]string{"ALLOW_COMMANDS": shellCommands},
			Needs: []Need{
				{
					Kind:   NeedText,
					Env:    "ALLOW_COMMANDS",
					Prompt: "Programs the model may run, separated by commas",
					Help: "Name each program. The server refuses any other, but an allowed program " +
						"does whatever its arguments say: `find` can run commands with -exec, " +
						"and `git`, `python` or `bash` can do anything.",
				},
				{Kind: NeedNote, Prompt: "Each command asks you first. Read it and approve it once: " +
					"approving for the session lets the model run any allowed program without asking again."},
			},
			Requires: "the programs to allow, and uv",
			Allow:    []string{"shell_execute"},
			Confirm:  []string{"shell_execute"},
			Install:  installUV,
			Docs:     "https://github.com/tumf/mcp-shell-server",
		},
		{
			Name:        "google",
			Title:       "Gmail, Google Calendar, Drive and Docs",
			Description: "The gmail, calendar and drive entries in one server. Reading is allowed; sending, drafting and changing anything ask first.",
			Transport:   TransportStdio,
			Command:     "uvx",
			Args:        []string{"workspace-mcp", "--tools", "gmail", "calendar", "drive", "docs"},
			Env:         googleEnv(),
			Needs:       googleNeeds("Gmail API, Google Calendar API, Google Drive API and Google Docs API"),
			Requires:    googleRequires,
			Allow:       slices.Concat(gmailAllow(), calendarAllow(), driveAllow()),
			Confirm:     slices.Concat(gmailConfirm(), calendarConfirm(), driveConfirm()),
			Install:     installUV,
			Docs:        google,
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
			Requires:    googleRequires,
			Allow:       gmailAllow(),
			Confirm:     gmailConfirm(),
			Install:     installUV,
			Docs:        google,
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
			Requires:    googleRequires,
			Allow:       calendarAllow(),
			Confirm:     calendarConfirm(),
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
			Requires:    googleRequires,
			Allow:       driveAllow(),
			Confirm:     driveConfirm(),
			Install:     installUV,
			Docs:        google,
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
		{
			Name:  "windows",
			Title: "Windows desktop",
			Description: "Sees the screen and lists open apps; clicking, typing, running PowerShell " +
				"and changing files ask first. An approved action runs as you, with no sandbox.",
			Transport: TransportStdio,
			Command:   "uvx",
			Args:      []string{"windows-mcp", "serve"},
			// windows-mcp sends usage data to PostHog unless this is "false".
			Env:      map[string]string{"ANONYMIZED_TELEMETRY": "false"},
			OS:       "windows",
			Requires: "Windows, and uv",
			Allow: []string{
				"DisplayInventory", "Snapshot", "Screenshot", "Scrape", "Wait", "WaitFor",
				"App", "PowerShell", "FileSystem", "Click", "Type", "Scroll", "Move", "Shortcut",
				"MultiSelect", "MultiEdit", "Clipboard", "Process", "Notification",
			},
			// Registry stays out of allow: a registry edit can break Windows.
			Confirm: []string{
				"App", "PowerShell", "FileSystem", "Click", "Type", "Scroll", "Move", "Shortcut",
				"MultiSelect", "MultiEdit", "Clipboard", "Process", "Notification",
			},
			Install: installUV + " windows-mcp needs a recent Python; uv downloads one if you don't have it.",
			Docs:    "https://github.com/CursorTouch/Windows-MCP",
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
