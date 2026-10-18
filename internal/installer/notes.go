// This file holds the "Apple Notes" step. Meru reads Apple Notes through
// three built-in tools in merud, and macOS lets merud read the Notes
// database only once the user turns on Full Disk Access for it. macOS
// never asks for that grant, and no program may grant it, so the step
// turns the three tools on in config.toml and gives the user two buttons:
// one opens the Full Disk Access list in System Settings, the other shows
// merud in Finder, ready to drag into that list. See ARCHITECTURE.md,
// "Installer", and docs/faq/apple-notes.md.

package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// FullDiskAccessURL opens System Settings at Privacy & Security, Full Disk
// Access. A URL in Apple's x-apple.systempreferences form, which open
// hands to System Settings, as it hands an https link to the browser.
const FullDiskAccessURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"

// notesTools are the three built-in tools that read Apple Notes.
func notesTools() []string { return []string{"search_notes", "list_notes", "read_note"} }

// SetUpNotes makes sure [builtin] tools lists the three Apple Notes tools
// and returns what it did. The grant itself happens in System Settings,
// where the step can't see it: merud checks it on every call, and a
// tool without it answers with the steps to turn it on.
func SetUpNotes(p Paths) (string, error) {
	cfg, err := loadConfig(p.Config())
	if err != nil {
		return "", err
	}
	if err := turnOnTools(p, cfg, notesTools()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Turned on search_notes, list_notes and read_note in %s. Meru reads your notes once merud has Full Disk Access; "+
		"until then, a question about them answers with the steps to turn it on.", p.Tilde(p.Config())), nil
}

// OpenFullDiskAccess opens System Settings at the Full Disk Access list.
func (b *Bridge) OpenFullDiskAccess() error {
	_, err := b.o.Run(b.ctx, "open", []string{FullDiskAccessURL}, nil)
	return err
}

// ShowMerud opens a Finder window with merud selected in ~/.local/bin, so
// the user can drag it into the Full Disk Access list. It fails when
// merud isn't there yet, because the Install Meru step was skipped.
func (b *Bridge) ShowMerud() error {
	merud := filepath.Join(b.paths.Bin(), "merud")
	if _, err := os.Stat(merud); err != nil {
		return fmt.Errorf("merud isn't in %s yet; run the Install Meru step first", b.paths.Tilde(b.paths.Bin()))
	}
	// open -R reveals a file in Finder instead of opening it.
	_, err := b.o.Run(b.ctx, "open", []string{"-R", merud}, nil)
	return err
}
