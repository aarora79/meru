// This file tests AttachmentText through a real Dispatcher: a fake MCP
// tool saves files in the attachments folder and names them in its
// result, and the test checks what text dispatch adds.

package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// mailServer is a fake MCP backend with one tool, google.get_attachment.
// Its call runs save, which writes files as the google server would, then
// returns text.
type mailServer struct {
	save func()
	text string
}

// Kind returns dispatch.KindMCP.
func (m *mailServer) Kind() string { return dispatch.KindMCP }

// Tools offers the one tool.
func (m *mailServer) Tools() []engine.ToolSpec {
	return []engine.ToolSpec{{Name: "google.get_attachment"}}
}

// Confirm never asks.
func (m *mailServer) Confirm(string) dispatch.Confirm { return dispatch.ConfirmNever }

// Locate splits the name at its first dot.
func (m *mailServer) Locate(name string) (string, string) {
	server, tool, _ := strings.Cut(name, ".")
	return server, tool
}

// Call saves the files and returns the text.
func (m *mailServer) Call(context.Context, string, json.RawMessage) (dispatch.Result, error) {
	if m.save != nil {
		m.save()
	}
	return dispatch.Result{Text: m.text}, nil
}

// Status reports the server as connected.
func (m *mailServer) Status() []rpc.ServerInfo {
	return []rpc.ServerInfo{{Name: "google", Kind: dispatch.KindMCP, Connected: true}}
}

func TestAttachmentText(t *testing.T) {
	pdf, err := os.ReadFile(filepath.Join("testdata", "two-pages.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	const folio = "Dana_Reyes_folio_3f2a9c1e-7b4d-4e8a-9c0f-1a2b3c4d5e6f.pdf"
	pages := []string{"--- Meru read " + folio, "--- page 1 ---\nHarvest report for the orchard", "--- page 2 ---\nThe pear crop doubled"}
	// result is what the google server says after it saves a file.
	result := func(name string) string {
		return "Attachment downloaded successfully!\nFilename: folio.pdf\nSaved filename: " + name + "\nSize: 82.6 KB\n\nThe file will expire after 1 hour."
	}

	tests := []struct {
		name  string
		files map[string]string // saved during the call, by name
		old   bool              // the files' modified time is two days back
		link  string            // a symlink saved during the call, to a file outside
		text  string            // the tool's result
		want  []string          // pieces of what dispatch adds
		none  bool              // dispatch adds nothing
		count int               // how many "Saved at" lines; 0 means 1 unless none
	}{
		{name: "a fresh pdf", files: map[string]string{folio: string(pdf)}, text: result(folio),
			want: append([]string{"Saved at ", "/attachments/" + folio + ". Meru read all of it"}, pages...)},
		{name: "an old pdf", files: map[string]string{folio: string(pdf)}, old: true, text: result(folio), none: true},
		{name: "a name not in the folder", files: map[string]string{folio: string(pdf)}, text: result("Dana_Reyes_receipt.pdf"), none: true},
		{name: "a symlink", link: "folio_link.md", text: result("folio_link.md"),
			want: []string{"/attachments/folio_link.md. Meru can't read it: symbolic link."}},
		{name: "a file type Meru doesn't read", files: map[string]string{"folio.zip": "PK"}, text: result("folio.zip"),
			want: []string{"/attachments/folio.zip. Meru can't read it: "}},
		{name: "a long file is cut", files: map[string]string{"folio.md": longText()}, text: result("folio.md"),
			want: []string{"Read it with read_file; the text below is its first part.", "--- Meru read folio.md (12000 of 30000 characters) ---",
				"[18000 more characters. To read on, call read_file with path ", "/attachments/folio.md\" and offset 12000.]"}},
		{name: "a long result leaves less room", files: map[string]string{"folio.md": longText()},
			text: result("folio.md") + strings.Repeat("x", 10000),
			want: []string{"the text below is its first part.", "To read on, call read_file with path "}},
		{name: "at most two files", files: map[string]string{"a.md": "first", "b.md": "second", "c.md": "third"},
			text: "Saved a.md, b.md and c.md.", want: []string{"first", "second"}, count: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			notes := filepath.Join(base, "notes")
			out := filepath.Join(base, "meru-output")
			att := filepath.Join(out, attachmentsFolder)
			for _, d := range []string{notes, att} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(base, "outside.md")
			if err := os.WriteFile(outside, []byte("pear outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			ix, err := index.New(config.Index{Folders: []string{notes}, MaxFileMB: 1}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			tools := New(filepath.Join(base, "config.toml"), config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, out, ix, nil, nil)

			save := func() {
				for name, body := range tt.files {
					p := filepath.Join(att, name)
					if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
					if tt.old {
						then := time.Now().Add(-48 * time.Hour)
						if err := os.Chtimes(p, then, then); err != nil {
							t.Fatal(err)
						}
					}
				}
				if tt.link != "" {
					if err := os.Symlink(outside, filepath.Join(att, tt.link)); err != nil {
						t.Skipf("symlinks unsupported here: %v", err)
					}
				}
			}
			d := dispatch.New([]dispatch.Backend{tools, &mailServer{save: save, text: tt.text}}, nil,
				dispatch.Options{Attachments: tools.AttachmentText})
			res, out2 := d.Dispatch(context.Background(), dispatch.Call{ID: "c1", Name: "google.get_attachment", Args: json.RawMessage(`{}`)})
			if out2.Outcome != dispatch.OutcomeOK {
				t.Fatalf("outcome = %s: %s", out2.Outcome, res.Text)
			}
			added, ok := strings.CutPrefix(res.Text, tt.text)
			if !ok {
				t.Fatalf("result = %q, want it to start with the tool's text", res.Text)
			}
			if tt.none {
				if added != "" {
					t.Errorf("added %q, want nothing", added)
				}
				return
			}
			want := max(tt.count, 1)
			if n := strings.Count(added, "\n\nSaved at "); n != want {
				t.Errorf("added %d attachments, want %d: %q", n, want, added)
			}
			for _, w := range tt.want {
				if !strings.Contains(added, w) {
					t.Errorf("added %q, want it to hold %q", added, w)
				}
			}
			if strings.Contains(added, "pear outside") || strings.Contains(added, "third") {
				t.Errorf("added text it must not: %q", added)
			}
			if n := utf8.RuneCountInString(res.Text); n > dispatch.MaxModelResult {
				t.Errorf("result has %d characters, over dispatch's cut", n)
			}

			// A built-in's result is left alone, even when it names a
			// fresh attachment: read_file returns the page it was asked for.
			if len(tt.files) != 1 || strings.Contains(added, "can't read it") {
				return
			}
			for name := range tt.files {
				args, _ := json.Marshal(map[string]string{"path": name})
				res, _ := d.Dispatch(context.Background(), dispatch.Call{ID: "c2", Name: ReadFile, Args: args})
				if res.IsError {
					t.Fatalf("read_file %s: %s", name, res.Text)
				}
				if strings.Contains(res.Text, "Saved at ") || strings.Contains(res.Text, "--- Meru read") {
					t.Errorf("read_file result got attachment text: %q", res.Text)
				}
			}
		})
	}
}
