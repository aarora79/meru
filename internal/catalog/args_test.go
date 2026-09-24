// This file tests WithArgs, ExpandFolder and RunsOn: the folders on the
// command line and the system an entry needs.

package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWithArgsFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)        // os.UserHomeDir reads HOME on Unix
	t.Setenv("USERPROFILE", home) // and USERPROFILE on Windows
	notes := filepath.Join(home, "notes")
	if err := os.Mkdir(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	file := filepath.Join(other, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	fs, ok := Find("filesystem")
	if !ok {
		t.Fatal("no filesystem entry")
	}
	tests := []struct {
		name    string
		args    []string
		want    []string // folders added to the end of Args
		wantErr string
	}{
		{"home and absolute", []string{"~/notes", other}, []string{notes, other}, ""},
		{"home itself", []string{"~"}, []string{home}, ""},
		{"cleaned", []string{other + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(other)}, []string{other}, ""},
		{"none", nil, nil, "at least one folder"},
		{"missing", []string{"~/nope"}, nil, "doesn't exist"},
		{"a file", []string{file}, nil, "not a folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fs.WithArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := append(slices.Clone(fs.Args), tt.want...); !slices.Equal(got.Args, want) {
				t.Errorf("args = %q, want %q", got.Args, want)
			}
			for _, n := range got.Needs {
				if n.Kind == NeedFolders {
					t.Error("the folders need is still there")
				}
			}
			// The catalog's own entry is untouched.
			again, _ := Find("filesystem")
			if !slices.Equal(again.Args, fs.Args) {
				t.Errorf("Find returns changed args %q", again.Args)
			}
		})
	}
}

func TestWithArgsNone(t *testing.T) {
	fetch, _ := Find("fetch")
	if _, err := fetch.WithArgs([]string{"x"}); err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("error = %v", err)
	}
	got, err := fetch.WithArgs(nil)
	if err != nil || !slices.Equal(got.Args, fetch.Args) {
		t.Errorf("WithArgs(nil) = %v, %v", got.Args, err)
	}
}

func TestRunsOn(t *testing.T) {
	win, ok := Find("windows")
	if !ok {
		t.Fatal("no windows entry")
	}
	if win.RunsOn("darwin") || !win.RunsOn("windows") {
		t.Error("the windows entry should run on Windows alone")
	}
	fetch, _ := Find("fetch")
	if !fetch.RunsOn("linux") || !fetch.RunsOn("windows") {
		t.Error("fetch should run everywhere")
	}
}
