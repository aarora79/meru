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
	// EventToken carries the next piece of the answer text.
	EventToken EventType = "token"
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
