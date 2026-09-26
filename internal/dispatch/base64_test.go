// This file tests stripBase64, and that Dispatch strips base64 from MCP
// and A2A results before it adds an attachment's text.

package dispatch

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// blob returns n characters of standard base64, the shape a tool sends a
// file in, with "+" and "/" among them.
func blob(n int) string {
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = byte(i*7 + 3)
	}
	return base64.StdEncoding.EncodeToString(raw)[:n]
}

// wrap breaks s into lines of width characters, as a MIME encoder does.
func wrap(s string, width int) string {
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width] + "\r\n")
		s = s[width:]
	}
	b.WriteString(s)
	return b.String()
}

func TestStripBase64(t *testing.T) {
	// id is shaped like a Gmail attachment ID: about 400 characters of
	// base64url. The model passes it back, so it must survive.
	id := strings.NewReplacer("+", "-", "/", "_").Replace(blob(400))
	note := func(n int) string { return fmt.Sprintf(base64Note, n) }
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a PDF sent as base64",
			in:   "📦 Base64 content (109068 chars, standard base64):\n" + blob(109068) + "\nSaved filename: folio.pdf",
			want: "📦 Base64 content (109068 chars, standard base64):\n" + note(109068) + "\nSaved filename: folio.pdf",
		},
		{
			name: "a run inside a line",
			in:   `{"data": "` + blob(5000) + `", "size": 3750}`,
			want: `{"data": "` + note(5000) + `", "size": 3750}`,
		},
		{
			name: "wrapped at 76 characters",
			in:   "Content:\r\n" + wrap(blob(3000), 76) + "\r\nEnd.",
			want: "Content:\r\n" + note(3000) + "\r\nEnd.",
		},
		{
			name: "attachment IDs kept",
			in:   "Attachment ID: " + id + "\nAttachment ID: " + id + "\n" + strings.Repeat("Subject: lunch plans for the week\n", 60),
			want: "Attachment ID: " + id + "\nAttachment ID: " + id + "\n" + strings.Repeat("Subject: lunch plans for the week\n", 60),
		},
		{
			name: "IDs one to a line kept",
			in:   strings.Repeat(id+"\n", 10),
			want: strings.Repeat(id+"\n", 10),
		},
		{
			name: "a run under the threshold kept",
			in:   "data: " + blob(minBase64-1) + " end",
			want: "data: " + blob(minBase64-1) + " end",
		},
		{
			name: "a run at the threshold removed",
			in:   "data: " + blob(minBase64) + " end",
			want: "data: " + note(minBase64) + " end",
		},
		{
			name: "short wrapped base64 kept",
			in:   wrap(blob(600), 76),
			want: wrap(blob(600), 76),
		},
		{
			name: "prose kept",
			in:   strings.Repeat("The meeting moved to Tuesday at ten. ", 100),
			want: strings.Repeat("The meeting moved to Tuesday at ten. ", 100),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBase64(tt.in)
			if got != tt.want {
				t.Errorf("stripBase64 = %.300q\nwant           %.300q", got, tt.want)
			}
		})
	}
}

// TestDispatchStripsBase64 runs an attachment call whose result carries
// the file as base64, and checks that the model, the transcript and the
// row get the note in its place, with the attachment's text still added
// after it. A built-in tool's result keeps its base64.
func TestDispatchStripsBase64(t *testing.T) {
	result := "Saved filename: folio.pdf\n📦 Base64 content (109068 chars, standard base64):\n" + blob(109068)
	tests := []struct {
		name     string
		kind     string
		tool     string
		stripped bool
	}{
		{"mcp", KindMCP, "google.get_gmail_attachment_content", true},
		{"a2a", KindA2A, "a2a.mail.fetch", true},
		{"built-in", KindBuiltin, "read_file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{kind: tt.kind, tools: []string{tt.tool},
				call: func(context.Context) (Result, error) { return Result{Text: result}, nil }}
			var seen string // the text Attachments got
			attach := func(text string, since time.Time) string {
				seen = text
				return "\n\nSaved at ~/meru-output/attachments/folio.pdf. Room 214."
			}
			rec := &fakeRecorder{}
			tr := &sink{}
			d := New([]Backend{b}, rec, Options{Attachments: attach})
			res, _ := d.Dispatch(context.Background(), newCall(tt.tool, tr, nil))

			if !tt.stripped {
				if strings.Contains(res.Text, "removed by Meru") {
					t.Errorf("built-in result lost its base64: %.200q", res.Text)
				}
				return
			}
			want := "Saved filename: folio.pdf\n📦 Base64 content (109068 chars, standard base64):\n" +
				fmt.Sprintf(base64Note, 109068) + "\n\nSaved at ~/meru-output/attachments/folio.pdf. Room 214."
			for where, text := range map[string]string{"model": res.Text, "transcript": tr.lines[1].Result, "row": rec.rows[0].Result} {
				if text != want {
					t.Errorf("%s result = %.300q\nwant %.300q", where, text, want)
				}
			}
			if strings.Contains(seen, blob(100)) {
				t.Error("Attachments got the base64; it should run on the stripped text")
			}
		})
	}
}
