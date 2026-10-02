// This file holds the plain values the Bridge sends the page, and the
// functions that build them from merud's events: a friendly label for each
// tool call ("Searched mail", "Read lisbon.md"), the servers a turn
// contacted, and an approval card with its arguments laid out to read.
// Keeping these in Go keeps the page's code small, and lets table-driven
// tests pin down every label.

package desktop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// UpdateEvent is the name of the event that carries each Update to the
// page. The page listens for it once, at start.
const UpdateEvent = "meru:update"

// The kinds of Update.
const (
	// KindStart says a question went to merud. Question and Session are
	// set; Session is "" when merud will start a new session.
	KindStart = "start"
	// KindEvent carries one event from merud, in Event. A tool call or
	// tool result also carries its Step, and a "session" event its
	// Session.
	KindEvent = "event"
	// KindApproval asks the page to show an approval card, in Approval.
	KindApproval = "approval"
	// KindEnd says the turn is over. Error says why it failed, Stopped is
	// true when the user stopped it, Contacted lists who the turn's tool
	// calls reached, and Cited lists the sources the answer cites.
	KindEnd = "end"
	// KindQueue carries the questions waiting behind the running turn,
	// oldest first, in Queue. Notice says what just happened to the
	// queue, such as "Dropped 2 queued questions."
	KindQueue = "queue"
	// KindAttachments carries the files attached to the next question,
	// in Attachments. Notice names any file a pick or drop couldn't
	// attach, and why.
	KindAttachments = "attachments"
	// KindConnector carries one step of a connector's save or fix, in
	// Connector, while the Settings card waits for it (connectors.go).
	KindConnector = "connector"
)

// Update is one message from the Bridge to the page. Only the fields its
// Kind needs are set. Turn numbers the questions this app has sent, from
// 1, so the page can ignore a late message about a turn it already ended.
//
// The `json:"..."` text after each field is a struct tag: it names the
// field in the JSON the page receives, and omitempty leaves an empty
// field out.
type Update struct {
	Turn      int           `json:"turn"`
	Kind      string        `json:"kind"`
	Question  string        `json:"question,omitempty"`
	Session   string        `json:"session,omitempty"`
	Event     *rpc.Event    `json:"event,omitempty"`
	Step      *Step         `json:"step,omitempty"`
	Approval  *ApprovalView `json:"approval,omitempty"`
	Queue     []string      `json:"queue,omitempty"`
	Contacted []string      `json:"contacted,omitempty"`
	// Cited holds the sources the finished answer cites by number, picked
	// with rpc.Cited as `meru` and `meru chat` pick them. The page shows
	// only these under the answer.
	Cited   []rpc.Citation `json:"cited,omitempty"`
	Error   string         `json:"error,omitempty"`
	Stopped bool           `json:"stopped,omitempty"`
	Notice  string         `json:"notice,omitempty"`
	// Attachments is set on a KindAttachments Update. Images, on a
	// KindStart Update, are the images the question carries, with their
	// previews, for its bubble.
	Attachments []Attachment `json:"attachments,omitempty"`
	Images      []Attachment `json:"images,omitempty"`
	// Connector is set on a KindConnector Update.
	Connector *rpc.ConnectorStatus `json:"connector,omitempty"`
}

// Step is one tool call as the work strip shows it: a friendly Label, and
// the raw name, outcome and time behind "Show steps".
type Step struct {
	// ID ties a call's tool_call and tool_result events together. Past
	// turns have none.
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Server is the MCP server or A2A agent the call went to, and "" for
	// a built-in tool or a local command.
	Server         string `json:"server,omitempty"`
	Label          string `json:"label"`
	Outcome        string `json:"outcome,omitempty"`
	DurationMillis int64  `json:"duration_ms,omitempty"`
	// Progress is the latest line a running call sent, such as
	// "Installing Meru's page reader (about 95 MB, once)". The strip shows
	// it beside the label until the call ends.
	Progress string `json:"progress,omitempty"`
}

// stepOf builds the Step for a tool call.
func stepOf(id, kind, name string, args json.RawMessage) Step {
	server, tool := splitName(kind, name)
	return Step{ID: id, Name: name, Kind: kind, Server: server, Label: stepLabel(kind, server, tool, args)}
}

// splitName splits a tool's full name into the server or agent it belongs
// to and the tool's own name: "google.search_gmail_messages" gives
// "google" and "search_gmail_messages", "a2a.travel.book" gives "travel"
// and "book". A built-in tool or a local command has no server.
func splitName(kind, name string) (server, tool string) {
	switch kind {
	case "builtin":
		return "", name
	case "command":
		return "", strings.TrimPrefix(name, "cmd.")
	case "a2a":
		rest := strings.TrimPrefix(name, "a2a.")
		// strings.Cut splits at the first ".", and ok says whether it found one.
		if agent, skill, ok := strings.Cut(rest, "."); ok {
			return agent, skill
		}
		return rest, ""
	}
	if s, t, ok := strings.Cut(name, "."); ok {
		return s, t
	}
	return "", name
}

// stepLabel writes a tool call the way a person would say what Meru did:
// "Searched mail", "Read lisbon.md", "Asked travel". A tool it doesn't
// know gets "Used <server> <tool>", with the underscores turned to spaces.
func stepLabel(kind, server, tool string, args json.RawMessage) string {
	a := argMap(args)
	switch kind {
	case "a2a":
		return "Asked " + server
	case "command":
		return "Ran " + tool
	case "builtin":
		return builtinLabel(tool, a)
	}
	t := strings.ToLower(tool)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("mail", "message", "thread"):
		switch {
		case has("send"):
			return "Sent mail"
		case has("draft"):
			return "Drafted mail"
		case has("search", "list", "query"):
			return "Searched mail"
		case has("get", "read"):
			return "Read mail"
		}
	case has("calendar", "event"):
		if has("create", "add", "update", "delete") {
			return "Changed calendar"
		}
		return "Checked calendar"
	case has("note", "vault"):
		switch {
		case has("search", "list"):
			return "Searched notes"
		case has("read", "get"):
			if f := fileArg(a); f != "" {
				return "Read " + f
			}
			return "Read a note"
		}
	}
	name := strings.ReplaceAll(tool, "_", " ")
	if server == "" {
		return "Used " + name
	}
	return "Used " + server + " " + name
}

// builtinLabel is stepLabel for merud's built-in tools.
func builtinLabel(tool string, a map[string]any) string {
	switch tool {
	case "read_file":
		if f := fileArg(a); f != "" {
			return "Read " + f
		}
		return "Read a file"
	case "list_folder":
		if f := fileArg(a); f != "" {
			return "Listed " + f
		}
		return "Listed a folder"
	case "grep", "search_files":
		return "Searched your files"
	case "web_search":
		return "Searched the web"
	case "web_fetch":
		if h := hostArg(a); h != "" {
			return "Read " + h
		}
		return "Read a web page"
	case "remember":
		return "Saved a memory"
	case "write_file":
		if f := fileArg(a); f != "" {
			return "Wrote " + f
		}
		return "Wrote a file"
	case "configure":
		return "Changed settings"
	}
	return "Used " + strings.ReplaceAll(tool, "_", " ")
}

// argMap decodes a call's arguments into a map, or returns nil when they
// aren't a JSON object.
func argMap(args json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return nil
	}
	return m
}

// fileArg returns the last part of the path in a call's "path", "file" or
// "filename" argument, such as "lisbon.md", or "" when there is none.
func fileArg(a map[string]any) string {
	for _, k := range []string{"path", "file", "filename", "note"} {
		// v.(string) is a type assertion: ok is false when v isn't a string.
		if s, ok := a[k].(string); ok && s != "" {
			// path.Base works on "/" paths; the model may also write "\".
			s = strings.ReplaceAll(s, `\`, "/")
			return path.Base(strings.TrimRight(s, "/"))
		}
	}
	return ""
}

// hostArg returns the host in a call's "url" argument, such as
// "example.com", or "" when there is none.
func hostArg(a map[string]any) string {
	s, ok := a["url"].(string)
	if !ok {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// contacted lists who a turn's tool calls reached, in the order of first
// use, for the privacy line: each MCP server and A2A agent by name,
// "web search" for web_search and the site's host for web_fetch. A call
// that never ran (denied or declined) contacted nobody. Built-in tools
// that read the user's files, and local commands, reach no one else, so
// they add nothing.
func contacted(steps []Step) []string {
	var out []string
	add := func(s string) {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, s := range steps {
		if s.Outcome == "denied" || s.Outcome == "declined" {
			continue
		}
		switch {
		case s.Kind == "mcp" || s.Kind == "a2a":
			add(s.Server)
		case s.Kind == "builtin" && s.Name == "web_search":
			add("web search")
		case s.Kind == "builtin" && s.Name == "web_fetch":
			add(strings.TrimPrefix(s.Label, "Read "))
		}
	}
	return out
}

// ApprovalView is an approval card: which tool wants to run, its
// arguments laid out to read, and the choices merud offers with plain
// labels.
type ApprovalView struct {
	// ID names the card; the page sends it back with the answer. The
	// Bridge makes it from the turn or save and merud's approval ID.
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Server is the MCP server or A2A agent the call goes to, "" for a
	// built-in tool or a local command, and Tool the tool's own name: the
	// side panel lists the server's tools with their policies while the
	// card is open. Task is "save" for a card a save asked for, and ""
	// for one a turn asked for.
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool"`
	Task   string `json:"task,omitempty"`
	// Label says what the call would do, as the work strip would say it.
	Label string `json:"label"`
	// Fields holds the mail fields (To, Cc, Bcc, Subject, Body) when the
	// arguments have them, in that order.
	Fields []Field `json:"fields,omitempty"`
	// JSON holds the other arguments, indented, or all of them when none
	// is a mail field.
	JSON    string       `json:"json,omitempty"`
	Choices []ChoiceView `json:"choices"`
	// Draft is what "Edit first" puts in the composer: the call's
	// arguments written as a request the user can change and send, since
	// merud runs a call with the arguments it asked about and no others.
	Draft string `json:"draft"`
}

// Field is one labelled argument on an approval card.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ChoiceView is one answer an approval card offers.
type ChoiceView struct {
	Choice rpc.Choice `json:"choice"`
	Label  string     `json:"label"`
}

// approvalView builds the card for a. It lifts To, Cc, Bcc, Subject and
// Body out of the arguments when "to" or "subject" is among them, and
// shows the rest as indented JSON. Arguments that aren't a JSON object
// show as they came.
func approvalView(a rpc.Approval) (v ApprovalView) {
	server, tool := splitName(a.Kind, a.Name)
	v = ApprovalView{ID: a.ID, Name: a.Name, Kind: a.Kind, Server: server, Tool: tool, Label: stepLabel(a.Kind, server, tool, a.Args)}
	for _, c := range a.Choices {
		v.Choices = append(v.Choices, ChoiceView{Choice: c, Label: choiceLabel(c)})
	}
	// The draft is written last, from the fields and JSON the card shows.
	// v is a named result, so this deferred function sets the Draft of
	// the value approvalView returns, whichever return runs.
	defer func() { v.Draft = draftOf(v) }()

	m := argMap(a.Args)
	if m == nil {
		v.JSON = strings.Join(rpc.ArgsLines(a.Args, 0), "\n")
		return v
	}
	// Match keys without regard to case: servers write "to", "To" or "TO".
	byLower := map[string]string{}
	for k := range m {
		byLower[strings.ToLower(k)] = k
	}
	_, hasTo := byLower["to"]
	_, hasSubject := byLower["subject"]
	if hasTo || hasSubject {
		// The mail fields the card lifts out of the JSON, in the order it
		// shows them.
		mailFields := []struct{ key, label string }{
			{"to", "To"}, {"cc", "Cc"}, {"bcc", "Bcc"}, {"subject", "Subject"}, {"body", "Body"},
		}
		for _, f := range mailFields {
			k, ok := byLower[f.key]
			if !ok {
				continue
			}
			v.Fields = append(v.Fields, Field{Label: f.label, Value: fieldValue(m[k])})
			delete(m, k)
		}
	}
	if len(m) > 0 {
		v.JSON = indentJSON(m)
	}
	return v
}

// draftOf writes the text "Edit first" puts in the composer for card v:
// one line that says what to do, then the arguments as the card showed
// them, for the user to change and send as a new question. A mail card
// gives "Send this mail instead:" and its To, Cc, Subject and Body; any
// other card names the tool and its arguments as JSON.
func draftOf(v ApprovalView) string {
	var b strings.Builder
	switch {
	case len(v.Fields) > 0:
		verb := "Send"
		if strings.HasPrefix(v.Label, "Drafted") {
			verb = "Draft"
		}
		b.WriteString(verb + " this mail instead:\n")
		for _, f := range v.Fields {
			if f.Label == "Body" {
				continue
			}
			b.WriteString("\n" + f.Label + ": " + f.Value)
		}
		for _, f := range v.Fields {
			if f.Label == "Body" {
				b.WriteString("\n\n" + f.Value)
			}
		}
		if v.JSON != "" {
			b.WriteString("\n\nOther details: " + v.JSON)
		}
	case v.JSON != "":
		fmt.Fprintf(&b, "Run %s with these arguments instead:\n\n%s", v.Name, v.JSON)
	default:
		fmt.Fprintf(&b, "Run %s instead.", v.Name)
	}
	return b.String()
}

// fieldValue writes one argument for a card: a string as it is, a list of
// strings joined with ", ", anything else as JSON.
func fieldValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, p := range x {
			s, ok := p.(string)
			if !ok {
				return indentJSON(v)
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ", ")
	}
	return indentJSON(v)
}

// indentJSON writes v as JSON with two-space indents. encoding/json sorts
// a map's keys, so the card reads the same each time.
func indentJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	// SetEscapeHTML(false) keeps "<" and "&" as they are; the page puts
	// the text in the card as text, never as HTML.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(b.String(), "\n")
}

// choiceLabel returns the words a card's button shows for c.
func choiceLabel(c rpc.Choice) string {
	switch c {
	case rpc.ChoiceOnce:
		return "Allow once"
	case rpc.ChoiceSession:
		return "Allow for this chat"
	case rpc.ChoiceDeny:
		return "Don't allow"
	}
	return string(c)
}
