// This file answers OpSaveFile, which saves a chat or one answer as a
// Markdown file in [skills] output_dir, for the desktop app's "Share as
// file" and "Save to a note". merud writes the file through the
// write_file tool, by way of dispatch, so the save is logged in tool_calls
// and the session's transcript and asks first exactly as write_file does
// (AGENTS.md, non-negotiable 4).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/transcript"
)

// The folders inside output_dir that saved chats and notes go in.
const (
	chatsFolder = "chats"
	notesFolder = "notes"
)

// maxSlug caps the words a file name takes from the title.
const maxSlug = 6

// saveService saves chats and answers through write_file.
type saveService struct {
	dispatcher  *dispatch.Dispatcher
	sessionsDir string
	outputDir   string // [skills] output_dir, "~" expanded; "" when write_file has nowhere to write
	home        string // for showing source paths as ~/...
	now         func() time.Time
}

// handleSave answers OpSaveFile. It builds the Markdown, picks a file name
// under chats/ or notes/ that doesn't exist yet, and dispatches one
// write_file call in the session req.Session, with approve to ask the user
// as any write_file call does. The reply is one "saved" event whose Text is
// the file's full path.
//
// It fails when the kind is unknown, the session doesn't exist, a note has
// no text, there is no output folder, or the call doesn't run: the user
// said no, write_file is off, or the write failed. The error says which.
func (s saveService) handleSave(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
	if req.Kind != rpc.SaveChat && req.Kind != rpc.SaveNote {
		return fmt.Errorf("save_file saves a %q or a %q, not %q", rpc.SaveChat, rpc.SaveNote, req.Kind)
	}
	if s.outputDir == "" {
		return errors.New("there is no output folder: set [skills] output_dir in config.toml")
	}
	sess, err := transcript.Open(s.sessionsDir, req.Session)
	if err != nil {
		return err
	}
	lines, err := sess.Lines()
	if err != nil {
		return err
	}
	turns := turnsOf(lines, s.home)
	if len(turns) == 0 {
		return errors.New("this chat has no questions yet")
	}

	now := s.now()
	folder, title, content := chatsFolder, turns[0].Question, chatMarkdown(turns, now)
	if req.Kind == rpc.SaveNote {
		text := strings.TrimSpace(req.Text)
		if text == "" {
			return errors.New("the note is empty")
		}
		folder, title, content = notesFolder, noteTitle(text, turns), text+"\n"
	}
	rel := s.freeName(folder, now.Format("2006-01-02")+"-"+slug(title))
	args, err := json.Marshal(map[string]string{"path": rel, "content": content})
	if err != nil {
		return err
	}
	res, out := s.dispatcher.Dispatch(ctx, dispatch.Call{
		ID:      fmt.Sprintf("save-%d", now.UnixNano()),
		Name:    builtin.WriteFile,
		Args:    args,
		Session: sess.ID(),
		Source:  saveSource(req.Source),
		Append:  sess.Append,
		Approve: approve,
	})
	switch out.Outcome {
	case dispatch.OutcomeOK:
		return emit(rpc.Event{Type: rpc.EventSaved, Text: filepath.Join(s.outputDir, rel)})
	case dispatch.OutcomeDeclined:
		return errors.New("not saved: you didn't allow write_file")
	case dispatch.OutcomeDenied:
		return errors.New("not saved: write_file is off; turn it on in Settings under Connections, or with /mcp in meru chat")
	}
	return fmt.Errorf("not saved: %s", res.Text)
}

// saveSource returns the client a save came from, for the tool call's
// audit line and metrics: `meru chat` says so, and any other request is
// the desktop app's, as every save was before the chat had /save. The
// value stays one of a small fixed set, as a metric attribute must.
func saveSource(s rpc.Source) rpc.Source {
	if s == rpc.SourceTUI {
		return rpc.SourceTUI
	}
	return rpc.SourceDesktop
}

// freeName returns folder/base.md, or folder/base-2.md and so on when that
// file exists already, as a path relative to the output folder. write_file
// refuses to replace a file, and a save should never ask to.
func (s saveService) freeName(folder, base string) string {
	name := filepath.Join(folder, base+".md")
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(s.outputDir, name)); err != nil {
			return filepath.ToSlash(name)
		}
		name = filepath.Join(folder, fmt.Sprintf("%s-%d.md", base, i))
	}
}

// chatMarkdown writes a chat as Markdown: its first question as the title,
// the date it was saved, then each question as a heading with the answer
// under it and the files it used.
func chatMarkdown(turns []rpc.TurnInfo, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nSaved from Meru on %s.\n", oneLine(turns[0].Question), now.Format("2 January 2006"))
	for _, t := range turns {
		fmt.Fprintf(&b, "\n## %s\n\n", oneLine(t.Question))
		answer := strings.TrimSpace(t.Answer)
		if answer == "" {
			answer = "_No answer._"
		}
		b.WriteString(answer + "\n")
		if len(t.Sources) > 0 {
			b.WriteString("\nSources:\n\n")
			for _, p := range t.Sources {
				fmt.Fprintf(&b, "- %s\n", p)
			}
		}
	}
	return b.String()
}

// noteTitle picks the words a note's file name comes from: the note's
// first heading or line, and failing that the chat's last question.
func noteTitle(text string, turns []rpc.TurnInfo) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return line
		}
	}
	return turns[len(turns)-1].Question
}

// oneLine puts s on one line, for a Markdown heading.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// slug turns a title into a file name part: the first few words, lower
// case, letters and digits only, joined with "-". "What time is check-in
// at the Lisbon hotel?" gives "what-time-is-check-in-at". An empty result
// becomes "meru".
func slug(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > maxSlug {
		words = words[:maxSlug]
	}
	if len(words) == 0 {
		return "meru"
	}
	return strings.Join(words, "-")
}
