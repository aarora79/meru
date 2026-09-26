// This file tests the attachments: the file dialog and a drop each ask
// merud to copy the files, the page gets the chips or the reason a file
// stayed out, the cap holds, Detach takes a chip off, and Send puts one
// "Read this file" line per attachment in the question.

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// fakeUploads plays merud's attach_file: it copies each file into its
// uploads folder, and refuses a file whose name holds "secret" as merud
// refuses one that looks like it holds keys.
type fakeUploads struct {
	dir string // the uploads folder
}

// handler is the rpc.Handler for f.
func (f fakeUploads) handler(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
	if req.Op != rpc.OpAttachFile {
		return errors.New("unexpected op " + string(req.Op))
	}
	name := filepath.Base(req.Path)
	if strings.Contains(name, "secret") {
		return errors.New(name + " looks like it holds keys, passwords or tokens, so Meru won't read it")
	}
	raw, err := os.ReadFile(req.Path)
	if err != nil {
		return err
	}
	saved := filepath.Join(f.dir, name)
	if err := os.WriteFile(saved, raw, 0o600); err != nil {
		return err
	}
	return emit(rpc.Event{Type: rpc.EventSaved, Text: saved})
}

// isAttachments matches the nth KindAttachments update, counting from 1.
func isAttachments(r *recorder, n int) func(Update) bool {
	return func(Update) bool {
		seen := 0
		for _, u := range r.all() {
			if u.Kind == KindAttachments {
				seen++
			}
		}
		return seen >= n
	}
}

// lastAttachments returns the latest KindAttachments update.
func lastAttachments(r *recorder) Update {
	var last Update
	for _, u := range r.all() {
		if u.Kind == KindAttachments {
			last = u
		}
	}
	return last
}

// names lists the names on u's chips.
func names(u Update) []string {
	var out []string
	for _, a := range u.Attachments {
		out = append(out, a.Name)
	}
	return out
}

func TestAttach(t *testing.T) {
	home := t.TempDir()
	away := filepath.Join(home, "Downloads")
	uploads := filepath.Join(home, "meru-output", "uploads")
	for _, d := range []string{away, uploads} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := func(name string, size int) string {
		p := filepath.Join(away, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("a", size)), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plan, notes := file("garden-plan.pdf", 2600), file("seeds.md", 12)
	secret := file("secret-keys.txt", 3)
	var many []string
	for _, n := range []string{"a.md", "b.md", "c.md", "d.md", "e.md", "f.md", "g.md"} {
		many = append(many, file(n, 1))
	}

	var picked []string
	r := newRecorder()
	b := New(Options{Socket: startServer(t, fakeUploads{uploads}.handler), Home: home, Emit: r.emit,
		PickFiles: func() ([]string, error) { return picked, nil }})
	ctx := context.Background()

	// A cancelled dialog sends nothing.
	if err := b.AttachFile(ctx); err != nil || len(r.all()) != 0 {
		t.Fatalf("cancel = %v, updates %+v", err, r.all())
	}

	steps := []struct {
		name   string
		do     func()
		want   []string // the chips after the step
		notice string   // a piece of the notice; "" for none
	}{
		{"pick two", func() { picked = []string{plan, notes}; _ = b.AttachFile(ctx) }, []string{"garden-plan.pdf", "seeds.md"}, ""},
		{"a secret stays out", func() { picked = []string{secret}; _ = b.AttachFile(ctx) },
			[]string{"garden-plan.pdf", "seeds.md"}, "Not attached: secret-keys.txt looks like it holds keys"},
		{"remove one", func() { _ = b.Detach(0) }, []string{"seeds.md"}, ""},
		{"drop seven", func() { Drop(b, many) }, []string{"seeds.md", "a.md", "b.md", "c.md", "d.md"}, "5 files at most, so 3 files stayed out"},
		{"remove all", func() { b.DetachAll() }, nil, ""},
	}
	for i, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			st.do()
			r.waitFor(t, "attachments", isAttachments(r, i+1))
			u := lastAttachments(r)
			if got := names(u); strings.Join(got, ",") != strings.Join(st.want, ",") {
				t.Errorf("chips = %v, want %v", got, st.want)
			}
			if (st.notice == "") != (u.Notice == "") || !strings.Contains(u.Notice, st.notice) {
				t.Errorf("notice = %q, want %q", u.Notice, st.notice)
			}
		})
	}
	if err := b.Detach(0); err == nil {
		t.Error("Detach with no attachments went ahead")
	}

	// The chip shows the copy's path in the "~" form and its size.
	picked = []string{plan}
	_ = b.AttachFile(ctx)
	r.waitFor(t, "attachments", isAttachments(r, len(steps)+1))
	a := lastAttachments(r).Attachments
	if len(a) != 1 || a[0].Path != "~/meru-output/uploads/garden-plan.pdf" || a[0].Size != "2.5 KB" {
		t.Fatalf("attachment = %+v", a)
	}

	// Send carries one line per file and clears the chips. The fake has
	// no ask op, so the turn ends in an error, which is fine here.
	if err := b.Send("", "What goes in the north bed?", ""); err != nil {
		t.Fatal(err)
	}
	start := r.waitFor(t, "start", func(u Update) bool { return u.Kind == KindStart })
	want := "What goes in the north bed?\n\nRead this file: ~/meru-output/uploads/garden-plan.pdf"
	if start.Question != want {
		t.Errorf("question = %q, want %q", start.Question, want)
	}
	if got := lastAttachments(r).Attachments; len(got) != 0 {
		t.Errorf("chips after Send = %+v", got)
	}
	r.waitFor(t, "end", isEnd(1))

	// No dialog in this build.
	if err := New(Options{Socket: "/nonexistent"}).AttachFile(ctx); err == nil {
		t.Error("AttachFile with no dialog went ahead")
	}
}

func TestSizeText(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{1, "1 byte"},
		{812, "812 bytes"},
		{4300, "4.2 KB"},
		{2516582, "2.4 MB"},
	}
	for _, tt := range tests {
		if got := sizeText(tt.n); got != tt.want {
			t.Errorf("sizeText(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
