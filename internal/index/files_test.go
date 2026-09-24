// This file tests what the indexer lends the file tools: Check on one path,
// Walk over a folder, and ReadText for each kind of file.

package index

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// filesTree writes a small tree under a fresh folder and returns an
// Indexer over it, the folder, and a folder outside it.
func filesTree(t *testing.T) (*Indexer, string, string) {
	t.Helper()
	root, outside := tempRoot(t), tempRoot(t)
	writeFiles(t, root, map[string]string{
		"a.md":            "# A\n\nkept",
		"sub/b.txt":       "kept",
		"sub/.meruignore": "drafts/\n",
		"sub/drafts/c.md": "ignored",
		".env":            "K=V",
		"nul.txt":         "a\x00b",
		"page.html":       "<p>One &amp; two</p><script>x()</script>",
		"doc.pdf":         string(minimalPDF([]string{"First page", "Second page"})),
	})
	writeFiles(t, outside, map[string]string{"x.md": "outside"})
	if err := os.Symlink(outside, filepath.Join(root, "sub", "out")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	cfg := testConfig(root)
	cfg.MaxFileMB = 1
	ix, _, _ := newTestIndexer(t, cfg)
	return ix, root, outside
}

func TestCheck(t *testing.T) {
	ix, root, outside := filesTree(t)
	if err := os.WriteFile(filepath.Join(root, "big.md"), make([]byte, 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		path   string
		reason string
		err    error
	}{
		{"the folder itself", root, "", nil},
		{"kept file", filepath.Join(root, "a.md"), "", nil},
		{"kept folder", filepath.Join(root, "sub"), "", nil},
		{"secret", filepath.Join(root, ".env"), ReasonSecret, nil},
		{"ignored by a parent's .meruignore", filepath.Join(root, "sub", "drafts", "c.md"), ReasonIgnored, nil},
		{"NUL byte", filepath.Join(root, "nul.txt"), ReasonBinary, nil},
		{"over max_file_mb", filepath.Join(root, "big.md"), ReasonTooLarge, nil},
		{"symlink", filepath.Join(root, "sub", "out"), ReasonSymlink, nil},
		{"through a symlink", filepath.Join(root, "sub", "out", "x.md"), ReasonSymlink, nil},
		{"outside", filepath.Join(outside, "x.md"), "", ErrOutsideFolders},
		{"missing", filepath.Join(root, "nope.md"), "", fs.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ix.Check(tt.path)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Check err = %v, want %v", err, tt.err)
			}
			if err == nil && (c.Reason != tt.reason || c.Root != root || c.Path != tt.path) {
				t.Errorf("Check = %+v, want reason %q under %s", c, tt.reason, root)
			}
		})
	}
}

func TestCheckThroughLinkedFolder(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{"real/a.md": "kept"})
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	// The configured folder is the link; Check answers with the real path.
	ix, _, _ := newTestIndexer(t, testConfig(link))
	c, err := ix.Check(filepath.Join(link, "a.md"))
	want := filepath.Join(root, "real", "a.md")
	if err != nil || c.Path != want || c.Reason != "" {
		t.Errorf("Check = %+v, %v; want %s kept", c, err, want)
	}
}

func TestWalk(t *testing.T) {
	ix, root, _ := filesTree(t)
	got := map[string]string{}
	err := ix.Walk(context.Background(), root, func(p string, info fs.FileInfo, reason string) error {
		rel, _ := filepath.Rel(root, p)
		got[filepath.ToSlash(rel)] = reason
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := map[string]string{
		"a.md": "", "sub": "", "sub/b.txt": "", "sub/.meruignore": ReasonHidden,
		"sub/drafts": ReasonIgnored, "sub/out": ReasonSymlink, ".env": ReasonSecret,
		"nul.txt": ReasonBinary, "page.html": "", "doc.pdf": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Walk saw %v\nwant %v", got, want)
	}

	n := 0
	err = ix.Walk(context.Background(), root, func(string, fs.FileInfo, string) error {
		n++
		return fs.SkipAll
	})
	if err != nil || n != 1 {
		t.Errorf("Walk with SkipAll = %v after %d calls, want nil after 1", err, n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ix.Walk(ctx, root, func(string, fs.FileInfo, string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("Walk after cancel = %v, want context.Canceled", err)
	}
	if err := ix.Walk(context.Background(), t.TempDir(), nil); !errors.Is(err, ErrOutsideFolders) {
		t.Errorf("Walk outside = %v, want ErrOutsideFolders", err)
	}
}

func TestReadText(t *testing.T) {
	ix, root, _ := filesTree(t)
	tests := []struct {
		name   string
		file   string
		want   Text
		reason string
	}{
		{"markdown as is", "a.md", Text{Kind: KindMarkdown, Pages: []string{"# A\n\nkept"}}, ""},
		{"text", "sub/b.txt", Text{Kind: KindText, Pages: []string{"kept"}}, ""},
		{"html as text", "page.html", Text{Kind: KindHTML, Pages: []string{"One & two"}}, ""},
		{"pdf by page", "doc.pdf", Text{Kind: KindPDF, Pages: []string{"First page", "Second page"}}, ""},
		{"NUL byte", "nul.txt", Text{}, ReasonBinary},
		{"symlink", "sub/out", Text{}, ReasonSymlink},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason, err := ix.ReadText(filepath.Join(root, filepath.FromSlash(tt.file)))
			if err != nil {
				t.Fatalf("ReadText: %v", err)
			}
			for i := range got.Pages {
				got.Pages[i] = strings.TrimSpace(got.Pages[i])
			}
			if reason != tt.reason || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ReadText = %+v, %q; want %+v, %q", got, reason, tt.want, tt.reason)
			}
		})
	}

	if _, _, err := ix.ReadText(filepath.Join(t.TempDir(), "x.md")); !errors.Is(err, ErrOutsideFolders) {
		t.Errorf("ReadText outside = %v, want ErrOutsideFolders", err)
	}
	writeFiles(t, root, map[string]string{"scan.pdf": string(minimalPDF([]string{""}))})
	if _, _, err := ix.ReadText(filepath.Join(root, "scan.pdf")); !errors.Is(err, errNoText) {
		t.Errorf("ReadText of a PDF with no text = %v, want errNoText", err)
	}
}
