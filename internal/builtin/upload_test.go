// This file tests Upload, the copy behind the desktop app's attach button
// and file drops: where the copy lands and under what name, each refusal,
// and that read_file then reads the copy.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/rpc"
)

// uploadTools returns built-in tools over one [index] folder, notes, with
// out as the output folder, and a folder outside both where the test puts
// the files the user picks. folders false leaves [index] folders empty.
func uploadTools(t *testing.T, folders bool, cfg config.Builtin) (tools *Tools, out, away string) {
	t.Helper()
	base := t.TempDir()
	notes := filepath.Join(base, "notes")
	out = filepath.Join(base, "meru-output")
	away = filepath.Join(base, "Downloads")
	for _, d := range []string{notes, away} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ic := config.Index{MaxFileMB: 1}
	if folders {
		ic.Folders = []string{notes}
	}
	ix, err := index.New(ic, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(base, "config.toml"), cfg, config.Web{}, nil, out, ix, nil, nil), out, away
}

// writeFile writes body to p, failing the test on an error.
func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUpload(t *testing.T) {
	cfg := config.Builtin{Tools: config.BuiltinTools()}
	tools, out, away := uploadTools(t, true, cfg)
	uploads := filepath.Join(out, uploadsFolder)

	writeFile(t, filepath.Join(away, "garden-plan.md"), "Plant tomatoes in May.\n")
	writeFile(t, filepath.Join(away, "garden plan (2).txt"), "Beans in June.\n")
	writeFile(t, filepath.Join(away, ".env"), "TOKEN=x\n")
	writeFile(t, filepath.Join(away, "server.pem"), "-----BEGIN-----\n")
	writeFile(t, filepath.Join(away, "id_ed25519"), "key\n")
	writeFile(t, filepath.Join(away, "photos.zip"), "PK")
	writeFile(t, filepath.Join(away, "tool.txt"), "a\x00b")
	writeFile(t, filepath.Join(away, "big.md"), strings.Repeat("a", 2<<20))
	// Truncate grows the empty file into a sparse one: it reports its
	// size without taking the disk space, so the test stays fast.
	writeFile(t, filepath.Join(away, "huge.md"), "")
	if err := os.Truncate(filepath.Join(away, "huge.md"), uploadCap+1); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(away, "link.md")
	if err := os.Symlink(filepath.Join(away, "garden-plan.md"), link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	tests := []struct {
		name    string
		src     string
		want    string // the copy's name in uploads; "" when Upload must refuse
		refusal string // a piece of the refusal
	}{
		{name: "a file anywhere", src: filepath.Join(away, "garden-plan.md"), want: "garden-plan.md"},
		{name: "the same name again", src: filepath.Join(away, "garden-plan.md"), want: "garden-plan-2.md"},
		{name: "spaces become underscores", src: filepath.Join(away, "garden plan (2).txt"), want: "garden_plan__2_.txt"},
		{name: "missing", src: filepath.Join(away, "gone.md"), refusal: "isn't there any more"},
		{name: "relative path", src: "garden-plan.md", refusal: "isn't a full path"},
		{name: "folder", src: away, refusal: "is a folder"},
		{name: "symlink", src: link, refusal: "symbolic link"},
		{name: "env file", src: filepath.Join(away, ".env"), refusal: "keys, passwords or tokens"},
		{name: "certificate", src: filepath.Join(away, "server.pem"), refusal: "keys, passwords or tokens"},
		{name: "ssh key", src: filepath.Join(away, "id_ed25519"), refusal: "keys, passwords or tokens"},
		{name: "over the cap", src: filepath.Join(away, "huge.md"), refusal: "Meru takes files up to 50.0 MB"},
		{name: "media", src: filepath.Join(away, "photos.zip"), refusal: "media file"},
		{name: "binary", src: filepath.Join(away, "tool.txt"), refusal: "binary file"},
		{name: "over max_file_mb", src: filepath.Join(away, "big.md"), refusal: "max_file_mb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tools.Upload(tt.src)
			if tt.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tt.refusal) {
					t.Fatalf("Upload(%s) = %+v, %v; want a refusal with %q", tt.src, got, err, tt.refusal)
				}
				return
			}
			if err != nil || got.Path != filepath.Join(uploads, tt.want) || got.Kind != rpc.AttachFile {
				t.Fatalf("Upload(%s) = %+v, %v; want %s, a file", tt.src, got, err, filepath.Join(uploads, tt.want))
			}
			src, _ := os.ReadFile(tt.src)
			if copied, err := os.ReadFile(got.Path); err != nil || string(copied) != string(src) {
				t.Errorf("the copy holds %q, %v", copied, err)
			}
			if info, err := os.Stat(got.Path); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("the copy's mode = %v, %v; want 0600", info.Mode(), err)
			}
		})
	}

	// A refused file leaves nothing behind: only the three copies remain.
	entries, err := os.ReadDir(uploads)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("uploads holds %v; want the three copies alone", names)
	}

	// read_file reads the copy by the path the question names.
	res := callTool(t, tools, context.Background(), ReadFile, `{"path":`+jsonPath(filepath.Join(uploads, "garden-plan.md"))+`}`)
	if res.IsError || !strings.Contains(res.Text, "Plant tomatoes in May.") {
		t.Errorf("read_file on the copy = %+v", res)
	}
}

// TestUploadNeedsReadFile checks that Upload refuses when the model
// couldn't read the copy anyway: no [index] folders, read_file taken out
// of [builtin] tools, or no output folder.
func TestUploadNeedsReadFile(t *testing.T) {
	var noRead []string
	for _, name := range config.BuiltinTools() {
		if name != ReadFile {
			noRead = append(noRead, name)
		}
	}
	tests := []struct {
		name    string
		folders bool
		tools   []string
		noOut   bool
		refusal string
	}{
		{name: "no folders", tools: config.BuiltinTools(), refusal: "add a folder in Settings"},
		{name: "read_file off", folders: true, tools: noRead, refusal: "turn it on in Settings"},
		{name: "no output folder", folders: true, tools: config.BuiltinTools(), noOut: true, refusal: "no output folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, _, away := uploadTools(t, tt.folders, config.Builtin{Tools: tt.tools})
			if tt.noOut {
				tools.outputDir = ""
			}
			src := filepath.Join(away, "garden-plan.md")
			writeFile(t, src, "Plant tomatoes in May.\n")
			if got, err := tools.Upload(src); err == nil || !strings.Contains(err.Error(), tt.refusal) {
				t.Errorf("Upload = %+v, %v; want a refusal with %q", got, err, tt.refusal)
			}
		})
	}
}
