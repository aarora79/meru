// This file defines the messages meru and merud exchange over the Unix socket.
//
// The protocol is newline-delimited JSON: each message is one JSON object on
// one line. The client opens a connection, writes one Request, then reads
// Events until one has Type "done" or "error". One connection carries one
// request.
//
// New fields only ever get added, each with omitempty, so an older client
// reading a newer merud's events sees the fields it knows and skips the rest.

package rpc

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
)

// Source says where a question came from. It becomes a metric attribute, so
// it must stay a small fixed set.
type Source string

const (
	SourceCLI Source = "cli" // one-shot `meru "..."`
	SourceTUI Source = "tui" // `meru chat`
	SourceJob Source = "job" // the scheduler (v0.5)
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
}

// EventType names what an Event carries.
type EventType string

const (
	// EventSession tells the client which session this turn belongs to. It
	// comes first on every OpAsk reply.
	EventSession EventType = "session"
	// EventRoute reports the route the router picked, with its confidence.
	// Fallback is true when the router wasn't sure and used the fallback
	// route instead.
	EventRoute EventType = "route"
	// EventSources lists the excerpts from the user's files that merud put
	// in the prompt, numbered as the answer cites them ([1], [2], ...). It
	// comes after "route" and before the first "token", and only on a turn
	// whose search found something.
	EventSources EventType = "sources"
	// EventToken carries the next piece of the answer text.
	EventToken EventType = "token"
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
	// Scanning is true while a scan of the folders runs.
	Scanning bool `json:"scanning,omitempty"`
	// LastScan is what the last finished scan did, and LastScanAt when it
	// ended (RFC 3339). Both are empty before the first scan ends.
	LastScan   *IndexReport `json:"last_scan,omitempty"`
	LastScanAt string       `json:"last_scan_at,omitempty"`
	// LastError is why the last scan stopped early, if it did.
	LastError string `json:"last_error,omitempty"`
}
