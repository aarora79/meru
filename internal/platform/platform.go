// This file holds the table: one Platform row per system, and For and
// Current, which return a row.

package platform

import "runtime"

// Platform is one system's row: what Meru does differently on it. Every
// field is plain data, so a caller reads it and a test checks it on any
// system.
type Platform struct {
	// GOOS is Go's name for the system: "darwin", "linux" or "windows".
	GOOS string
	// Name is the name people use, such as "macOS".
	Name string
	// Here is how a client names the machine merud runs on, such as
	// "this Mac".
	Here string
	// Open is the program, with any arguments, that opens a URL in the
	// default app for it; the caller appends the URL as the last argument.
	Open []string
	// Clipboard lists the programs that copy standard input to the
	// clipboard, each with its arguments, in the order to try them.
	Clipboard [][]string
	// ProgramDirs lists the folders, in order, where installers put
	// programs such as docker that may be missing from merud's PATH: a
	// service started at login gets a short PATH. A folder that starts
	// with "~/" is under the home folder.
	ProgramDirs []string
	// OllamaHint tells the user how to install Ollama.
	OllamaHint string
	// AppleNotes is true when the system has the Notes app, whose
	// database the notes tools read.
	AppleNotes bool
	// Service names what restarts merud after a reboot: "launchd" or
	// "systemd", or "" where Meru has none yet.
	Service string
	// SharedGPUMemory is true when the GPU uses main memory, as on Apple
	// silicon, so main memory sets how large a model fits.
	SharedGPUMemory bool
}

// rows holds one row per system. It is a function, not a package-level
// variable, so no caller can change the table: each call builds new
// slices.
func rows() []Platform {
	return []Platform{
		{
			GOOS: "darwin", Name: "macOS", Here: "this Mac",
			Open:      []string{"open"},
			Clipboard: [][]string{{"pbcopy"}},
			// Homebrew on Intel and on Apple silicon, the system's own
			// folder, then OrbStack, Docker Desktop's two places.
			ProgramDirs: []string{
				"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin",
				"~/.orbstack/bin", "~/.docker/bin",
				"/Applications/Docker.app/Contents/Resources/bin",
			},
			OllamaHint:      "Install it with `brew install ollama`, or from https://ollama.com/download, then open the Ollama app.",
			Service:         "launchd",
			AppleNotes:      true,
			SharedGPUMemory: true,
		},
		{
			GOOS: "linux", Name: "Linux", Here: "this computer",
			Open: []string{"xdg-open"},
			// wl-copy for Wayland, then xclip and xsel for X11.
			Clipboard: [][]string{
				{"wl-copy"},
				{"xclip", "-selection", "clipboard"},
				{"xsel", "-b", "-i"},
			},
			// Distribution packages, hand installs, snaps, then Docker
			// Desktop for Linux.
			ProgramDirs: []string{"/usr/bin", "/usr/local/bin", "/snap/bin", "~/.docker/bin"},
			OllamaHint:  "Install it with: curl -fsSL https://ollama.com/install.sh | sh",
			Service:     "systemd",
		},
		{
			GOOS: "windows", Name: "Windows", Here: "this computer",
			// url.dll's FileProtocolHandler hands a URL to its default app,
			// as a double-click would.
			Open:       []string{"rundll32", "url.dll,FileProtocolHandler"},
			Clipboard:  [][]string{{"clip.exe"}},
			OllamaHint: "Install it from https://ollama.com/download, then start it.",
		},
	}
}

// For returns the row for goos. A system with no row, such as FreeBSD,
// gets the Linux row under its own GOOS and name: the Unix programs there
// are the likeliest to work.
func For(goos string) Platform {
	for _, p := range rows() {
		if p.GOOS == goos {
			return p
		}
	}
	// The recursion ends: the table has a "linux" row.
	p := For("linux")
	p.GOOS, p.Name = goos, goos
	return p
}

// Current returns the row for the system this program runs on.
func Current() Platform {
	return For(runtime.GOOS)
}
