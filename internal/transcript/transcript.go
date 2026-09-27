// This file holds the Session type: creating a session file, opening an
// existing one by ID, appending lines, and reading the history back.

package transcript

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/engine"
)

// The line types. See ARCHITECTURE.md, "Session transcripts".
const (
	TypeUser      = "user"
	TypeAssistant = "assistant"
	// The tool lines, written by dispatch (v0.3).
	TypeToolCall   = "tool_call"
	TypeApproval   = "approval"
	TypeToolResult = "tool_result"
	// TypeSummary holds, in Text, a one- or two-sentence summary of the
	// session, which merud appends once the session has gone quiet (v0.4).
	TypeSummary = "summary"
	// TypeModelSwitch says that the answers from here on come from another
	// model: Tier is "main", From the model before and To the one after.
	// The agent writes one before an answer whose model differs from the
	// session's last one, so a session's first answer gets one with an
	// empty From. The turns table reads it to split usage by model.
	TypeModelSwitch = "model_switch"
)

// Line is one event in a transcript. Only the fields that matter for its Type
// are set; `omitempty` in the struct tag leaves the others out of the JSON.
type Line struct {
	TS        time.Time `json:"ts"`
	Type      string    `json:"type"`
	Text      string    `json:"text,omitempty"`
	TokensIn  int       `json:"tokens_in,omitempty"`
	TokensOut int       `json:"tokens_out,omitempty"`
	// Route is the route the turn took, and Sources the files whose
	// excerpts went into its prompt, on an assistant line. Ms is the
	// turn's duration on an assistant line, and the call's on a
	// tool_result line. The turns table and `meru usage` rebuild from them.
	Route   string   `json:"route,omitempty"`
	Sources []string `json:"sources,omitempty"`
	// Images holds the full paths of the images a question carried, on a
	// user line: copies in the uploads folder under [skills] output_dir.
	// The line keeps the paths, never the bytes.
	Images []string `json:"images,omitempty"`

	// The fields below belong to the tool lines (v0.3): "tool_call",
	// "approval" and "tool_result". CallID ties the three lines of one call
	// together, and replay uses it to rebuild the call's tool_calls row.
	CallID string `json:"call_id,omitempty"`
	// Kind is "mcp", "a2a" or "builtin". Server is the MCP server or A2A
	// agent name ("meru" for a built-in tool) and Tool the tool or skill
	// name without the server prefix.
	Kind   string `json:"kind,omitempty"`
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
	// Args holds the call's arguments as JSON, secrets redacted.
	Args json.RawMessage `json:"args,omitempty"`
	// Choice is the user's answer on an approval line: "once", "session"
	// or "deny".
	Choice string `json:"choice,omitempty"`
	// Outcome is how a call ended, on a tool_result line: "ok", "error",
	// "denied", "declined", "cancelled" or "timeout". OK repeats
	// Outcome == "ok" so the line reads plainly with grep. On an
	// assistant line, Outcome says how a turn ended without a full
	// answer: "timeout", "cut_off", "gave_up" or "bad_output"; it is empty
	// otherwise.
	Outcome string `json:"outcome,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	// Notice is the warning the user read under the answer, on an
	// assistant line: set when the answer claimed an action and no tool
	// call in the turn succeeded, or when the answer model couldn't call
	// tools, and empty otherwise. History hands it to the model with the
	// answer, so a later turn doesn't build on the claim.
	Notice string `json:"notice,omitempty"`
	// Ms is how long the call took, in milliseconds.
	Ms int64 `json:"ms,omitempty"`
	// TTFTMs, EvalMs, BadCalls and Capped describe how the model did, on
	// an assistant line: its time to first token on the round that first
	// wrote text, the time Ollama spent writing tokens over the turn, the
	// tool calls it wrote that Meru couldn't run as written, and whether
	// the turn used every round and still wrote no answer. `/usage by
	// model` adds them up.
	TTFTMs   int64 `json:"ttft_ms,omitempty"`
	EvalMs   int64 `json:"eval_ms,omitempty"`
	BadCalls int   `json:"bad_calls,omitempty"`
	Capped   bool  `json:"capped,omitempty"`
	// Tier, From and To belong to a model_switch line.
	Tier string `json:"tier,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Result is what the tool returned, as text, secrets redacted.
	Result string `json:"result,omitempty"`

	TraceID string `json:"trace_id,omitempty"`
}

// Session is one open transcript file. It holds only the file's path, so
// copying it or keeping many is cheap, and there is nothing to close: each
// Append opens the file, writes one line and closes it again.
type Session struct {
	id   string
	path string
}

// idPattern matches a session ID: the UTC start time to the second, a dash,
// and four random hex digits, as in 2026-09-23T101502-7f3a. Open checks every
// ID against it, which keeps an ID from a client (say "../../etc/passwd")
// from naming a file outside the sessions directory.
var idPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{6}-[0-9a-f]{4}$`)

// maxLineBytes caps one transcript line when reading, 16 MiB. An answer is
// far smaller; the cap stops a damaged file from eating memory.
const maxLineBytes = 16 << 20

// New creates a new, empty session file under dir (usually ~/.meru/sessions)
// and returns it. It creates the YYYY/MM directories with mode 0700 and the
// file with mode 0600, so only the user can read them.
//
// New fails when the directories or the file can't be created.
func New(dir string) (*Session, error) {
	now := time.Now().UTC()
	// Two sessions started in the same second get different random suffixes.
	// O_EXCL below makes a clash fail instead of sharing a file, so try a few
	// suffixes before giving up.
	for range 5 {
		id := now.Format("2006-01-02T150405") + "-" + randomHex(2)
		s := &Session{id: id, path: sessionPath(dir, id)}
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return nil, fmt.Errorf("create session directory: %w", err)
		}
		f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create session %s: %w", id, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("create session %s: %w", id, err)
		}
		return s, nil
	}
	return nil, errors.New("create session: five random names in a row were taken")
}

// Open returns the existing session with the given ID under dir. It fails when
// the ID isn't a valid session ID or no such session exists.
func Open(dir, id string) (*Session, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("session ID %q isn't valid", id)
	}
	s := &Session{id: id, path: sessionPath(dir, id)}
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("open session %s: not a regular file", id)
	}
	return s, nil
}

// ID returns the session's ID, which clients send back to continue it.
func (s *Session) ID() string { return s.id }

// Path returns the session file's path.
func (s *Session) Path() string { return s.path }

// Append writes l as one line at the end of the session file. A zero TS
// becomes the current time. Times are stored in UTC to the second, as in
// ARCHITECTURE.md.
//
// The file is opened with O_APPEND and each line goes out in a single write,
// so two writers never interleave inside a line, and a crash loses at most the
// line being written.
//
// (s *Session) before the name makes Append a method with a pointer
// receiver: it gets the Session's address, not a copy.
func (s *Session) Append(l Line) error {
	if l.TS.IsZero() {
		l.TS = time.Now()
	}
	l.TS = l.TS.UTC().Truncate(time.Second)

	b, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("encode transcript line: %w", err)
	}
	b = append(b, '\n')

	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("append to session %s: %w", s.id, err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return fmt.Errorf("append to session %s: %w", s.id, err)
	}
	// Close can report a failed write that the OS delayed, so check it.
	if err := f.Close(); err != nil {
		return fmt.Errorf("append to session %s: %w", s.id, err)
	}
	return nil
}

// History returns the last maxTurns turns of the session as model messages,
// oldest first. A turn is a user line and the assistant line that answered
// it. An answer that carries a notice gets it after its text, in square
// brackets, so the model reads that its claim didn't happen. A question
// that carried images gets one note per image after its text, such as
// "[image: receipt.jpg]", and never the image itself: see imageNotes. A
// user line with no answer (the turn failed or was cancelled) is left out,
// so the model never sees two questions in a row. maxTurns of zero or less
// returns nothing.
//
// History fails only when the file can't be read.
func (s *Session) History(maxTurns int) ([]engine.Message, error) {
	if maxTurns <= 0 {
		return nil, nil
	}
	lines, err := s.read()
	if err != nil {
		return nil, err
	}

	// Pair each user line with the assistant line that follows it.
	var turns [][2]engine.Message
	var question *Line
	for i := range lines {
		l := &lines[i]
		switch l.Type {
		case TypeUser:
			question = l // a later user line replaces an unanswered one
		case TypeAssistant:
			if question != nil {
				answer := l.Text
				if l.Notice != "" {
					answer += "\n\n[" + l.Notice + "]"
				}
				turns = append(turns, [2]engine.Message{
					{Role: engine.RoleUser, Content: question.Text + imageNotes(question.Images)},
					{Role: engine.RoleAssistant, Content: answer},
				})
				question = nil
			}
		}
	}

	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	msgs := make([]engine.Message, 0, 2*len(turns))
	for _, t := range turns {
		msgs = append(msgs, t[0], t[1])
	}
	return msgs, nil
}

// Model returns the main model the session's answers come from now: the
// To of its last model_switch line for the main tier, or "" when it has
// none, as a new session or one written before the line existed has. It
// fails only when the file can't be read.
func (s *Session) Model() (string, error) {
	lines, err := s.read()
	if err != nil {
		return "", err
	}
	return LastModel(lines), nil
}

// LastModel returns the To of the last main-tier model_switch line in
// lines, or "" when there is none.
func LastModel(lines []Line) string {
	model := ""
	for _, l := range lines {
		if l.Type == TypeModelSwitch && l.Tier == "main" {
			model = l.To
		}
	}
	return model
}

// imageNotes returns one line per image in paths, "[image: <name>]",
// after a blank line, or "" for none. A later turn sends the model this
// note in place of the image: the model knows the user shared one, and
// the prompt doesn't carry the bytes again. Each image costs hundreds of
// tokens and seconds of work, and a model's context fills fast; the turn
// that carried the image has the answer about it in its history already.
func imageNotes(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\n")
	for _, p := range paths {
		s.WriteString("\n[image: " + filepath.Base(p) + "]")
	}
	return s.String()
}

// read parses every line of the session file and skips lines that aren't
// valid JSON.
//
// A crash in the middle of Append can leave a torn line with no newline. The
// next Append then lands on the end of it, so a damaged line can sit anywhere
// in the file, not only at the end. Skipping it loses that one event and
// keeps the rest of the session readable.
func (s *Session) read() ([]Line, error) {
	return readFile(s.path, s.id)
}

// ReadLines parses every line of the transcript file at path and skips the
// lines that aren't valid JSON, as History does. The store's replay uses it
// to rebuild tool_calls from the files. It fails only when the file can't be
// read.
func ReadLines(path string) ([]Line, error) {
	return readFile(path, filepath.Base(path))
}

// readFile does the work of read and ReadLines. name identifies the file in
// errors.
func readFile(path, name string) ([]Line, error) {
	f, err := os.Open(path) // #nosec G304 -- a session file under merud's own sessions directory
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", name, err)
	}
	defer f.Close() // we only read, so Close has nothing useful to report

	var lines []Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue // a torn or empty line
		}
		lines = append(lines, l)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read session %s: %w", name, err)
	}
	return lines, nil
}

// ReadFrom parses the lines of the transcript file at path that start at
// byte offset and end in a newline. It returns them, skipping lines that
// aren't valid JSON as History does, and the offset just past the last
// newline it read. The store's replay passes that offset back next time,
// so each replay reads only the lines added since the last one.
//
// A last line with no newline yet is left for next time: Append may still
// be writing it, or a crash tore it. If a crash tore it, the next Append
// lands on its end and ends it with a newline, and the joined line fails
// to parse and is skipped. A line over 16 MiB is skipped as well.
//
// ReadFrom fails when the file can't be opened, read or seeked.
func ReadFrom(path string, offset int64) ([]Line, int64, error) {
	name := filepath.Base(path)
	f, err := os.Open(path) // #nosec G304 -- a session file under merud's own sessions directory
	if err != nil {
		return nil, offset, fmt.Errorf("read session %s: %w", name, err)
	}
	defer f.Close() // we only read, so Close has nothing useful to report
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("read session %s: %w", name, err)
	}

	var lines []Line
	r := bufio.NewReader(f)
	for {
		// ReadBytes returns everything up to and including the next '\n'.
		// At the end of the file it returns what is left, with io.EOF.
		b, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return lines, offset, nil // b, if any, is a line still being written
		}
		if err != nil {
			return nil, offset, fmt.Errorf("read session %s: %w", name, err)
		}
		offset += int64(len(b))
		if len(b) > maxLineBytes {
			continue
		}
		var l Line
		if err := json.Unmarshal(b, &l); err != nil {
			continue // a torn or empty line
		}
		lines = append(lines, l)
	}
}

// sessionPath returns dir/YYYY/MM/<id>.jsonl. It trusts id to match
// idPattern; the year and month come from the ID's first characters.
func sessionPath(dir, id string) string {
	return filepath.Join(dir, id[0:4], id[5:7], id+".jsonl")
}

// randomHex returns n random bytes written as 2n hex digits.
func randomHex(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error on supported platforms; it
	// crashes the program instead if the OS has no randomness to give.
	rand.Read(b)
	return hex.EncodeToString(b)
}
