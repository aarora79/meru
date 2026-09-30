// This file pins the two runtimes Meru downloads for itself, Node and uv,
// and holds EnsureRuntime, which fetches one into ~/.meru/runtime the
// first time a connector needs it. See ARCHITECTURE.md, "The runtime
// folder".

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The runtimes Meru can download.
const (
	RuntimeNode = "node" // runs npm and every npm connector
	RuntimeUV   = "uv"   // makes each pip connector's Python environment
)

// Runtime is one pinned runtime: its version and, for each platform, the
// archive to download and the SHA-256 it must have.
type Runtime struct {
	Name    string
	Version string
	// Downloads maps a platform, <GOOS>_<GOARCH> such as "darwin_arm64",
	// to its .tar.gz archive. A platform missing here has no runtime yet.
	Downloads map[string]Binary
	// Program is the runtime's main program, relative to its folder once
	// unpacked: "bin/node" or "uv".
	Program string
}

// DirName is the runtime's folder under ~/.meru/runtime, such as
// "node-24.21.0". The version is in the name, so a new pin gets a new
// folder and never mixes with the old one.
func (r Runtime) DirName() string {
	return r.Name + "-" + r.Version
}

// Runtimes returns the pinned runtimes by name. It builds a new map on
// each call, so no caller can change a pin for the rest of the program.
//
// Windows isn't pinned yet: Node ships a .zip there and uv an .exe, and
// the rest of the connector code assumes macOS and Linux paths.
func Runtimes() map[string]Runtime {
	return map[string]Runtime{
		// Node 24.21.0 "Krypton", the newest Long Term Support release on
		// 2026-09-30 (released 2026-09-07). obsidian-mcp 2.0.1 needs Node
		// 22 or later. The hashes come from
		// https://nodejs.org/dist/v24.21.0/SHASUMS256.txt, read on
		// 2026-09-30.
		RuntimeNode: {
			Name:    RuntimeNode,
			Version: "24.21.0",
			Program: "bin/node",
			Downloads: map[string]Binary{
				"darwin_arm64": {
					URL:    "https://nodejs.org/dist/v24.21.0/node-v24.21.0-darwin-arm64.tar.gz",
					SHA256: "bed7eea5325e1108f32ce5228ddd6a5f0f08a499ee42aa7442aea583702f6057",
				},
				"darwin_amd64": {
					URL:    "https://nodejs.org/dist/v24.21.0/node-v24.21.0-darwin-x64.tar.gz",
					SHA256: "1462cb3b3046b815cf8ea436d3da450ec1a9f11dac7e5a46b0ada5305d7e8097",
				},
				"linux_amd64": {
					URL:    "https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-x64.tar.gz",
					SHA256: "6e1db87ef58b8819e5d5402eff1536491b18edd8eb7bee5ef7897876e88dc5ff",
				},
				"linux_arm64": {
					URL:    "https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-arm64.tar.gz",
					SHA256: "724282c3b43aec998aa9527380465b45d229e021b58035f5f4f63095eabfe5d5",
				},
			},
		},
		// uv 0.12.21, the newest release on 2026-09-30 (published
		// 2026-09-29). The hashes come from the uv-<target>.tar.gz.sha256
		// file beside each archive on
		// https://github.com/astral-sh/uv/releases/tag/0.12.21, read on
		// 2026-09-30; the release API's digest for each archive agrees.
		RuntimeUV: {
			Name:    RuntimeUV,
			Version: "0.12.21",
			Program: "uv",
			Downloads: map[string]Binary{
				"darwin_arm64": {
					URL:    "https://github.com/astral-sh/uv/releases/download/0.12.21/uv-aarch64-apple-darwin.tar.gz",
					SHA256: "b88bda573e566ef9bced66b155fe0408626fbbc053aee1c30ba686f0728c9447",
				},
				"darwin_amd64": {
					URL:    "https://github.com/astral-sh/uv/releases/download/0.12.21/uv-x86_64-apple-darwin.tar.gz",
					SHA256: "2b336763b396ec6afa20c5a8b083538ca7402445b868311979d740a4344c17d8",
				},
				"linux_amd64": {
					URL:    "https://github.com/astral-sh/uv/releases/download/0.12.21/uv-x86_64-unknown-linux-gnu.tar.gz",
					SHA256: "23f02075b652bb1df64178cfae41b5caf160822e720e2663568f3f5d63bc52c0",
				},
				"linux_arm64": {
					URL:    "https://github.com/astral-sh/uv/releases/download/0.12.21/uv-aarch64-unknown-linux-gnu.tar.gz",
					SHA256: "030b69227b40af8c1981b7301793dc66e71ed3c796ea8688209dd268bd91ec51",
				},
			},
		},
	}
}

// markerName is the file written last into a finished runtime or
// package folder. A folder without it is a leftover from an install that
// stopped part way, and Meru deletes it rather than trust it.
const markerName = ".meru-installed"

// runtimeMarker is what a runtime's marker file holds.
type runtimeMarker struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// ErrNoRuntime means Meru has no pinned runtime for this computer's
// system and processor, such as Windows today.
var ErrNoRuntime = errors.New("no pinned runtime for this platform yet")

// RuntimeDir returns the folder of the pinned runtime name, and whether
// it is there and complete. It downloads nothing.
func (in *Installer) RuntimeDir(name string) (string, bool) {
	rt, ok := in.Runtimes[name]
	if !ok {
		return "", false
	}
	dir := filepath.Join(in.runtimeDir(), rt.DirName())
	var m runtimeMarker
	if !readMarker(dir, &m) || m.Name != rt.Name || m.Version != rt.Version {
		return dir, false
	}
	return dir, true
}

// EnsureRuntime returns the folder of the pinned runtime name, such as
// ~/.meru/runtime/node-24.21.0, and downloads it first if it isn't there.
// progress, when not nil, gets one line per step.
//
// The download goes to a temporary folder under ~/.meru/runtime.
// EnsureRuntime checks its SHA-256 before it unpacks it; the unpacked
// folder gets its marker file and only then moves to its final name in
// one rename. So
// the final folder either doesn't exist or is complete. Once the new
// runtime is in place, the folders of older versions of it go.
//
// It fails with ErrNoRuntime on a platform with no pin, with ErrChecksum
// when the download doesn't match, and with ErrUnsafeArchive when the
// archive would write outside its folder.
func (in *Installer) EnsureRuntime(ctx context.Context, name string, progress func(string)) (string, error) {
	rt, ok := in.Runtimes[name]
	if !ok {
		return "", fmt.Errorf("runtime %q: Meru pins only node and uv", name)
	}
	if dir, done := in.RuntimeDir(name); done {
		return dir, nil
	}
	dl, ok := rt.Downloads[in.Platform]
	if !ok {
		return "", fmt.Errorf("%s %s on %s: %w", rt.Name, rt.Version, in.Platform, ErrNoRuntime)
	}
	say(progress, "Downloading %s %s", rt.Name, rt.Version)

	if err := os.MkdirAll(in.runtimeDir(), 0o750); err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}
	// The temporary folder sits beside the final one, on the same disk,
	// so the rename at the end is one step that can't half-happen.
	tmp, err := os.MkdirTemp(in.runtimeDir(), ".tmp-"+rt.DirName()+"-")
	if err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}
	defer os.RemoveAll(tmp)

	archive := filepath.Join(tmp, "archive.tar.gz")
	if err := download(ctx, in.Client, dl.URL, dl.SHA256, archive); err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}
	say(progress, "Unpacking %s %s", rt.Name, rt.Version)
	unpacked := filepath.Join(tmp, "out")
	if err := os.Mkdir(unpacked, 0o750); err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}
	if err := unpackTarGz(archive, unpacked); err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}
	if info, err := os.Stat(filepath.Join(unpacked, filepath.FromSlash(rt.Program))); err != nil || info.IsDir() {
		return "", fmt.Errorf("runtime %s: the archive has no %s", rt.Name, rt.Program)
	}
	if err := writeMarker(unpacked, runtimeMarker{Name: rt.Name, Version: rt.Version, SHA256: dl.SHA256}); err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}

	dir := filepath.Join(in.runtimeDir(), rt.DirName())
	// A folder already at the final name has no marker (RuntimeDir said
	// so): an install that stopped part way. It goes.
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("runtime %s: remove the unfinished %s: %w", rt.Name, dir, err)
	}
	if err := os.Rename(unpacked, dir); err != nil {
		return "", fmt.Errorf("runtime %s: %w", rt.Name, err)
	}
	removeOthers(in.runtimeDir(), rt.Name+"-", rt.DirName())
	say(progress, "%s %s is ready", rt.Name, rt.Version)
	return dir, nil
}

// removeOthers deletes each folder in parent whose name starts with
// prefix, other than keep. It removes the old versions of a runtime or a
// package once the new one is complete. A folder it can't remove stays;
// the next install tries again.
func removeOthers(parent, prefix, keep string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep && strings.HasPrefix(e.Name(), prefix) {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// writeMarker writes v as JSON into dir's marker file. It writes a
// temporary file and renames it, so a reader never sees half a marker.
func writeMarker(dir string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, markerName+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, markerName))
}

// readMarker reads dir's marker file into v, and reports whether it was
// there and parsed.
func readMarker(dir string, v any) bool {
	data, err := os.ReadFile(filepath.Join(dir, markerName)) // #nosec G304 -- dir is a folder under the runtime folder that Meru names
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// say sends one formatted line to progress, when there is one.
func say(progress func(string), format string, args ...any) {
	if progress != nil {
		progress(fmt.Sprintf(format, args...))
	}
}
