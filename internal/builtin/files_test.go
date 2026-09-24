// This file tests the read-only file tools, read_file, list_folder and
// grep, over a temp folder tree that holds one of everything the indexer
// skips: a secret, a hidden folder, a build folder, a .meruignore, a
// symlink out of the folder, a big file, binaries, and a PDF.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/index"
)

// fileTree holds the paths of the test tree.
type fileTree struct {
	root    string // the first [index] folder
	second  string // the second [index] folder
	outside string // a folder no [index] folder holds
}

// newFileTree builds two [index] folders and an outside folder in a temp
// directory, and returns built-in tools that read them.
func newFileTree(t *testing.T) (*Tools, fileTree) {
	t.Helper()
	base := t.TempDir()
	tr := fileTree{
		root:    filepath.Join(base, "notes"),
		second:  filepath.Join(base, "work"),
		outside: filepath.Join(base, "outside"),
	}
	pdf, err := os.ReadFile(filepath.Join("testdata", "two-pages.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"notes/garden.md":                  "# Garden\n\nPlant tomatoes in May.\nThe Orchard needs pruning.\n",
		"notes/.env":                       "API_KEY=abc\n",
		"notes/.hidden/diary.md":           "orchard secrets\n",
		"notes/node_modules/pkg/readme.md": "orchard package\n",
		"notes/.meruignore":                "drafts/\n",
		"notes/drafts/plan.md":             "orchard draft\n",
		"notes/private/tax.md":             "orchard tax\n",
		"notes/nul.txt":                    "orchard\x00binary",
		"notes/tool.exe":                   "MZ",
		"notes/photo.png":                  "png",
		"notes/harvest.pdf":                string(pdf),
		"notes/trips/japan.md":             "Kyoto in April.\n",
		"notes/trips/2026/lisbon.md":       "Lisbon orchard tour.\n",
		"notes/trips/2026/may/porto.md":    "Porto.\n",
		"notes/page.html":                  "<html><head><title>x</title><script>orchard()</script></head><body><h1>Fruit</h1><p>Apples &amp; pears.</p></body></html>",
		"work/garden.md":                   "Work garden.\n",
		"work/only-here.md":                "Only in work.\n",
		"outside/stolen.md":                "orchard outside\n",
	}
	for rel, body := range files {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A file over the 1 MB cap, and a long one under it for paging.
	if err := os.WriteFile(filepath.Join(tr.root, "big.txt"), []byte(strings.Repeat("a", 1<<20+10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tr.root, "long.md"), []byte(longText()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlinks out of the folder: one to a file, one to a folder.
	if err := os.Symlink(filepath.Join(tr.outside, "stolen.md"), filepath.Join(tr.root, "link.md")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if err := os.Symlink(tr.outside, filepath.Join(tr.root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	ix, err := index.New(config.Index{
		Folders:   []string{tr.root, tr.second},
		Ignore:    []string{"private/"},
		MaxFileMB: 1,
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(base, "config.toml"), config.Builtin{}, nil, "", ix, nil, nil), tr
}

// longText returns 30,000 characters: "line NNNNN" lines, with a
// multi-byte character in each so offsets must count characters.
func longText() string {
	var b strings.Builder
	for i := 0; b.Len() < 40000; i++ {
		fmt.Fprintf(&b, "line %05d é\n", i)
	}
	return string([]rune(b.String())[:30000])
}

// callTool calls a built-in with args and returns the result.
func callTool(t *testing.T, tools *Tools, ctx context.Context, name, args string) dispatch.Result {
	t.Helper()
	res, err := tools.Call(ctx, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("Call %s: %v", name, err)
	}
	return res
}

// jsonPath quotes p for a JSON argument.
func jsonPath(p string) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func TestFileToolsOfferedAndNeverAsk(t *testing.T) {
	tools, _ := newFileTree(t)
	var names []string
	for _, s := range tools.Tools() {
		names = append(names, s.Name)
		if IsFileTool(s.Name) && !json.Valid(s.Parameters) {
			t.Errorf("%s schema isn't valid JSON", s.Name)
		}
	}
	for _, want := range []string{ReadFile, ListFolder, Grep} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Errorf("Tools() = %v, want %s", names, want)
		}
		if got := tools.Confirm(want); got != dispatch.ConfirmNever {
			t.Errorf("Confirm(%s) = %v, want ConfirmNever", want, got)
		}
	}

	asks := New("config.toml", config.Builtin{Confirm: []string{Grep}}, nil, "", tools.files, nil, nil)
	if got := asks.Confirm(Grep); got != dispatch.ConfirmAsk {
		t.Errorf("Confirm(grep) with [builtin] confirm = %v, want ConfirmAsk", got)
	}

	none := New("config.toml", config.Builtin{}, nil, "", nil, nil, nil)
	for _, s := range none.Tools() {
		if IsFileTool(s.Name) {
			t.Errorf("Tools() with no indexer offers %s", s.Name)
		}
	}
	if _, err := none.Call(context.Background(), ReadFile, json.RawMessage(`{"path":"x"}`)); err == nil {
		t.Error("read_file with no indexer ran; want an unknown-tool error")
	}
}

func TestFileToolsRefuse(t *testing.T) {
	tools, tr := newFileTree(t)
	in := func(parts ...string) string { return jsonPath(filepath.Join(append([]string{tr.root}, parts...)...)) }
	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"secret", ReadFile, `{"path":` + in(".env") + `}`, "secret file"},
		{"hidden folder", ReadFile, `{"path":` + in(".hidden", "diary.md") + `}`, "hidden"},
		{"build folder", ReadFile, `{"path":` + in("node_modules", "pkg", "readme.md") + `}`, "build or dependency folder"},
		{"meruignore", ReadFile, `{"path":` + in("drafts", "plan.md") + `}`, "ignored by"},
		{"config ignore", ListFolder, `{"path":` + in("private") + `}`, "ignored by"},
		{"symlink file", ReadFile, `{"path":` + in("link.md") + `}`, "symbolic link"},
		{"through symlink folder", ReadFile, `{"path":` + in("linkdir", "stolen.md") + `}`, "symbolic link"},
		{"too large", ReadFile, `{"path":` + in("big.txt") + `}`, "max_file_mb"},
		{"nul byte", ReadFile, `{"path":` + in("nul.txt") + `}`, "binary file"},
		{"binary extension", ReadFile, `{"path":` + in("tool.exe") + `}`, "binary file"},
		{"media", ReadFile, `{"path":` + in("photo.png") + `}`, "media file"},
		{"outside", ReadFile, `{"path":` + jsonPath(filepath.Join(tr.outside, "stolen.md")) + `}`, "outside the indexed folders"},
		{"dot-dot", ReadFile, `{"path":` + jsonPath(tr.root+"/../outside/stolen.md") + `}`, "outside the indexed folders"},
		{"relative dot-dot", ReadFile, `{"path":"../outside/stolen.md"}`, "outside the indexed folders"},
		{"missing", ReadFile, `{"path":` + in("nope.md") + `}`, "doesn't exist"},
		{"relative nowhere", ReadFile, `{"path":"nope.md"}`, "no indexed folder holds"},
		{"relative in two folders", ReadFile, `{"path":"garden.md"}`, "more than one indexed folder"},
		{"read a folder", ReadFile, `{"path":` + in("trips") + `}`, "is a folder"},
		{"list a file", ListFolder, `{"path":` + in("garden.md") + `}`, "is a file"},
		{"empty path", ReadFile, `{"path":""}`, "pass path"},
		{"negative offset", ReadFile, `{"path":` + in("garden.md") + `,"offset":-1}`, "negative"},
		{"offset past end", ReadFile, `{"path":` + in("garden.md") + `,"offset":999}`, "past the end"},
		{"unknown key", ReadFile, `{"path":` + in("garden.md") + `,"lines":3}`, "valid JSON object"},
		{"depth too big", ListFolder, `{"path":` + in() + `,"depth":4}`, "out of range"},
		{"empty pattern", Grep, `{"pattern":""}`, "pass pattern"},
		{"bad regex", Grep, `{"pattern":"a(b","regex":true}`, "isn't a valid regular expression"},
		{"too many results", Grep, `{"pattern":"a","max_results":201}`, "out of range"},
		{"grep a secret", Grep, `{"pattern":"KEY","path":` + in(".env") + `}`, "secret file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tools, context.Background(), tt.tool, tt.args)
			if !res.IsError || !strings.Contains(res.Text, tt.want) {
				t.Errorf("%s %s = %+v, want an error holding %q", tt.tool, tt.args, res, tt.want)
			}
		})
	}
	// The outside refusal names the indexed folders.
	res := callTool(t, tools, context.Background(), ReadFile, `{"path":`+jsonPath(filepath.Join(tr.outside, "stolen.md"))+`}`)
	if !strings.Contains(res.Text, "notes") || !strings.Contains(res.Text, "work") {
		t.Errorf("outside refusal = %q, want it to name both indexed folders", res.Text)
	}
}

func TestReadFile(t *testing.T) {
	tools, tr := newFileTree(t)
	tests := []struct {
		name    string
		args    string
		want    []string
		notWant []string
	}{
		{"markdown as is", `{"path":` + jsonPath(filepath.Join(tr.root, "garden.md")) + `}`,
			[]string{"garden.md\n", "bytes, modified ", "Characters 0 to 60 of 60.", "# Garden\n\nPlant tomatoes in May."},
			[]string{"more characters"}},
		{"relative to the one folder that holds it", `{"path":"only-here.md"}`,
			[]string{"only-here.md", "Only in work."}, nil},
		{"relative in a subfolder", `{"path":"trips/japan.md"}`,
			[]string{"Kyoto in April."}, nil},
		{"html as text", `{"path":` + jsonPath(filepath.Join(tr.root, "page.html")) + `}`,
			[]string{"Fruit", "Apples & pears."}, []string{"<p>", "orchard()"}},
		{"pdf page by page", `{"path":` + jsonPath(filepath.Join(tr.root, "harvest.pdf")) + `}`,
			[]string{"--- page 1 ---\nHarvest report for the orchard", "--- page 2 ---\nThe pear crop doubled"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tools, context.Background(), ReadFile, tt.args)
			if res.IsError {
				t.Fatalf("read_file: %s", res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result lacks %q:\n%s", w, res.Text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(res.Text, w) {
					t.Errorf("result holds %q:\n%s", w, res.Text)
				}
			}
		})
	}
}

func TestReadFilePages(t *testing.T) {
	tools, tr := newFileTree(t)
	full := []rune(longText())
	path := jsonPath(filepath.Join(tr.root, "long.md"))
	var got []rune
	offset := 0
	for calls := 0; ; calls++ {
		if calls > 5 {
			t.Fatal("read_file never reached the end")
		}
		res := callTool(t, tools, context.Background(), ReadFile, fmt.Sprintf(`{"path":%s,"offset":%d}`, path, offset))
		if res.IsError {
			t.Fatalf("read_file: %s", res.Text)
		}
		// The text sits between the header's blank line and the note.
		body := res.Text[strings.Index(res.Text, "\n\n")+2:]
		next := -1
		if i := strings.Index(body, "\n\n["); i >= 0 {
			if _, err := fmt.Sscanf(body[i:], "\n\n[%d more characters. To read on, call read_file with offset %d.]", new(int), &next); err != nil {
				t.Fatalf("closing note: %v in %q", err, body[i:])
			}
			body = body[:i]
		}
		if n := len([]rune(body)); n > maxReadChars {
			t.Errorf("one call returned %d characters, over %d", n, maxReadChars)
		}
		got = append(got, []rune(body)...)
		if next < 0 {
			break
		}
		offset = next
	}
	if string(got) != string(full) {
		t.Errorf("pages joined = %d characters, want the file's %d", len(got), len(full))
	}
	if offset != 24000 {
		t.Errorf("last offset = %d, want 24000", offset)
	}
}

func TestListFolder(t *testing.T) {
	tools, tr := newFileTree(t)
	tests := []struct {
		name    string
		args    string
		want    []string
		notWant []string
	}{
		{"no path lists the indexed folders", `{}`,
			[]string{"The indexed folders", "notes/\n", "work/\n"}, nil},
		{"depth 1", `{"path":` + jsonPath(tr.root) + `}`,
			[]string{"(depth 1): 1 folder, 4 files.", "trips/\n", "garden.md  60 bytes  ", "harvest.pdf", "long.md", "page.html",
				"Left out 12 that Meru doesn't read: ",
				"binary file (2), hidden file or folder (2), ignored by .gitignore, .meruignore or [index] ignore (2), symbolic link (2), " +
					"build or dependency folder (1), media file (1), secret file (1), larger than [index] max_file_mb (1)."},
			[]string{"japan.md", ".env", "node_modules", "drafts", "\nprivate", "link.md", "big.txt", "photo.png"}},
		{"depth 3", `{"path":` + jsonPath(tr.root) + `,"depth":3}`,
			[]string{"trips/\ntrips/2026/\ntrips/2026/may/\n", "trips/japan.md", "trips/2026/lisbon.md"},
			[]string{"porto.md"}},
		{"relative folder", `{"path":"trips","depth":2}`,
			[]string{"2026/\n", "japan.md", "2026/lisbon.md"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tools, context.Background(), ListFolder, tt.args)
			if res.IsError {
				t.Fatalf("list_folder: %s", res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result lacks %q:\n%s", w, res.Text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(res.Text, w) {
					t.Errorf("result holds %q:\n%s", w, res.Text)
				}
			}
		})
	}
}

func TestListFolderFoldersFirst(t *testing.T) {
	tools, tr := newFileTree(t)
	res := callTool(t, tools, context.Background(), ListFolder, `{"path":`+jsonPath(filepath.Join(tr.root, "trips"))+`,"depth":2}`)
	folder := strings.Index(res.Text, "2026/\n")
	file := strings.Index(res.Text, "japan.md")
	if folder < 0 || file < 0 || folder > file {
		t.Errorf("want the folder before the files:\n%s", res.Text)
	}
}

func TestListFolderEntryCap(t *testing.T) {
	tools, tr := newFileTree(t)
	many := filepath.Join(tr.root, "many")
	if err := os.Mkdir(many, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxEntries + 20 {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("n%03d.md", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res := callTool(t, tools, context.Background(), ListFolder, `{"path":`+jsonPath(many)+`}`)
	if res.IsError {
		t.Fatalf("list_folder: %s", res.Text)
	}
	if n := strings.Count(res.Text, ".md  "); n != maxEntries {
		t.Errorf("listed %d files, want %d", n, maxEntries)
	}
	if !strings.Contains(res.Text, "More entries exist") {
		t.Errorf("result lacks the note on more entries:\n%s", res.Text[len(res.Text)-300:])
	}
}

func TestGrep(t *testing.T) {
	tools, tr := newFileTree(t)
	tests := []struct {
		name    string
		args    string
		want    []string
		notWant []string
	}{
		{"substring ignores case, every folder", `{"pattern":"orchard"}`,
			[]string{"garden.md:4: The Orchard needs pruning.", "lisbon.md:1: Lisbon orchard tour.",
				"harvest.pdf (page 1): Harvest report for the orchard", "3 matching lines in 3 files"},
			// Nothing the indexer skips, nothing outside, no HTML script text.
			[]string{"diary", "node_modules", "drafts", "tax", "nul.txt", "stolen", "orchard()"}},
		{"case sensitive", `{"pattern":"Orchard","case_sensitive":true}`,
			[]string{"garden.md:4:", "1 matching line in 1 file"}, []string{"lisbon"}},
		{"plain text keeps regex characters literal", `{"pattern":"May."}`,
			[]string{"garden.md:3: Plant tomatoes in May."}, nil},
		{"regex", `{"pattern":"^(Kyoto|Porto)","regex":true}`,
			[]string{"japan.md:1: Kyoto in April.", "porto.md:1: Porto."}, []string{"garden.md"}},
		{"one folder", `{"pattern":"orchard","path":"trips"}`,
			[]string{"lisbon.md:1:", "1 matching line in 1 file"}, []string{"garden.md"}},
		{"one pdf", `{"pattern":"pear","path":` + jsonPath(filepath.Join(tr.root, "harvest.pdf")) + `}`,
			[]string{"harvest.pdf (page 2): The pear crop doubled"}, nil},
		{"max results", `{"pattern":"line","path":"long.md","max_results":3}`,
			[]string{"long.md:1: line 00000", "long.md:3: line 00002", "3 matching lines in 1 file", "Stopped early at max_results (3)"},
			[]string{"long.md:4:"}},
		{"no match", `{"pattern":"zebra"}`,
			[]string{"0 matching lines in 0 files"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, tools, context.Background(), Grep, tt.args)
			if res.IsError {
				t.Fatalf("grep: %s", res.Text)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Text, w) {
					t.Errorf("result lacks %q:\n%s", w, res.Text)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(res.Text, w) {
					t.Errorf("result holds %q:\n%s", w, res.Text)
				}
			}
		})
	}
}

func TestGrepCutsLongLines(t *testing.T) {
	tools, tr := newFileTree(t)
	if err := os.WriteFile(filepath.Join(tr.root, "wide.md"), []byte("needle "+strings.Repeat("x", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, tools, context.Background(), Grep, `{"pattern":"needle"}`)
	for _, line := range strings.Split(res.Text, "\n") {
		if !strings.Contains(line, "wide.md:1: ") {
			continue
		}
		text := line[strings.Index(line, ": ")+2:]
		if n := len([]rune(text)); n != maxLineChars || !strings.HasSuffix(text, "…") {
			t.Errorf("match text is %d characters, want %d ending in …", n, maxLineChars)
		}
		return
	}
	t.Errorf("no wide.md match in:\n%s", res.Text)
}

func TestGrepLimits(t *testing.T) {
	tools, tr := newFileTree(t)
	c, err := tools.files.Check(tr.root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  grepRun
		want string
	}{
		{"file limit", grepRun{max: 50, timeLimit: time.Minute, fileLimit: 2}, "the limit of 2 files"},
		{"time limit", grepRun{max: 50, timeLimit: -1, fileLimit: 100}, "the time limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.run
			s.t = tools
			s.re = mustCompile(t, "(?i)orchard")
			if err := s.search(context.Background(), []index.Checked{c}); err != nil {
				t.Fatalf("search: %v", err)
			}
			if !strings.Contains(s.stop, tt.want) {
				t.Errorf("stop = %q, want %q", s.stop, tt.want)
			}
			if out := s.report("orchard", []index.Checked{c}); !strings.Contains(out, "Stopped early at "+s.stop) {
				t.Errorf("report doesn't say what stopped it:\n%s", out)
			}
		})
	}
}

func TestGrepCancelled(t *testing.T) {
	tools, _ := newFileTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := callTool(t, tools, ctx, Grep, `{"pattern":"orchard"}`)
	if !res.IsError || !strings.Contains(res.Text, "context canceled") {
		t.Errorf("grep after cancel = %+v, want a context canceled error", res)
	}
}

// mustCompile compiles expr or fails the test.
func mustCompile(t *testing.T, expr string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
