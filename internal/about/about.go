// This file holds the tagline, the license, the project's links and the
// version: the one `make release` stamps in, or else the one the Go
// toolchain recorded in the binary.

package about

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// Tagline says in one line what Meru is. The desktop app's title bar, its
// rail and its About section show it, and so does the chat's /about box.
const Tagline = "A personal AI assistant that runs entirely on your own computer"

// License names the license Meru's code is under, as the LICENSE file at
// the top of the repository gives it.
const License = "Apache License 2.0"

// The project's pages on GitHub.
const (
	SourceURL  = "https://github.com/aarora79/meru"
	DesignURL  = SourceURL + "/blob/main/ARCHITECTURE.md"
	FeatureURL = SourceURL + "/issues/new"
	LicenseURL = SourceURL + "/blob/main/LICENSE"
)

// Link is one of the project's pages: a label for people, and the page's
// URL. ID lets a client put its own words beside a link.
//
// The `json:"..."` text after each field is a struct tag: it names the
// field in the JSON the desktop app's page receives.
type Link struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Links returns the project's pages in the order the About views list
// them. It returns a new slice each call, so no caller can change the
// list for another.
func Links() []Link {
	return []Link{
		{ID: "source", Label: "Source code on GitHub", URL: SourceURL},
		{ID: "design", Label: "Read the design", URL: DesignURL},
		{ID: "feature", Label: "Request a feature", URL: FeatureURL},
		{ID: "license", Label: "Read the license", URL: LicenseURL},
	}
}

// releaseVersion holds the version `make release` writes into the program
// with the linker flag -X, such as "v0.4.1". Every other build leaves it
// empty. The linker sets it before main starts and nothing changes it
// after, so it acts as a constant. docs/releasing.md shows the flag.
var releaseVersion string

// Version returns the program's version in full: the one `make release`
// stamped in, else the one buildVersion reads from the binary's build
// information, such as "(devel) 9b7be92, modified".
func Version() string {
	if releaseVersion != "" {
		return releaseVersion
	}
	// debug.ReadBuildInfo returns two values, the build information and
	// whether the binary carries any, and buildVersion takes both.
	return buildVersion(debug.ReadBuildInfo())
}

// ShortVersion returns Version cut to fit beside the name: "v0.4.1" for a
// release, "dev 5325b3e" for any other build, and "" when the build says
// nothing useful. See short.
func ShortVersion() string {
	return short(Version())
}

// pseudoVersion matches the version Go gives a build made between tags,
// such as "v0.4.2-0.20260927021103-5325b3ef94cb": the next patch, then a
// time and the commit's first twelve characters.
var pseudoVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+-(?:0\.)?\d{14}-([0-9a-f]{12})$`)

// short cuts v, as Version returns it, to fit beside the name. A release
// tag such as "v0.4.1" stays as it is. A pseudo-version or a
// "(devel) 5325b3e" build becomes "dev" and the commit's first seven
// characters, since the version Go guesses for a build between tags names
// a release that doesn't exist yet. It returns "" when v says nothing
// useful, such as "unknown".
func short(v string) string {
	if m := pseudoVersion.FindStringSubmatch(v); m != nil {
		return "dev " + m[1][:7]
	}
	if rest, ok := strings.CutPrefix(v, "(devel) "); ok {
		// rest is "5325b3e" or "5325b3e, modified"; keep the commit.
		sha, _, _ := strings.Cut(rest, ",")
		return "dev " + sha
	}
	if v == "(devel)" || v == "unknown" {
		return ""
	}
	return v
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
