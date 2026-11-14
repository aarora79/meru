// This file decides whether a chat has read content from outside the
// machine: mail, a web page, another server's answer. Once it has, every
// tool that asks asks every time, and remember asks too, so words in that
// content can't act for the user unseen. dispatch applies the rule; this
// file only says when a chat has crossed the line. See
// docs/design/security.md and dispatch.Call.Outside.

package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/transcript"
)

// outsideFolders are the folders under the output folder where text from
// outside lands as files: mail attachments the google server saves, and
// downloads web_fetch saves. Reading one of those files brings in someone
// else's words, as reading the mail or the page did.
var outsideFolders = []string{"attachments", "downloads"}

// readsOutside reports whether a call to the tool named name, its full
// name such as "google.search_gmail_messages", of kind, with args, brings
// in text someone other than the user wrote:
//
//   - an MCP, A2A or [[commands]] tool that runs without asking: the tools
//     that read, such as a mail search. A tool that asks first, such as a
//     send, doesn't count: the user saw that call and said yes, and its
//     result is the server's word that it ran. Counting it would mark the
//     chat at the first approved send, and "for this chat" could then never
//     hold for any server's tool. asks reports whether a tool asks; a tool
//     it doesn't know counts as one that reads.
//   - the two web tools;
//   - read_file or grep on a path under outputDir's attachments or
//     downloads folder.
//
// The other built-ins read the user's own machine (files, notes, the date,
// Meru's facts) and don't count. outputDir is the output folder, such as
// ~/meru-output, and home the user's home folder, which "~/" stands for;
// an empty outputDir counts no path.
//
// Commands count because some print text strangers wrote, such as an issue
// thread; telling those apart would need a setting per command.
func readsOutside(kind, name string, args json.RawMessage, outputDir, home string, asks func(string) bool) bool {
	if kind != dispatch.KindBuiltin {
		return asks == nil || !asks(name)
	}
	if builtin.IsWebTool(name) {
		return true
	}
	if name != builtin.ReadFile && name != builtin.Grep {
		return false
	}
	return underOutside(pathArg(args), outputDir, home)
}

// pathArg returns the "path" argument of a call's args, or "" when there
// is none or args aren't a JSON object.
func pathArg(args json.RawMessage) string {
	// The struct names only the field this reads; json.Unmarshal skips
	// the rest.
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Path
}

// underOutside reports whether path sits in one of outsideFolders under
// outputDir. Both get "~/" expanded to home and are cleaned first, so
// "~/meru-output/attachments/x.pdf", "~/meru-output/./attachments/x.pdf"
// and the same path in full all count.
func underOutside(path, outputDir, home string) bool {
	if path == "" || outputDir == "" {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(expandHome(path, home)))
	base := filepath.ToSlash(filepath.Clean(expandHome(outputDir, home)))
	for _, f := range outsideFolders {
		dir := base + "/" + f
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return true
		}
	}
	return false
}

// expandHome returns p with a leading "~/" replaced by home, and p as it is
// otherwise.
func expandHome(p, home string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok && home != "" {
		return filepath.Join(home, rest)
	}
	return p
}

// fullName returns the name the model calls a tool by, from the kind,
// server and tool a tool_call line holds: "google.search_gmail_messages",
// "a2a.travel.plan", "cmd.disk-free" or "read_file". It undoes the split
// dispatch makes when it writes the line.
func fullName(kind, server, tool string) string {
	switch kind {
	case dispatch.KindMCP:
		return server + "." + tool
	case dispatch.KindA2A:
		return "a2a." + server + "." + tool
	}
	// A command's Tool already holds "cmd.<name>", and a built-in's its
	// own name.
	return tool
}

// readOutside reports whether lines, a chat's transcript, show a call that
// readsOutside and ran. A tool_call line names the tool and its kind; the
// tool_result line with the same call ID says how it ended. A call that
// was denied, declined or cancelled brought nothing in, so only "ok" and
// "error" count: an error from a server is still its words.
func readOutside(lines []transcript.Line, outputDir, home string, asks func(string) bool) bool {
	// call is what a tool_call line says, kept until its result arrives.
	type call struct {
		kind, name string
		args       json.RawMessage
	}
	calls := map[string]call{}
	for _, l := range lines {
		switch l.Type {
		case transcript.TypeToolCall:
			calls[l.CallID] = call{kind: l.Kind, name: fullName(l.Kind, l.Server, l.Tool), args: l.Args}
		case transcript.TypeToolResult:
			c, ok := calls[l.CallID]
			if !ok || (l.Outcome != dispatch.OutcomeOK && l.Outcome != dispatch.OutcomeError) {
				continue
			}
			if readsOutside(c.kind, c.name, c.args, outputDir, home, asks) {
				return true
			}
		}
	}
	return false
}

// asksFirst returns the asks function readsOutside takes: whether config
// allows the tool named name and it asks before it runs. A tool the runner
// doesn't list, such as one from a server since removed, counts as one
// that doesn't ask, so its old results still mark the chat. It returns nil
// when the agent has no tools, and then every non-built-in call counts.
func (a *Agent) asksFirst() func(string) bool {
	if a.tools == nil {
		return nil
	}
	listed := map[string]bool{}
	for _, s := range a.tools.Tools() {
		listed[s.Name] = true
	}
	return func(name string) bool { return listed[name] && a.tools.Asks(name) }
}
