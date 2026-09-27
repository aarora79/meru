// This file holds the Bridge method behind the Library's About section:
// the one line that says what Meru is, the app's version, where Meru keeps
// its files, and the links to the project on GitHub. internal/about holds
// the line, the version and the links, for this app and for `meru chat`.

package desktop

import (
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/about"
)

// Tagline says in one line what Meru is. The window's title bar, the
// rail's logo and the About section all show it; it comes from
// internal/about, which the chat's /about box reads too. index.html
// repeats it for the logo's tooltip, and a test keeps the two the same.
const Tagline = about.Tagline

// WindowTitle is the title bar's text: the name first, as macOS cuts a
// long title from the end, then the tagline.
const WindowTitle = "Meru · " + Tagline

// License names the license Meru's code is under, as the LICENSE file at
// the top of the repository gives it.
const License = about.License

// About is what the Library's About section shows beside its own prose.
type About struct {
	// Tagline is the one line that says what Meru is.
	Tagline string `json:"tagline"`
	// Version is the app's build, such as "v0.3.0" or "(devel) 9b7be92".
	// merud reports no version over the socket, and the app and merud
	// come from the same source tree, so the app's own build stands in.
	Version string `json:"version"`
	// ShortVersion is Version cut to fit beside the logo in the rail:
	// "v0.4.1" for a release, "dev 5325b3e" for any other build. See
	// about.ShortVersion.
	ShortVersion string `json:"short_version"`
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

// Link is one of the project's pages, as internal/about lists them: a
// button's label and the page's URL, with an ID that lets the page put
// its own words beside a link. "type Link = about.Link" makes Link a
// second name for that type, not a new type, so no copy is needed.
type Link = about.Link

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
		Tagline:      Tagline,
		Version:      about.Version(),
		ShortVersion: about.ShortVersion(),
		License:      License,
		ConfigPath:   tilde(filepath.Join(dir, "config.toml"), b.home),
		DataDir:      tilde(dir, b.home),
		Links:        about.Links(),
	}
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
