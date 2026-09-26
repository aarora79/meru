// This file tests the skip rules: the gitignore pattern matcher on its own,
// then every default skip category and every ignore source through a real
// Scan over a temporary folder.

package index

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		isDir   bool
		matched bool
		ignored bool
	}{
		{"*.log", "a.log", false, true, true},
		{"*.log", "deep/down/a.log", false, true, true},
		{"*.log", "a.log.txt", false, false, false},
		{"!keep.log", "keep.log", false, true, false},
		{"build/", "build", true, true, true},
		{"build/", "build", false, false, false}, // a file named build
		{"build/", "src/build", true, true, true},
		{"/drafts", "drafts", true, true, true},
		{"/drafts", "sub/drafts", true, false, false},
		{"docs/*.md", "docs/a.md", false, true, true},
		{"docs/*.md", "docs/deep/a.md", false, false, false},
		{"docs/*.md", "x/docs/a.md", false, false, false}, // a middle slash anchors
		{"**/tmp", "tmp", true, true, true},
		{"**/tmp", "a/b/tmp", true, true, true},
		{"tmp/**", "tmp/a/b.md", false, true, true},
		{"tmp/**", "tmp", true, false, false},
		{"a/**/b.md", "a/b.md", false, true, true},
		{"a/**/b.md", "a/x/y/b.md", false, true, true},
		{"file?.md", "file1.md", false, true, true},
		{"file?.md", "file10.md", false, false, false},
		{"[abc].md", "b.md", false, true, true},
		{"[!abc].md", "b.md", false, false, false},
		{"[!abc].md", "d.md", false, true, true},
		{`\#notes.md`, "#notes.md", false, true, true},
		{`\!bang.md`, "!bang.md", false, true, true},
		{"a.md   ", "a.md", false, true, true}, // trailing spaces trimmed
		{"# comment", "# comment", false, false, false},
		{"", "anything", false, false, false},
		{"a+b.md", "a+b.md", false, true, true}, // regexp characters are literal
		{"a.md", "aXmd", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.path, func(t *testing.T) {
			ps, errs := parsePatterns([]string{tt.pattern})
			if len(errs) > 0 {
				t.Fatalf("parse: %v", errs)
			}
			m, ig := matchPatterns(ps, tt.path, tt.isDir)
			if m != tt.matched || ig != tt.ignored {
				t.Errorf("match(%q, %q) = %v, %v; want %v, %v", tt.pattern, tt.path, m, ig, tt.matched, tt.ignored)
			}
		})
	}
}

func TestPatternLastMatchWins(t *testing.T) {
	ps, _ := parsePatterns([]string{"*.md", "!keep.md", "keep.md"})
	if _, ig := matchPatterns(ps, "keep.md", false); !ig {
		t.Error("keep.md: the last pattern ignores it again")
	}
	ps, _ = parsePatterns([]string{"*.md", "!keep.md"})
	if _, ig := matchPatterns(ps, "keep.md", false); ig {
		t.Error("keep.md: the negation should re-include it")
	}
}

func TestNewRejectsBadConfigPattern(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Ignore = []string{"[z-a].md"}
	if _, err := New(cfg, newFakeSink(), &fakeEngine{}, nil); err == nil || !strings.Contains(err.Error(), "index.ignore") {
		t.Errorf("New with a broken class = %v; want an index.ignore error", err)
	}
}

func TestNewFolders(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	cfg := testConfig("~/notes")
	ix, err := New(cfg, newFakeSink(), &fakeEngine{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "notes"); ix.configured()[0] != want {
		t.Errorf("folder = %q, want %q", ix.configured()[0], want)
	}
	cfg.Folders = []string{"relative/notes"}
	if _, err := New(cfg, newFakeSink(), &fakeEngine{}, nil); err == nil {
		t.Error("New accepted a relative folder")
	}
}

// TestSkipRules builds a folder holding one file for every skip rule, runs
// Scan, and checks what got indexed and why the rest didn't.
func TestSkipRules(t *testing.T) {
	root := tempRoot(t)
	outside := tempRoot(t)
	notes := filepath.Join(root, "notes")

	files := map[string]string{
		// Kept.
		"a.md":             "# A\n\nkept",
		"sub/b.txt":        "kept",
		"code/main.py":     "print('kept')",
		"sub/drafts/d.md":  "kept: /drafts is anchored to the top folder",
		"kept-by-neg.md":   "the root .gitignore re-includes this",
		"sub/renamed.md":   "sub/.gitignore re-includes this",
		"sub/meru-back.md": "sub/.meruignore re-includes what sub/.gitignore drops",

		// Hidden.
		".hidden.md":      "x",
		".git/config.md":  "x",
		"sub/.obsidian/w": "x",

		// Build folders.
		"node_modules/x.js": "x", ".venv/x.py": "x", "venv/x.py": "x",
		"vendor/x.go": "x", "target/x.rs": "x", "dist/x.js": "x",
		"build/x.md": "x", "__pycache__/x.py": "x",

		// Secrets.
		".env": "K=V", ".env.local": "K=V", "server.pem": "x", "tls.key": "x",
		"cert.p12": "x", "cert.pfx": "x", "id_rsa": "x", "id_rsa.pub": "x",
		"id_ed25519": "x", "ID_ECDSA": "x", "vault.kdbx": "x",
		"secrets.yaml": "x", "credentials.json": "x", ".netrc": "x",
		".npmrc": "x", ".pypirc": "x",

		// Media and binaries by extension.
		"photo.png": "x", "song.mp3": "x", "movie.mp4": "x", "backup.zip": "x",
		"tool.exe": "x", "data.sqlite": "x",

		// Binary by content, too large, unsupported.
		"nul.txt":     "text\x00more",
		"big.txt":     strings.Repeat("word ", 300_000), // 1.5 MB, over the 1 MB limit below
		"unknown.xyz": "x",

		// Ignore files.
		".gitignore":          "*.log\nignored-*.md\n!kept-by-neg.md\n/drafts/\n**/tmp/**\nrenamed.md\n",
		"app.log":             "x",
		"ignored-one.md":      "x",
		"kept-by-neg.md.bak":  "x", // unsupported, not ignored
		"drafts/d.md":         "x",
		"a/b/tmp/deep.md":     "x",
		"sub/.gitignore":      "!renamed.md\nmeru-back.md\n",
		"sub/.meruignore":     "!meru-back.md\nprivate.md\n",
		"sub/private.md":      "x",
		"sub/deeper/other.md": "kept: sub/.meruignore names private.md only",
		".meruignore":         "secret-plan.md\n",
		"secret-plan.md":      "x",
		"sub/secret-plan.md":  "x",
		"x.draft.md":          "x",
		"sub/x2.draft.md":     "x",
		"sub/.gitignore2":     "x",
	}
	writeFiles(t, notes, files)
	// A nested .gitignore can't re-include what the config ignores.
	writeFiles(t, notes, map[string]string{"sub/.gitignore": "!renamed.md\nmeru-back.md\n!x2.draft.md\n"})

	// Symlinks: to a file outside, to a folder outside, and to a file inside.
	writeFiles(t, outside, map[string]string{"leak.md": "outside", "dir/leak2.md": "outside"})
	links := map[string]string{
		"link-out.md": filepath.Join(outside, "leak.md"),
		"link-dir":    filepath.Join(outside, "dir"),
		"link-in.md":  filepath.Join(notes, "a.md"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(notes, name)); err != nil {
			t.Skipf("can't make symlinks here: %v", err)
		}
	}

	cfg := testConfig(notes)
	cfg.MaxFileMB = 1
	cfg.Ignore = []string{"*.draft.md"}
	ix, sink, _ := newTestIndexer(t, cfg)
	rep, err := ix.Scan(t.Context())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := []string{
		"a.md", "code/main.py", "kept-by-neg.md", "sub/b.txt",
		"sub/deeper/other.md", "sub/drafts/d.md", "sub/meru-back.md", "sub/renamed.md",
	}
	if got := sink.paths(notes); !reflect.DeepEqual(got, want) {
		t.Errorf("indexed:\n got %v\nwant %v", got, want)
	}

	wantSkipped := map[string]int{
		// .hidden.md, .git, sub/.obsidian, .venv (hidden before it's a build
		// folder), and the five ignore files and look-alikes.
		ReasonHidden:      9,
		ReasonBuildFolder: 7,
		ReasonSecret:      16,
		ReasonMedia:       4,
		ReasonBinary:      3, // tool.exe, data.sqlite, nul.txt
		ReasonTooLarge:    1,
		ReasonUnsupported: 2, // unknown.xyz, kept-by-neg.md.bak
		ReasonSymlink:     3,
		// app.log, ignored-one.md, drafts/, a/b/tmp/deep.md, sub/private.md,
		// secret-plan.md twice, x.draft.md, sub/x2.draft.md.
		ReasonIgnored: 9,
	}
	if !reflect.DeepEqual(rep.Skipped, wantSkipped) {
		t.Errorf("skipped:\n got %v\nwant %v", rep.Skipped, wantSkipped)
	}
	if rep.Indexed != len(want) {
		t.Errorf("Indexed = %d, want %d", rep.Indexed, len(want))
	}
}

// TestSkipPathChecksParents covers the path-at-a-time check the watcher
// uses: a file is skipped when any folder above it is.
func TestSkipPathChecksParents(t *testing.T) {
	root := tempRoot(t)
	writeFiles(t, root, map[string]string{
		"node_modules/pkg/readme.md": "x",
		".git/notes.md":              "x",
		"ok/fine.md":                 "x",
		"ignored/x.md":               "x",
		".gitignore":                 "ignored/\n",
	})
	ix, _, _ := newTestIndexer(t, testConfig(root))
	tests := []struct {
		rel  string
		want string
	}{
		{"node_modules/pkg/readme.md", ReasonBuildFolder},
		{".git/notes.md", ReasonHidden},
		{"ignored/x.md", ReasonIgnored},
		{"ok/fine.md", ""},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			p := filepath.Join(root, filepath.FromSlash(tt.rel))
			info, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := ix.skipPath(root, p, info.Mode()); got != tt.want {
				t.Errorf("skipPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLooksBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"text", []byte("hello\nworld"), false},
		{"utf-8", []byte("héllo 日本"), false},
		{"nul early", []byte("a\x00b"), true},
		{"nul past 8 KB", append([]byte(strings.Repeat("a", binarySniffBytes)), 0), false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksBinary(tt.data); got != tt.want {
				t.Errorf("looksBinary = %v, want %v", got, tt.want)
			}
		})
	}
}
