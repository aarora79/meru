// This file tests the edit_file tool: a replacement in one place or every
// place, the backup it keeps, the link it returns, and each refusal.

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/dispatch"
)

// callEdit calls edit_file with args and returns the result.
func callEdit(t *testing.T, tools *Tools, args string) dispatch.Result {
	t.Helper()
	res, err := tools.Call(t.Context(), EditFile, []byte(args))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return res
}

// pageFile writes a small page in the output folder of tools and returns
// its path. "Mom" is in it twice and "Dad" once.
func pageFile(t *testing.T, tools *Tools, out string) string {
	t.Helper()
	if res := callWrite(t, tools, `{"path":"reports/oct.html","content":"<p>Mom: 40</p><p>Dad: 12</p><p>Mom again</p>"}`); res.IsError {
		t.Fatalf("write: %s", res.Text)
	}
	return filepath.Join(out, "reports", "oct.html")
}

func TestEditFile(t *testing.T) {
	tools, out := writeTools(t)
	page := pageFile(t, tools, out)

	// Two matches need all.
	res := callEdit(t, tools, `{"path":"reports/oct.html","old":"Mom","new":"Mother"}`)
	if !res.IsError || !strings.Contains(res.Text, "2 times") || !strings.Contains(res.Text, "set all to true") {
		t.Errorf("an edit that matches twice = %+v, want a refusal that asks for all", res)
	}
	// With all, both change; the path may name the output folder, as
	// write_file's may.
	res = callEdit(t, tools, `{"path":"meru-output/reports/oct.html","old":"Mom","new":"Mother","all":true}`)
	if res.IsError || !strings.HasPrefix(res.Text, "Replaced 2 places in "+page) {
		t.Fatalf("all = %+v", res)
	}
	// One match needs no all. The backup holds the page before this edit.
	res = callEdit(t, tools, `{"path":"reports/oct.html","old":"Dad: 12","new":"Dad: 13"}`)
	if res.IsError || !strings.HasPrefix(res.Text, "Replaced 1 place in "+page) {
		t.Fatalf("one match = %+v", res)
	}
	if name, link, ok := Written(res.Text); !ok || name != "oct.html" || link != FileURL(page) {
		t.Errorf("Written = %q, %q, %v; want the page's link", name, link, ok)
	}
	got, _ := os.ReadFile(page) // #nosec G304 -- a test temp dir
	if want := "<p>Mother: 40</p><p>Dad: 13</p><p>Mother again</p>"; string(got) != want {
		t.Errorf("page = %q, want %q", got, want)
	}
	bak, _ := os.ReadFile(filepath.Join(out, "reports", ".oct.html.bak")) // #nosec G304 -- a test temp dir
	if want := "<p>Mother: 40</p><p>Dad: 12</p><p>Mother again</p>"; string(bak) != want {
		t.Errorf("backup = %q, want %q", bak, want)
	}
}

func TestEditFileRefuses(t *testing.T) {
	tools, out := writeTools(t)
	page := pageFile(t, tools, out)
	before, _ := os.ReadFile(page) // #nosec G304 -- a test temp dir
	tests := []struct {
		name, args, want string
	}{
		{"text not there", `{"path":"reports/oct.html","old":"Sam","new":"Samir"}`, "isn't in"},
		{"two matches without all", `{"path":"reports/oct.html","old":"Mom","new":"Mother"}`, "set all to true"},
		{"empty old", `{"path":"reports/oct.html","old":"","new":"x"}`, "old is empty"},
		{"no such file", `{"path":"reports/nov.html","old":"a","new":"b"}`, "write_file makes a new file"},
		{"absolute outside", `{"path":"/etc/hosts","old":"a","new":"b"}`, "absolute"},
		{"dot dot", `{"path":"../x.html","old":"a","new":"b"}`, `".."`},
		{"unknown key", `{"path":"reports/oct.html","old":"a","new":"b","mode":"x"}`, "valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callEdit(t, tools, tt.args)
			if !res.IsError || !strings.Contains(res.Text, tt.want) || !strings.Contains(res.Text, "Nothing changed") {
				t.Errorf("result = %.200q, want a refusal holding %q and \"Nothing changed\"", res.Text, tt.want)
			}
		})
	}
	if after, _ := os.ReadFile(page); string(after) != string(before) { // #nosec G304 -- a test temp dir
		t.Errorf("a refused edit changed the page to %q", after)
	}
}

func TestEditFileSymlink(t *testing.T) {
	tools, out := writeTools(t)
	pageFile(t, tools, out)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("Mom"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(out, "link.txt")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	res := callEdit(t, tools, `{"path":"link.txt","old":"Mom","new":"Mother"}`)
	if !res.IsError || !strings.Contains(res.Text, "symbolic link") {
		t.Errorf("result = %+v, want a refusal", res)
	}
	if got, _ := os.ReadFile(outside); string(got) != "Mom" { // #nosec G304 -- a test temp dir
		t.Errorf("the file the link points at holds %q", got)
	}
}
