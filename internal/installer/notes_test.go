// This file tests the Apple Notes step: the tools it turns on and the two
// buttons that help with Full Disk Access.

package installer

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// TestSetUpNotes checks that the step puts the three notes tools back in
// a [builtin] tools list that lacks them, keeps the others, and changes
// nothing on a second run.
func TestSetUpNotes(t *testing.T) {
	p := tempHome(t)
	tools := slices.DeleteFunc(config.BuiltinTools(), func(s string) bool { return slices.Contains(notesTools(), s) })
	if err := catalog.SetTableLists(p.Config(), "builtin", map[string][]string{"tools": tools}, nil); err != nil {
		t.Fatal(err)
	}
	msg, err := SetUpNotes(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Full Disk Access") {
		t.Errorf("result = %q", msg)
	}
	cfg, err := config.Load(p.Config())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(notesTools(), "web_fetch") {
		if !slices.Contains(cfg.Builtin.Tools, name) {
			t.Errorf("[builtin] tools %v lacks %s", cfg.Builtin.Tools, name)
		}
	}
	before := readFile(t, p.Config())
	if _, err := SetUpNotes(p); err != nil {
		t.Fatal(err)
	}
	if readFile(t, p.Config()) != before {
		t.Error("a second run changed config.toml")
	}
}

// TestNotesButtons checks that the buttons run open with the Full Disk
// Access pane and with merud to reveal, and that Show merud in Finder
// refuses before merud is installed.
func TestNotesButtons(t *testing.T) {
	r := &fakeRunner{}
	b, _ := newBridge(t, r)
	if err := b.ShowMerud(); err == nil {
		t.Error("ShowMerud worked with no merud installed")
	}
	if err := b.OpenFullDiskAccess(); err != nil {
		t.Fatal(err)
	}
	merud := filepath.Join(b.paths.Bin(), "merud")
	writeFile(t, merud, "binary")
	if err := b.ShowMerud(); err != nil {
		t.Fatal(err)
	}
	want := []string{"open " + FullDiskAccessURL, "open -R " + merud}
	if got := r.called(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}
