// This file tests a turn that reads a mail attachment: the model, played
// by the fake Ollama, calls a fake google tool that saves a PDF in the
// attachments folder, dispatch adds the PDF's text to the result, and the
// model answers in the next round without calling read_file.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// attachmentServer is a fake google MCP server with one tool. Each call
// saves the PDF under name in dir and reports the name, as the real
// server's get_gmail_attachment_content does.
type attachmentServer struct {
	dir  string
	name string
	pdf  []byte
}

// attachmentTool is the fake server's one tool.
const attachmentTool = "google.get_gmail_attachment_content"

// Kind returns dispatch.KindMCP.
func (s *attachmentServer) Kind() string { return dispatch.KindMCP }

// Tools offers the one tool.
func (s *attachmentServer) Tools() []engine.ToolSpec { return []engine.ToolSpec{spec(attachmentTool)} }

// Confirm never asks.
func (s *attachmentServer) Confirm(string) dispatch.Confirm { return dispatch.ConfirmNever }

// Locate splits the name at its first dot.
func (s *attachmentServer) Locate(name string) (string, string) {
	server, tool, _ := strings.Cut(name, ".")
	return server, tool
}

// Call saves the PDF and says where, in the real server's words.
func (s *attachmentServer) Call(context.Context, string, json.RawMessage) (dispatch.Result, error) {
	if err := os.WriteFile(filepath.Join(s.dir, s.name), s.pdf, 0o600); err != nil {
		return dispatch.Result{}, err
	}
	return dispatch.Result{Text: "Attachment downloaded successfully!\nFilename: folio.pdf\nSaved filename: " + s.name +
		"\nSize: 82.6 KB\n\nThe file will expire after 1 hour."}, nil
}

// Status reports the server as connected.
func (s *attachmentServer) Status() []rpc.ServerInfo {
	return []rpc.ServerInfo{{Name: "google", Kind: dispatch.KindMCP, Connected: true}}
}

func TestTurnReadsASavedAttachment(t *testing.T) {
	cfg := testConfig(t)
	base := t.TempDir()
	notes := filepath.Join(base, "notes")
	out := filepath.Join(base, "meru-output")
	att := filepath.Join(out, "attachments")
	for _, d := range []string{notes, att} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pdf, err := os.ReadFile(filepath.Join("..", "builtin", "testdata", "two-pages.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Index.Folders = []string{notes}
	ix, err := index.New(cfg.Index, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := builtin.New(filepath.Join(cfg.Dir, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, out, ix, nil, nil)
	mail := &attachmentServer{dir: att, name: "Dana_Reyes_folio_3f2a9c1e.pdf", pdf: pdf}
	disp := dispatch.New([]dispatch.Backend{tools, mail}, nil, dispatch.Options{Attachments: tools.AttachmentText})

	srv := fakeollama.Start(t, fakeollama.Config{})
	srv.Enqueue(cfg.Models.Main,
		fakeollama.Reply{ToolCalls: []fakeollama.ToolCall{{Name: attachmentTool, Arguments: map[string]any{"message_id": "m1"}}}},
		fakeollama.Reply{Text: "The folio says the pear crop doubled."},
	)
	eng, err := engine.NewOllama(srv.URL, "", cfg.Models.Embed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(cfg, eng, &fakeRouter{dec: Decision{Route: "tools", Confidence: 0.9, Outcome: "ok"}}, nil, disp, nil, nil, quietLog())

	evs, err := run(context.Background(), a, rpc.Request{Text: "what does the folio attached to Dana's mail say?"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var calls []string
	var answer strings.Builder
	for _, ev := range evs {
		switch ev.Type {
		case rpc.EventToolCall:
			calls = append(calls, ev.Tool.Name)
		case rpc.EventToken:
			answer.WriteString(ev.Text)
		case rpc.EventError:
			t.Fatalf("error event: %s", ev.Error)
		}
	}
	if len(calls) != 1 || calls[0] != attachmentTool {
		t.Errorf("tool calls = %v, want the attachment tool once and no read_file", calls)
	}
	if answer.String() != "The folio says the pear crop doubled." {
		t.Errorf("answer = %q", answer.String())
	}

	// The second chat request carries the tool result with the PDF's text.
	chats := srv.Requests("/api/chat")
	if len(chats) != 2 {
		t.Fatalf("fake Ollama got %d chat requests, want 2", len(chats))
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(chats[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	last := body.Messages[len(body.Messages)-1]
	for _, want := range []string{"Saved filename: Dana_Reyes_folio_3f2a9c1e.pdf", "Saved at ", "--- Meru read Dana_Reyes_folio_3f2a9c1e.pdf",
		"--- page 2 ---\nThe pear crop doubled"} {
		if last.Role != "tool" || !strings.Contains(last.Content, want) {
			t.Errorf("last message = %s %q, want a tool result holding %q", last.Role, last.Content, want)
		}
	}
}
