// This file tests the write_file tool: what it writes, where, with which
// mode, and every path and size it must refuse.

package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/dispatch"
)

// writeTools returns built-in tools whose output folder is a path that
// doesn't exist yet inside a temp directory, and that path.
func writeTools(t *testing.T) (*Tools, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "meru-output")
	return New(filepath.Join(t.TempDir(), "config.toml"), config.Builtin{Tools: config.BuiltinTools(), Confirm: []string{WriteFile}}, config.Web{}, nil, out, nil, nil, nil), out
}

// callWrite calls write_file with args and returns the result.
func callWrite(t *testing.T, tools *Tools, args string) dispatch.Result {
	t.Helper()
	res, err := tools.Call(context.Background(), WriteFile, json.RawMessage(args))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return res
}

func TestWriteFile(t *testing.T) {
	tools, out := writeTools(t)

	res := callWrite(t, tools, `{"path":"drafts/landlord.md","content":"Dear landlord,"}`)
	want := filepath.Join(out, "drafts", "landlord.md")
	if res.IsError || !strings.Contains(res.Text, want) {
		t.Fatalf("Result = %+v, want success naming %s", res, want)
	}
	got, err := os.ReadFile(want) // #nosec G304 -- a test temp dir
	if err != nil || string(got) != "Dear landlord," {
		t.Fatalf("file holds %q, %v", got, err)
	}
	checkMode(t, want, 0o600)
	checkMode(t, out, 0o700)
	checkMode(t, filepath.Join(out, "drafts"), 0o700)

	// A second write to the same path needs overwrite.
	res = callWrite(t, tools, `{"path":"drafts/landlord.md","content":"v2"}`)
	if !res.IsError || !strings.Contains(res.Text, "already exists") {
		t.Errorf("Result = %+v, want a refusal to overwrite", res)
	}
	res = callWrite(t, tools, `{"path":"drafts/landlord.md","content":"v2","overwrite":true}`)
	if res.IsError {
		t.Fatalf("overwrite: %+v", res)
	}
	if got, _ := os.ReadFile(want); string(got) != "v2" { // #nosec G304 -- a test temp dir
		t.Errorf("after overwrite the file holds %q", got)
	}

	// No temporary files stay behind.
	entries, err := os.ReadDir(filepath.Join(out, "drafts"))
	if err != nil || len(entries) != 1 {
		t.Errorf("drafts holds %d entries (%v), want 1", len(entries), err)
	}
}

// TestWriteFileNamesTheOutputFolder checks the three ways a model names a
// file it read in the output folder: "~/meru-output/x", the absolute path,
// and "meru-output/x". Each must write x itself, never a meru-output folder
// inside the output folder, and the result must say whether it replaced the
// file and give a link the user can click.
func TestWriteFileNamesTheOutputFolder(t *testing.T) {
	tools, out := writeTools(t)
	// os.UserHomeDir reads HOME, or USERPROFILE on Windows, so "~" is the
	// folder above the output folder.
	t.Setenv("HOME", filepath.Dir(out))
	t.Setenv("USERPROFILE", filepath.Dir(out))
	want := filepath.Join(out, "uploads", "page.html")
	paths := []string{
		"uploads/page.html",
		"meru-output/uploads/page.html",
		"~/meru-output/uploads/page.html",
		filepath.ToSlash(want),
	}
	for i, p := range paths {
		res := callWrite(t, tools, `{"path":"`+p+`","content":"v`+string(rune('1'+i))+`","overwrite":true}`)
		if res.IsError {
			t.Fatalf("%s: %s", p, res.Text)
		}
		verb := "Replaced"
		if i == 0 {
			verb = "Wrote a new file"
		}
		if !strings.HasPrefix(res.Text, verb) || !strings.Contains(res.Text, want) {
			t.Errorf("%s: result = %q, want %q and the path", p, res.Text, verb)
		}
		if !strings.Contains(res.Text, "Link for the user: [page.html](file://") {
			t.Errorf("%s: result = %q, want a file link", p, res.Text)
		}
	}
	if got, _ := os.ReadFile(want); string(got) != "v4" { // #nosec G304 -- a test temp dir
		t.Errorf("the file holds %q, want the last write", got)
	}
	if _, err := os.Stat(filepath.Join(out, "meru-output")); err == nil {
		t.Error("write_file made a meru-output folder inside the output folder")
	}
}

func TestFileURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	if got, want := FileURL("/Users/me/meru-output/a b.html"), "file:///Users/me/meru-output/a%20b.html"; got != want {
		t.Errorf("FileURL = %q, want %q", got, want)
	}
}

func TestWriteFileRefuses(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string // a piece of the refusal
	}{
		{"absolute", `{"path":"/etc/passwd","content":"x"}`, "absolute"},
		{"backslash absolute", `{"path":"\\x\\y","content":"x"}`, "absolute"},
		{"drive letter", `{"path":"C:\\x.md","content":"x"}`, "absolute"},
		{"dot dot", `{"path":"../escape.md","content":"x"}`, `".."`},
		{"dot dot inside", `{"path":"a/../../escape.md","content":"x"}`, `".."`},
		{"dot dot backslash", `{"path":"a\\..\\..\\escape.md","content":"x"}`, `".."`},
		{"empty", `{"path":"","content":"x"}`, "empty"},
		{"folder itself", `{"path":"./","content":"x"}`, "names no file"},
		{"too big", `{"path":"big.txt","content":"` + strings.Repeat("a", maxWriteBytes+1) + `"}`, "1 MiB"},
		{"unknown key", `{"path":"a.md","content":"x","mode":"0777"}`, "valid JSON"},
		{"not an object", `"a.md"`, "valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, out := writeTools(t)
			res := callWrite(t, tools, tt.args)
			if !res.IsError || !strings.Contains(res.Text, tt.want) {
				t.Errorf("Result = %.200q, want a refusal containing %q", res.Text, tt.want)
			}
			if !strings.Contains(res.Text, "Nothing was written") {
				t.Errorf("Result = %.200q, want it to say nothing was written", res.Text)
			}
			// The folder may exist, but nothing may be in it.
			if entries, _ := os.ReadDir(out); len(entries) != 0 {
				t.Errorf("output folder holds %d entries after a refusal", len(entries))
			}
		})
	}
}

// TestWriteFileSymlinks checks that write_file won't write through a link,
// whether the link is the file or a folder on the way to it.
func TestWriteFileSymlinks(t *testing.T) {
	tools, out := writeTools(t)
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(out, "link.txt")); err != nil {
		t.Skipf("can't make symbolic links here: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(out, "away")); err != nil {
		t.Fatal(err)
	}

	for _, args := range []string{
		`{"path":"link.txt","content":"x","overwrite":true}`,
		`{"path":"away/new.txt","content":"x"}`,
		`{"path":"away/victim.txt","content":"x","overwrite":true}`,
	} {
		res := callWrite(t, tools, args)
		if !res.IsError || !strings.Contains(res.Text, "symbolic link") {
			t.Errorf("%s: Result = %q, want a refusal naming the link", args, res.Text)
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" { // #nosec G304 -- a test temp dir
		t.Errorf("the file outside holds %q; write_file wrote through a link", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("write_file made a file outside the output folder")
	}
}

// TestWriteFileBackend checks how write_file shows up to dispatch and
// `meru tools`, and that it stays out when merud gives no output folder.
func TestWriteFileBackend(t *testing.T) {
	tools, out := writeTools(t)
	if got := tools.Confirm(WriteFile); got != dispatch.ConfirmAsk {
		t.Errorf("Confirm(write_file) = %v, want ask", got)
	}
	var found bool
	for _, s := range tools.Tools() {
		if s.Name == WriteFile {
			found = true
			if !strings.Contains(s.Description, out) {
				t.Errorf("description %q doesn't name the folder", s.Description)
			}
		}
	}
	if !found {
		t.Error("Tools doesn't offer write_file")
	}
	st := tools.Status()[0].Tools
	if len(st) != 5 || st[2].Name != WriteFile || !st[2].Confirm || st[3].Name != EditFile {
		t.Errorf("Status tools = %+v, want configure, datetime, write_file, which asks, edit_file and web_fetch", st)
	}

	none := New("config.toml", config.Builtin{Tools: config.BuiltinTools()}, config.Web{}, nil, "", nil, nil, nil)
	for _, s := range none.Tools() {
		if s.Name == WriteFile {
			t.Error("write_file offered with no output folder")
		}
	}
	if _, err := none.Call(context.Background(), WriteFile, json.RawMessage(`{}`)); err == nil {
		t.Error("write_file ran with no output folder")
	}
}

// checkMode fails the test when path's permission bits aren't want. Windows
// has no Unix permission bits, so the check is skipped there.
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

// TestWritten checks that Written reads back the name and link of the file
// a write_file result names, and nothing from a refusal.
func TestWritten(t *testing.T) {
	tools, out := writeTools(t)
	res := callWrite(t, tools, `{"path":"r e.html","content":"x"}`)
	name, link, ok := Written(res.Text)
	if !ok || name != "r e.html" || link != FileURL(filepath.Join(out, "r e.html")) {
		t.Errorf("Written(%q) = %q, %q, %v", res.Text, name, link, ok)
	}
	if _, _, ok := Written("write_file: r.html already exists. Nothing was written"); ok {
		t.Error("Written read a link from a refusal")
	}
}
