// This file holds the one line that says what Meru is, and the Bridge
// method behind the Library's About section: that line, the app's version,
// where Meru keeps its files, and the links to the project on GitHub.

package desktop

import (
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Tagline says in one line what Meru is. The window's title bar, the
// rail's logo and the About section all show it, so it lives here once;
// index.html repeats it for the logo's tooltip, and a test keeps the two
// the same.
const Tagline = "A personal AI assistant that runs entirely on your own computer"

// WindowTitle is the title bar's text: the name first, as macOS cuts a
// long title from the end, then the tagline.
const WindowTitle = "Meru · " + Tagline

// The project's pages on GitHub. They live in Go, not in the page, so the
// page's own files name no host at all (assets_test.go checks that); the
// page gets them from About and opens each through OpenURL.
const (
	sourceURL  = "https://github.com/aarora79/meru"
	designURL  = sourceURL + "/blob/main/ARCHITECTURE.md"
	featureURL = sourceURL + "/issues/new"
	licenseURL = sourceURL + "/blob/main/LICENSE"
)

// License names the license Meru's code is under, as the LICENSE file at
// the top of the repository gives it.
const License = "GNU Affero General Public License v3.0 (AGPL-3.0)"

// About is what the Library's About section shows beside its own prose.
type About struct {
	// Tagline is the one line that says what Meru is.
	Tagline string `json:"tagline"`
	// Version is the app's build, such as "v0.3.0" or "(devel) 9b7be92".
	// merud reports no version over the socket, and the app and merud
	// come from the same source tree, so the app's own build stands in.
	Version string `json:"version"`
	// License is the License constant.
	License string `json:"license"`
	// ConfigPath is config.toml, and DataDir the folder that holds it,
	// the transcripts, the memories and the index, both written with ~
	// for the home folder.
	ConfigPath string `json:"config_path"`
	DataDir    string `json:"data_dir"`
	// Links are the project's pages, in the order the section lists them.
	Links []Link `json:"links"`
}

// Link is one of the project's pages: a button's label and the page's
// URL. ID lets the page put its own words beside a link.
type Link struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// About returns what the About section shows. It asks merud for nothing,
// so it works while merud is down.
func (b *Bridge) About() About {
	dir := b.dir
	if dir == "" {
		// merud's socket sits in its home folder, so the folder that
		// holds the socket is the best guess left.
		dir = filepath.Dir(b.socket)
	}
	return About{
		Tagline:    Tagline,
		Version:    appVersion(),
		License:    License,
		ConfigPath: tilde(filepath.Join(dir, "config.toml"), b.home),
		DataDir:    tilde(dir, b.home),
		Links: []Link{
			{ID: "source", Label: "Source code on GitHub", URL: sourceURL},
			{ID: "design", Label: "Read the design", URL: designURL},
			{ID: "feature", Label: "Request a feature", URL: featureURL},
			{ID: "license", Label: "Read the license", URL: licenseURL},
		},
	}
}

// releaseVersion holds the version `make release` writes into the app
// with the linker flag -X, such as "v0.4.1". Every other build leaves it
// empty. The linker sets it before main starts and nothing changes it
// after, so it acts as a constant. docs/releasing.md shows the flag.
var releaseVersion string

// appVersion returns the version the About section shows: the one
// `make release` stamped in, else the one buildVersion reads from the
// binary's build information.
func appVersion() string {
	if releaseVersion != "" {
		return releaseVersion
	}
	return buildVersion(debug.ReadBuildInfo())
}

// buildVersion turns what the Go toolchain recorded in the binary into a
// version: the module's tag for a release build, and for a local build
// "(devel)" with the first seven characters of the git commit, and
// "modified" when the tree had changes. ok is false when the binary
// carries no build information, as in some test binaries.
func buildVersion(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "unknown"
	}
	v := info.Main.Version
	if v == "" {
		v = "(devel)"
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if v != "(devel)" || rev == "" {
		return v
	}
	v += " " + rev[:min(7, len(rev))]
	if modified == "true" {
		v += ", modified"
	}
	return v
}

// tilde writes path with ~ in place of the home folder, as the rest of
// the app shows paths, or leaves it as it is when it lies outside home.
func tilde(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return filepath.Join("~", rest)
	}
	return path
}
