// This file defines the messages meru and merud exchange over the Unix socket.
//
// The protocol is newline-delimited JSON: each message is one JSON object on
// one line. The client opens a connection, writes one Request, then reads
// Events until one has Type "done" or "error". One connection carries one
// request. The only other line a client writes is a Reply, which answers an
// "approval" event.
//
// New fields only ever get added, each with omitempty, so an older client
// reading a newer merud's events sees the fields it knows and skips the rest.

package rpc

import (
	"context"
	"encoding/json"
)

// Op names what a Request asks merud to do.
type Op string

const (
	// OpAsk sends a question and streams the answer back.
	OpAsk Op = "ask"
	// OpPing checks that merud is up. The reply is a single "done" event.
	OpPing Op = "ping"
	// OpIndex brings the search index up to date now. With an empty Path it
	// rescans every [index] folder; with a Path it indexes that folder or
	// file, which must sit inside an [index] folder. The reply is zero or
	// more "progress" events, one "report" event and "done".
	OpIndex Op = "index"
	// OpIndexStatus asks what the index holds. The reply is one "status"
	// event and "done".
	OpIndexStatus Op = "index_status"
	// OpTools asks which tools the model may use. The reply is one "tools"
	// event and "done".
	OpTools Op = "tools"
	// OpLog asks for the latest tool calls from the audit log, newest
	// first, at most Request.Limit of them. The reply is one "log" event
	// and "done".
	OpLog Op = "log"
	// OpUsage asks how much Meru has been used. The reply is one "usage"
	// event and "done".
	OpUsage Op = "usage"
	// OpMemoryList asks for every memory file. The reply is one "memories"
	// event and "done".
	OpMemoryList Op = "memory_list"
	// OpMemoryAdd saves Request.Text as a new memory of Request.Kind, such
	// as "me" or "preferences". The reply is one "memories" event holding
	// the new memory, and "done".
	OpMemoryAdd Op = "memory_add"
	// OpMemoryForget deletes the memory whose ID is Request.ID, such as
	// "me/name-dana-reyes.md". The reply is "done".
	OpMemoryForget Op = "memory_forget"
	// OpSkills lists the skills, the disabled ones last. The reply is one
	// "skills" event and "done". The event's Text holds the reasons merud
	// skipped any skill folders, one per line.
	OpSkills Op = "skills"
	// OpSkillShow asks for the SKILL.md of the skill named Request.ID. The
	// reply is one "skills" event holding that skill, with its Body, and
	// "done".
	OpSkillShow Op = "skill_show"
	// OpSkillReset puts the shipped copy of the built-in skill named
	// Request.ID back in place of the user's. The reply is "done".
	OpSkillReset Op = "skill_reset"
	// OpMCPProbe starts the MCP server described by Request.Server for a
	// moment, lists the tools it offers, and stops it. Nothing is saved.
	// The reply is one "probe" event and "done".
	OpMCPProbe Op = "mcp_probe"
	// OpMCPReload reads config.toml and secrets.toml again and swaps in a
	// new MCP pool, so a server added or removed takes effect without a
	// restart. The reply is one "tools" event with the new state, and
	// "done".
	OpMCPReload Op = "mcp_reload"
	// OpMCPStatus asks for the state of each configured MCP server. merud
	// answers from config and what its client pool already holds, and
	// sends nothing to any server, so the reply comes at once while a
	// server is down. The reply is one "mcp_status" event and "done".
	OpMCPStatus Op = "mcp_status"
	// OpSessions lists the past conversations, the most recent first, at
	// most Request.Limit of them. merud reads them from the session
	// transcripts. The reply is one "sessions" event and "done".
	OpSessions Op = "sessions"
	// OpSessionTurns asks for the turns of the session named
	// Request.Session: each question with its answer, route, sources and
	// tool calls, read from the transcript. The reply is one "turns" event
	// and "done".
	OpSessionTurns Op = "session_turns"

	// The ops below are the desktop app's settings. Each one changes
	// config.toml, secrets.toml or the output folder in merud, never in
	// the client, and settings.go describes what they carry.

	// OpConnections lists every tool source with each tool's policy (off,
	// ask or allow), and the catalog servers that could be added. The
	// reply is one "connections" event and "done".
	OpConnections Op = "connections"
	// OpToolPolicy sets one tool's policy, as Request.Policy says, in
	// config.toml, then reloads the tools. The reply is one "connections"
	// event and "done".
	OpToolPolicy Op = "tool_policy"
	// OpMCPAdd adds the catalog server named Request.ID to config.toml and
	// reloads the MCP servers. Any API key the server needs must be in
	// secrets.toml first; OpSecretSet saves it. The reply is one
	// "connections" event and "done".
	OpMCPAdd Op = "mcp_add"
	// OpMCPRemove takes the MCP server named Request.ID out of config.toml
	// and reloads the MCP servers. The reply is one "connections" event
	// and "done".
	OpMCPRemove Op = "mcp_remove"
	// OpSecretSet saves Request.Text as the secret named Request.ID in
	// secrets.toml. Only a name that config.toml or the catalog refers to
	// is accepted. The reply is "done"; merud never sends a secret back.
	OpSecretSet Op = "secret_set"
	// OpFolders lists the [index] folders with how many files the index
	// holds from each, and the usual folders that exist on this machine
	// and aren't indexed yet. The reply is one "folders" event and "done".
	OpFolders Op = "folders"
	// OpFolderAdd adds the folder Request.Path to [index] folders and
	// starts a scan; OpFolderRemove takes it out, and the next scan drops
	// its files from the index. The reply is one "folders" event and
	// "done".
	OpFolderAdd    Op = "folder_add"
	OpFolderRemove Op = "folder_remove"
	// OpSaveFile saves a Markdown file in [skills] output_dir through the
	// write_file tool, so it goes through dispatch and asks first as
	// write_file does. Request.Kind is SaveChat (the whole session named
	// Request.Session) or SaveNote (Request.Text, an answer from that
	// session). The reply is one "saved" event, whose Text is the path
	// written, and "done".
	OpSaveFile Op = "save_file"
	// OpSkillEnable and OpSkillDisable take the skill named Request.ID out
	// of [skills] disabled or put it in. The reply is one "skills" event,
	// as OpSkills sends, and "done".
	OpSkillEnable  Op = "skill_enable"
	OpSkillDisable Op = "skill_disable"
	// OpModels asks which models config names and which ones Ollama holds
	// in memory now. The reply is one "models" event and "done".
	OpModels Op = "models"
)

// Source says where a question came from. It becomes a metric attribute, so
// it must stay a small fixed set.
type Source string

const (
	SourceCLI Source = "cli" // one-shot `meru "..."`
	SourceTUI Source = "tui" // `meru chat`
	SourceJob Source = "job" // the scheduler (v0.5)
	// SourceDesktop is the desktop app, cmd/meru-desktop.
	SourceDesktop Source = "desktop"
)

// Request is the one message a client sends on a connection.
type Request struct {
	Op Op `json:"op"`
	// Session names the conversation to continue. Empty starts a new one;
	// merud replies with its ID in a "session" event.
	Session string `json:"session,omitempty"`
	// Text is the question, for OpAsk.
	Text   string `json:"text,omitempty"`
	Source Source `json:"source,omitempty"`
	// Path is the absolute folder or file to index, for OpIndex. Empty
	// means every [index] folder.
	Path string `json:"path,omitempty"`
	// Limit caps how many rows OpLog returns, or how many sessions
	// OpSessions lists. Zero means merud's default.
	Limit int `json:"limit,omitempty"`
	// Kind is the memory's folder for OpMemoryAdd, and ID names the memory
	// for OpMemoryForget.
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
	// Server describes the server to probe, for OpMCPProbe.
	Server *ProbeServer `json:"server,omitempty"`
	// Scope says where an OpAsk turn may look; see the Scope constants.
	// Empty means ScopeAuto: the router decides.
	Scope string `json:"scope,omitempty"`
	// Policy is the change OpToolPolicy makes. It is a pointer, as Server
	// is, so a Request stays comparable with ==, which tests rely on.
	Policy *PolicyChange `json:"policy,omitempty"`
}

// EventType names what an Event carries.
type EventType string

const (
	// EventSession tells the client which session this turn belongs to. It
	// comes first on every OpAsk reply.
	EventSession EventType = "session"
	// EventRoute reports the route the router picked, with its confidence.
	// Fallback is true when the router wasn't sure and used the fallback
	// route instead. Skills names the skills the turn loaded, if any, with
	// only Name set.
	EventRoute EventType = "route"
	// EventSources lists the excerpts from the user's files that merud put
	// in the prompt, numbered as the answer cites them ([1], [2], ...). It
	// comes after "route" and before the first "token", and only on a turn
	// whose search found something. A round whose tool calls returned
	// excerpts, such as search_files, sends it again after its
	// "tool_result" events, with every source so far: each "sources" event
	// replaces the one before it.
	EventSources EventType = "sources"
	// EventToken carries the next piece of the answer text.
	EventToken EventType = "token"
	// EventToolCall says the model asked for a tool, in Tool, before
	// dispatch decides whether it runs.
	EventToolCall EventType = "tool_call"
	// EventToolResult says how that call ended, in Tool, with Outcome and
	// DurationMillis set.
	EventToolResult EventType = "tool_result"
	// EventApproval asks the user whether a tool call may run, in
	// Approval. The client must answer with a Reply line carrying the same
	// ID; the turn waits until it does. A client that can't ask anyone
	// replies "deny".
	EventApproval EventType = "approval"
	// EventTools answers OpTools, in Servers.
	EventTools EventType = "tools"
	// EventLog answers OpLog, in Log.
	EventLog EventType = "log"
	// EventUsage answers OpUsage, in Usage.
	EventUsage EventType = "usage"
	// EventMemories answers OpMemoryList and OpMemoryAdd, in Memories. On
	// an OpAsk reply it lists the memories recall put in this turn's
	// prompt, before the first "token"; a turn that recalled none sends
	// none.
	EventMemories EventType = "memories"
	// EventSkills answers OpSkills and OpSkillShow, in Skills.
	EventSkills EventType = "skills"
	// EventProbe answers OpMCPProbe, in Probe.
	EventProbe EventType = "probe"
	// EventMCPStatus answers OpMCPStatus, in MCP.
	EventMCPStatus EventType = "mcp_status"
	// EventSessions answers OpSessions, in Sessions.
	EventSessions EventType = "sessions"
	// EventTurns answers OpSessionTurns, in Turns.
	EventTurns EventType = "turns"
	// EventConnections answers the connection ops, in Connections and
	// Catalog.
	EventConnections EventType = "connections"
	// EventFolders answers the folder ops, in Folders and Suggested.
	EventFolders EventType = "folders"
	// EventSaved answers OpSaveFile; Text holds the path written.
	EventSaved EventType = "saved"
	// EventModels answers OpModels, in Models.
	EventModels EventType = "models"
	// EventProgress carries one line of news from a running OpIndex, such
	// as "scanning 2 folders", in Text.
	EventProgress EventType = "progress"
	// EventReport carries what an OpIndex run did, in Report.
	EventReport EventType = "report"
	// EventStatus answers OpIndexStatus, in Status.
	EventStatus EventType = "status"
	// EventDone ends a successful reply. After an ask it carries the turn's
	// stats: TTFTMillis, DurationMillis, TokensIn, TokensOut and EvalMillis.
	// After a ping
	// those fields are zero and left out of the JSON.
	EventDone EventType = "done"
	// EventError ends a failed reply; Error says why.
	EventError EventType = "error"
)

// Event is one message merud sends back. Only the fields that matter for its
// Type are set.
type Event struct {
	Type       EventType `json:"type"`
	Session    string    `json:"session,omitempty"`
	Text       string    `json:"text,omitempty"`
	Route      string    `json:"route,omitempty"`
	Confidence float64   `json:"confidence,omitempty"`
	Fallback   bool      `json:"fallback,omitempty"` // route event: the router fell back
	Error      string    `json:"error,omitempty"`

	// Sources is set on a "sources" event.
	Sources []Citation `json:"sources,omitempty"`
	// Report is set on a "report" event and Status on a "status" event.
	// They are pointers so that events without them leave them out of the
	// JSON: omitempty drops a nil pointer but never a struct value.
	Report *IndexReport `json:"report,omitempty"`
	Status *IndexStatus `json:"status,omitempty"`

	// Tool is set on "tool_call" and "tool_result" events, Approval on an
	// "approval" event.
	Tool     *ToolEvent `json:"tool,omitempty"`
	Approval *Approval  `json:"approval,omitempty"`
	// Servers is set on a "tools" event and Log on a "log" event.
	Servers []ServerInfo `json:"servers,omitempty"`
	Log     []LogEntry   `json:"log,omitempty"`
	// Usage is set on a "usage" event.
	Usage []UsageWindow `json:"usage,omitempty"`
	// Memories is set on a "memories" event.
	Memories []MemoryInfo `json:"memories,omitempty"`
	// Skills is set on a "skills" event, and on a "route" event that
	// loaded skills, where each entry carries only its Name.
	Skills []SkillInfo `json:"skills,omitempty"`
	// Probe is set on a "probe" event.
	Probe *ProbeResult `json:"probe,omitempty"`
	// MCP is set on an "mcp_status" event.
	MCP []MCPStatus `json:"mcp,omitempty"`
	// Sessions is set on a "sessions" event and Turns on a "turns" event.
	Sessions []SessionInfo `json:"sessions,omitempty"`
	Turns    []TurnInfo    `json:"turns,omitempty"`
	// Connections and Catalog are set on a "connections" event, Folders
	// and Suggested on a "folders" event, and Models on a "models" event.
	Connections []Connection   `json:"connections,omitempty"`
	Catalog     []CatalogEntry `json:"catalog,omitempty"`
	Folders     []FolderInfo   `json:"folders,omitempty"`
	Suggested   []FolderInfo   `json:"suggested,omitempty"`
	Models      *ModelsInfo    `json:"models,omitempty"`

	// The turn's stats, on the "done" event that ends an ask.

	// TTFTMillis is the time from receiving the question to the first token
	// of the answer, routing included, in milliseconds.
	TTFTMillis int64 `json:"ttft_ms,omitempty"`
	// DurationMillis is the time from receiving the question to the end of
	// the turn, in milliseconds.
	DurationMillis int64 `json:"duration_ms,omitempty"`
	// TokensIn and TokensOut are the main model's prompt and answer token
	// counts, as the model runtime reported them.
	TokensIn  int `json:"tokens_in,omitempty"`
	TokensOut int `json:"tokens_out,omitempty"`
	// EvalMillis is the time the model runtime reports spending on writing
	// the answer's tokens. TokensOut / EvalMillis gives the model's speed
	// without the network, routing or buffering in between.
	EvalMillis int64 `json:"eval_ms,omitempty"`
}

// Citation is one excerpt a turn's prompt held, as the "sources" event lists
// it. N is the number the answer cites it by.
type Citation struct {
	N int `json:"n"`
	// Path is the file, shortened for display: "~/notes/garden.md" for a
	// file under the home folder, otherwise the absolute path.
	Path    string `json:"path"`
	Heading string `json:"heading,omitempty"` // the heading or symbol the excerpt sits under
	// StartLine and EndLine locate the excerpt in a text file (1-based);
	// Page locates it in a PDF. Unused fields are 0.
	StartLine int `json:"start_line,omitempty"`
	EndLine   int `json:"end_line,omitempty"`
	Page      int `json:"page,omitempty"`
	// Score is the excerpt's search score (reciprocal-rank fusion); higher
	// is better.
	Score float64 `json:"score,omitempty"`
}

// IndexReport says what one OpIndex run, or merud's startup scan, did.
type IndexReport struct {
	Seen      int `json:"seen"`      // files found
	Indexed   int `json:"indexed"`   // files chunked, embedded and stored
	Unchanged int `json:"unchanged"` // files the index already held as they are
	Removed   int `json:"removed"`   // files dropped from the index
	Failed    int `json:"failed"`    // files that couldn't be read or parsed
	Skipped   int `json:"skipped"`   // files and folders the skip rules left out
	Chunks    int `json:"chunks"`    // chunks written
	// DurationMillis is the run's wall time in milliseconds.
	DurationMillis int64 `json:"duration_ms"`
}

// IndexStatus says what the search index holds and what the indexer is
// doing.
type IndexStatus struct {
	// Folders lists the [index] folders, as config.toml writes them.
	Folders []string `json:"folders"`
	// Documents, Chunks and Vectors count what the index holds. Vectors is
	// below Chunks while merud re-embeds after an embedding model change.
	Documents int `json:"documents"`
	Chunks    int `json:"chunks"`
	Vectors   int `json:"vectors"`
	// DBBytes is the size on disk of meru.db plus its -wal and -shm files.
	DBBytes int64 `json:"db_bytes,omitempty"`
	// Memories counts the memory files, and Profile the ones in the kinds
	// that go into every prompt ("me" and "preferences"). A Profile of 0
	// means Meru knows nothing about the user yet. Both are -1 when merud
	// couldn't read the memory folder.
	Memories int `json:"memories"`
	Profile  int `json:"profile"`
	// Scanning is true while a scan of the folders runs.
	Scanning bool `json:"scanning,omitempty"`
	// LastScan is what the last finished scan did, and LastScanAt when it
	// ended (RFC 3339). Both are empty before the first scan ends.
	LastScan   *IndexReport `json:"last_scan,omitempty"`
	LastScanAt string       `json:"last_scan_at,omitempty"`
	// LastError is why the last scan stopped early, if it did.
	LastError string `json:"last_error,omitempty"`
}

// Choice is the user's answer to an approval prompt.
type Choice string

const (
	// ChoiceOnce runs this call. The next call to the tool asks again.
	ChoiceOnce Choice = "once"
	// ChoiceSession runs this call and every later call to the tool in the
	// same session without asking.
	ChoiceSession Choice = "session"
	// ChoiceDeny doesn't run the call.
	ChoiceDeny Choice = "deny"
)

// ToolEvent describes one tool call on a "tool_call" or "tool_result" event.
type ToolEvent struct {
	// ID ties a call's events together within the turn.
	ID string `json:"id"`
	// Name is the tool's full name, as the model saw it: "<server>.<tool>"
	// for MCP, "a2a.<agent>.<skill>" for A2A, "cmd.<name>" for a local
	// command, "<tool>" for a built-in.
	Name string `json:"name"`
	// Kind is "mcp", "a2a", "builtin" or "command".
	Kind string `json:"kind"`
	// Args are the call's arguments as JSON, as the model wrote them. The
	// model never sees a secret, so they hold none. Set on "tool_call" only.
	Args json.RawMessage `json:"args,omitempty"`
	// Outcome and DurationMillis are set on "tool_result" only. Outcome is
	// "ok", "error", "denied", "declined", "cancelled" or "timeout".
	Outcome        string `json:"outcome,omitempty"`
	DurationMillis int64  `json:"duration_ms,omitempty"`
	// Sources lists the excerpts from the user's files that the call
	// returned, numbered as the model reads them. Set on "tool_result"
	// only, by a tool such as search_files; the turn's next "sources"
	// event holds them too.
	Sources []Citation `json:"sources,omitempty"`
}

// Approval asks the user whether one tool call may run.
type Approval struct {
	// ID names this question; the Reply must carry it back.
	ID string `json:"id"`
	// Name, Kind and Args describe the call, as on ToolEvent, except that
	// a command's Args hold the program it will run (see
	// dispatch.Auditor).
	Name string          `json:"name"`
	Kind string          `json:"kind"`
	Args json.RawMessage `json:"args,omitempty"`
	// Choices lists the answers the client may offer, in order. The
	// configure tool offers only "once" and "deny".
	Choices []Choice `json:"choices"`
}

// Reply is the line a client writes to answer an "approval" event.
type Reply struct {
	ApprovalID string `json:"approval_id"`
	Choice     Choice `json:"choice"`
}

// ApproveFunc asks the user about one tool call and returns their choice.
// On the server side, dispatch calls it and the rpc server turns it into an
// "approval" event and waits for the Reply. On the client side, Do calls it
// for each "approval" event and writes the Reply. It fails when ctx ends or
// the connection breaks.
type ApproveFunc func(ctx context.Context, a Approval) (Choice, error)

// ServerInfo is one tool source on a "tools" event: an MCP server, an A2A
// agent, merud's built-in tools, or the local commands.
type ServerInfo struct {
	Name string `json:"name"`
	// Kind is "mcp", "a2a", "builtin" or "command".
	Kind string `json:"kind"`
	// Transport is "stdio" or "http" for MCP, "http" for A2A, "" for built-ins.
	Transport string `json:"transport,omitempty"`
	// Connected is false when merud couldn't reach the source; LastError
	// says why.
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
	// Tools lists the tools the model may use from this source.
	Tools []ToolInfo `json:"tools"`
	// Offered counts every tool the source offers, allowed or not, and
	// OfferedTools lists them, by full name, from the source's last
	// listing: the desktop app shows the ones config leaves off, so the
	// user can turn one on. Only the tool's name and description are set.
	Offered      int        `json:"offered"`
	OfferedTools []ToolInfo `json:"offered_tools,omitempty"`
	// Unknown lists allow entries the source doesn't offer, usually typos.
	Unknown []string `json:"unknown,omitempty"`
}

// ToolInfo is one tool the model may use.
type ToolInfo struct {
	// Name is the full name the model sees.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Confirm is true when a call asks first. AlwaysAsks is true when no
	// session approval can switch that off (the configure tool).
	Confirm    bool `json:"confirm,omitempty"`
	AlwaysAsks bool `json:"always_asks,omitempty"`
	// Argv is a local command's program and arguments as config.toml
	// declares them, placeholders and all, so `meru tools` can show what
	// the command runs. Empty for every other tool.
	Argv []string `json:"argv,omitempty"`
}

// LogEntry is one row of the tool_calls audit log.
type LogEntry struct {
	// Time is when the call started (RFC 3339).
	Time    string `json:"time"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	Server  string `json:"server"`
	Tool    string `json:"tool"`
	// Args and Result are as the transcript holds them, secrets redacted.
	// Result is cut to a few hundred characters.
	Args   json.RawMessage `json:"args,omitempty"`
	Result string          `json:"result,omitempty"`
	// Outcome is "ok", "error", "denied", "declined", "cancelled" or
	// "timeout"; Approval is the user's choice, or "" when nobody was asked.
	Outcome        string `json:"outcome"`
	Approval       string `json:"approval,omitempty"`
	DurationMillis int64  `json:"duration_ms"`
	TraceID        string `json:"trace_id,omitempty"`
}

// The usage windows, in the order a "usage" event lists them. Today, week
// and month follow merud's local calendar: today starts at midnight, the
// week on Monday, the month on the 1st. 1h and 30d roll back from now.
const (
	Usage1h       = "1h"
	UsageToday    = "today"
	UsageWeek     = "week"
	UsageMonth    = "month"
	Usage30d      = "30d"
	UsageLifetime = "all"
)

// UsageWindow is how much Meru was used in one window of time, counting
// answered questions only: a turn that failed or was cancelled has no
// answer line in the transcript, and the numbers must be rebuildable from
// the transcripts.
type UsageWindow struct {
	// Name is one of the Usage* constants.
	Name string `json:"name"`
	// Since is when the window starts (RFC 3339); "" for all time.
	Since string `json:"since,omitempty"`
	// Sessions counts the sessions with a question in the window.
	Sessions int `json:"sessions"`
	// Turns counts the questions answered.
	Turns int `json:"turns"`
	// TokensIn and TokensOut sum the main model's prompt and answer tokens
	// over every model call of those turns.
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
	// ActiveMillis sums how long merud spent answering, question to done.
	ActiveMillis int64 `json:"active_ms"`
	// Docs counts the distinct files whose excerpts went into a prompt.
	Docs int `json:"docs"`
	// ToolCalls counts the tool calls those turns made.
	ToolCalls int `json:"tool_calls"`
}

// ProfileKinds returns the memory kinds whose files go into every prompt:
// who the user is, and how they like things done. Memories of other kinds
// come back only when a search finds them. It returns a new slice each call,
// so no caller can change the list for another.
func ProfileKinds() []string { return []string{"me", "preferences"} }

// MemoryInfo is one memory file, as the memory ops show it.
type MemoryInfo struct {
	// ID is the file's path inside ~/.meru/memory, such as
	// "me/name-dana-reyes.md"; OpMemoryForget takes it.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
	// Created is the date in the file's frontmatter (YYYY-MM-DD), "" when
	// the file has none. Source says where it came from, such as
	// "meru setup user" or "session 2026-09-24T144512-cdc3".
	Created string `json:"created,omitempty"`
	Source  string `json:"source,omitempty"`
}

// SkillInfo is one skill, as the skill ops show it.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Builtin is true for a skill that ships inside merud; Edited is true
	// when the user's copy differs from the shipped one.
	Builtin bool `json:"builtin,omitempty"`
	Edited  bool `json:"edited,omitempty"`
	// Body is the SKILL.md text, on OpSkillShow only.
	Body string `json:"body,omitempty"`
	// Disabled is true for a skill [skills] disabled names. merud loads
	// no disabled skill, so its Description is empty.
	Disabled bool `json:"disabled,omitempty"`
}

// MCP server states, for MCPStatus.State. Config has no key that turns a
// server off, so there is no "disabled" state; a server you don't want is
// one you remove.
const (
	MCPConnected    = "connected"
	MCPNotConnected = "not connected"
)

// MCPStatus is one row of `meru mcp` and the chat's /mcp box: what config
// declares for one [[mcp.servers]] entry, joined with what merud's client
// pool holds for it. A server that never connected still reports the
// tools config allows.
type MCPStatus struct {
	Name      string `json:"name"`
	Transport string `json:"transport"` // "stdio" or "http"
	State     string `json:"state"`     // MCPConnected or MCPNotConnected
	URL       string `json:"url,omitempty"`
	// Tools counts the tools the server offers, from its tools/list. It is
	// -1 when the server isn't connected: with no list, any count would be
	// a guess.
	Tools int `json:"tools"`
	// Allowed counts the tools config allows, and Confirm the allowed
	// tools that ask before they run (confirm and always_confirm).
	Allowed int `json:"allowed"`
	Confirm int `json:"confirm"`
	// Err says in one line why the server isn't connected.
	Err string `json:"err,omitempty"`
}

// ProbeServer is an MCP server to try before it goes into config: the same
// fields as an [[mcp.servers]] entry, minus the allow lists. Env and Headers
// values may be "secret:<name>"; merud resolves them from secrets.toml.
type ProbeServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Remote  bool              `json:"remote,omitempty"`
}

// ProbeResult is what a probed server offers.
type ProbeResult struct {
	// ServerName and ServerVersion are what the server reports about
	// itself in the MCP handshake.
	ServerName    string `json:"server_name,omitempty"`
	ServerVersion string `json:"server_version,omitempty"`
	// Tools lists every tool it offers, by the server's own name.
	Tools []ProbeTool `json:"tools"`
}

// ProbeTool is one tool a probed server offers, with the hints MCP lets a
// server give about it. A hint the server leaves out is nil: unknown.
type ProbeTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ReadOnly is the server's readOnlyHint: the tool doesn't change its
	// environment. Destructive is destructiveHint: it may delete or
	// overwrite. MCP says both are hints from the server, not promises.
	ReadOnly    *bool `json:"read_only,omitempty"`
	Destructive *bool `json:"destructive,omitempty"`
}
