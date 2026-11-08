// This file tests that write_file and edit_file put a page's local pictures
// into it: which paths count as local, what stays as it is, and what the
// result tells the model.

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// photo is a picture's bytes for the tests; base64 of it is what a page
// should hold after the write.
var photo = []byte("\xff\xd8\xff\xe0 a family photo")

func TestWritePagePutsPicturesIn(t *testing.T) {
	tools, out := writeTools(t)
	if err := os.MkdirAll(filepath.Join(out, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "attachments", "IMG_0584.jpeg"), photo, 0o600); err != nil {
		t.Fatal(err)
	}
	data := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(photo)
	tests := []struct {
		name    string
		page    string // the path of the HTML file
		html    string
		has     []string
		hasNot  []string
		noteHas string
	}{
		{"relative to the page", "card.html", `<img src="attachments/IMG_0584.jpeg">`,
			[]string{`src="` + data + `"`}, []string{"attachments/IMG_0584.jpeg\""}, "Put 1 picture into the page"},
		{"from a subfolder", "cards/diwali.html", `<img src='../attachments/IMG_0584.jpeg'>`,
			[]string{`src='` + data + `'`}, nil, "Put 1 picture"},
		{"the output folder's own path", "card2.html", fmt.Sprintf(`<img src="%s">`, filepath.Join(out, "attachments", "IMG_0584.jpeg")),
			[]string{data}, nil, "Put 1 picture"},
		{"a CSS background, named twice", "card3.html", `<div style="background:url('attachments/IMG_0584.jpeg')"></div><img src="attachments/IMG_0584.jpeg">`,
			[]string{`url("` + data + `")`, `src="` + data + `"`}, nil, "Put 1 picture"}, // one photo, read once
		{"a web address stays", "card4.html", `<img src="https://example.com/a.png">`,
			[]string{`src="https://example.com/a.png"`}, nil, ""},
		{"a missing picture", "card5.html", `<img src="attachments/nope.jpg">`,
			[]string{`src="attachments/nope.jpg"`}, nil, "Left out, so the page shows nothing there: attachments/nope.jpg (no such file"},
		{"outside the output folder", "card6.html", `<img src="/etc/secret.png">`,
			[]string{`src="/etc/secret.png"`}, nil, "/etc/secret.png (outside"},
		{"climbing out with ..", "card7.html", `<img src="../../photo.jpg">`,
			[]string{`src="../../photo.jpg"`}, nil, "(outside"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(writeFileArgs{Path: tt.page, Content: tt.html})
			res := callWrite(t, tools, string(args))
			got, err := os.ReadFile(filepath.Join(out, tt.page))
			if err != nil {
				t.Fatalf("read the page: %v (result %q)", err, res.Text)
			}
			for _, s := range tt.has {
				if !strings.Contains(string(got), s) {
					t.Errorf("page lacks %.60q:\n%.200s", s, got)
				}
			}
			for _, s := range tt.hasNot {
				if strings.Contains(string(got), s) {
					t.Errorf("page still holds %q", s)
				}
			}
			switch {
			case tt.noteHas == "" && strings.Contains(res.Text, "picture"):
				t.Errorf("result mentions pictures with none to put in: %q", res.Text)
			case tt.noteHas != "" && !strings.Contains(res.Text, tt.noteHas):
				t.Errorf("result = %q, want it to say %q", res.Text, tt.noteHas)
			}
			if _, _, ok := Written(res.Text); !ok {
				t.Errorf("Written can't read the link from %q", res.Text)
			}
		})
	}
}

func TestEditPagePutsPicturesIn(t *testing.T) {
	tools, out := writeTools(t)
	if err := os.MkdirAll(filepath.Join(out, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "attachments", "IMG_0584.jpeg"), photo, 0o600); err != nil {
		t.Fatal(err)
	}
	callWrite(t, tools, `{"path":"card.html","content":"<h1>Happy Diwali</h1><!-- photo -->"}`)
	res, err := tools.Call(context.Background(), EditFile, json.RawMessage(
		`{"path":"card.html","old":"<!-- photo -->","new":"<img src=\"attachments/IMG_0584.jpeg\">"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(out, "card.html"))
	if !strings.Contains(string(got), "data:image/jpeg;base64,") || !strings.Contains(res.Text, "Put 1 picture into the page") {
		t.Errorf("edit result %q, page %.120s", res.Text, got)
	}
	// A second edit of the page, now past nothing but still a page, works.
	if _, err := tools.Call(context.Background(), EditFile, json.RawMessage(
		`{"path":"card.html","old":"Happy Diwali","new":"Wishing you a Diwali filled with light"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "card.html")); !strings.Contains(string(got), "filled with light") {
		t.Errorf("second edit didn't land: %.120s", got)
	}
}
