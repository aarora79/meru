// This file holds the rule for the made-up Obsidian vault in
// testdata/vault: no file in it names a real business or address that an
// earlier draft used. The list lives in testdata/vault_old_names.txt.

package policy

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVaultNamesAreMadeUp fails when a file in testdata/vault holds a name
// from the list. It reads Markdown, text, CSV and the PDFs as they are,
// since the vault's PDFs keep their text uncompressed, and reads a Word
// file's XML parts from inside its ZIP.
func TestVaultNamesAreMadeUp(t *testing.T) {
	names := readList(t, "testdata/vault_old_names.txt")
	vault := filepath.Join(moduleRoot(t), "testdata", "vault")
	n := 0
	// WalkDir calls the function for each file and folder under vault.
	err := filepath.WalkDir(vault, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		text, err := vaultText(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(text)
		for _, name := range names {
			if strings.Contains(lower, strings.ToLower(name)) {
				rel, _ := filepath.Rel(vault, path)
				t.Errorf("testdata/vault/%s names %q; the vault's names must all be made up", rel, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("found no files in testdata/vault")
	}
}

// vaultText returns the text to check in one vault file: the XML inside a
// .docx, and the bytes of any other file. A .docx is a ZIP whose parts are
// compressed, so its raw bytes would hide the words.
func vaultText(path string) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".docx") {
		b, err := os.ReadFile(path) // #nosec G304 -- a file under testdata/vault
		return string(b), err
	}
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	// defer runs r.Close() when vaultText returns, however it returns.
	defer r.Close()
	var sb strings.Builder
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		// LimitReader caps each part at 1 MiB, far above any vault file.
		_, err = io.Copy(&sb, io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}
