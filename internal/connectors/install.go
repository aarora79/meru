// This file installs a connector's program into its own folder,
// ~/.meru/runtime/pkg/<id>-<version>/: an npm package with the pinned
// Node, a Python package with the pinned uv, a download checked against
// its SHA-256, or a container image pulled by its digest. See
// ARCHITECTURE.md, "The runtime folder".

package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// pythonVersion is the Python release line each pip connector's
// environment uses. workspace-mcp 1.30.0 supports 3.10 to 3.12. uv picks
// the newest 3.12 release it knows, and the list it knows is fixed in
// each uv release, so the pinned uv also fixes the Python.
const pythonVersion = "3.12"

// Installer installs runtimes and connectors under one Meru home. Its
// fields are exported so a test can point it at a fake server, a fake
// Runner or a made-up platform; merud uses NewInstaller.
type Installer struct {
	// MeruDir is the Meru home, ~/.meru. Everything the Installer writes
	// goes under MeruDir/runtime.
	MeruDir string
	// Home is the user's home folder. Only docker gets it, since Docker
	// keeps its settings and socket there.
	Home string
	// Platform is <GOOS>_<GOARCH>, such as "darwin_arm64".
	Platform string
	// Runtimes are the pins, from Runtimes().
	Runtimes map[string]Runtime
	// Client makes the downloads.
	Client *http.Client
	// Run starts npm, uv and docker.
	Run Runner
	// DockerPaths are the places docker may live, in order. A path that
	// starts with "~/" sits under Home. Meru never asks PATH.
	DockerPaths []string
}

// NewInstaller returns an Installer for the Meru home meruDir and the
// user's home folder home, with the real pins, the real Runner and this
// computer's platform.
func NewInstaller(meruDir, home string) *Installer {
	return &Installer{
		MeruDir:  meruDir,
		Home:     home,
		Platform: runtime.GOOS + "_" + runtime.GOARCH,
		Runtimes: Runtimes(),
		// Downloads get 10 minutes each, enough for Node's 50 MiB on a
		// slow line; the caller's ctx can end them sooner.
		Client: &http.Client{Timeout: 10 * time.Minute},
		Run:    ExecRunner(),
		// Docker Desktop, OrbStack, colima and Linux packages each put
		// the docker command in one of these.
		DockerPaths: []string{
			"/usr/local/bin/docker", "/opt/homebrew/bin/docker", "/usr/bin/docker",
			"~/.orbstack/bin/docker", "~/.docker/bin/docker",
			"/Applications/Docker.app/Contents/Resources/bin/docker",
		},
	}
}

// runtimeDir is ~/.meru/runtime.
func (in *Installer) runtimeDir() string {
	return filepath.Join(in.MeruDir, "runtime")
}

// Installed describes one finished install. Its marker file holds the
// same record.
type Installed struct {
	// ID is the connector's ID.
	ID string `json:"id"`
	// Version is the package or binary version, or for a container the
	// first 12 hex digits of the image digest, as "sha256-<12 hex>".
	Version string `json:"version"`
	// Runtime is the runtime folder the install used, such as
	// "node-24.21.0", or empty for a binary or a container.
	Runtime string `json:"runtime,omitempty"`
	// Dir is the install folder. It isn't saved in the marker, since the
	// folder's own place says it.
	Dir string `json:"-"`
}

// installVersion is the version part of a connector's folder name: the
// pinned version, or for a container a short form of its digest.
func installVersion(m Manifest) string {
	if m.Install.Type == InstallContainer {
		_, digest, _ := strings.Cut(m.Install.Image, "@sha256:")
		if len(digest) >= 12 {
			return "sha256-" + digest[:12]
		}
	}
	return m.Install.Version
}

// runtimeFor names the runtime an install type needs, or "" for none.
func runtimeFor(installType string) string {
	switch installType {
	case InstallNPM:
		return RuntimeNode
	case InstallPip:
		return RuntimeUV
	}
	return ""
}

// PkgDir returns the folder a connector installs into:
// ~/.meru/runtime/pkg/<id>-<version>.
func (in *Installer) PkgDir(m Manifest) string {
	return filepath.Join(in.runtimeDir(), "pkg", m.ID+"-"+installVersion(m))
}

// Installed reports whether the manifest's pinned version is installed
// and complete: its folder has a marker file that names the same
// connector and version, and the same runtime version the pins name
// today. A new Node pin therefore means a new npm install, since a
// package may not run on another Node. It reads files only.
func (in *Installer) Installed(m Manifest) (Installed, bool) {
	dir := in.PkgDir(m)
	var got Installed
	if !readMarker(dir, &got) {
		return Installed{}, false
	}
	want := ""
	if name := runtimeFor(m.Install.Type); name != "" {
		want = in.Runtimes[name].DirName()
	}
	if got.ID != m.ID || got.Version != installVersion(m) || got.Runtime != want {
		return Installed{}, false
	}
	got.Dir = dir
	return got, true
}

// Install installs the connector m at its pinned version, unless it is
// already installed, and returns the finished install. progress, when
// not nil, gets one line per step and each line npm, uv or docker prints.
//
// It installs straight into the final folder, because a Python
// environment can't move once made. The marker file goes in last, so a
// folder without one is a leftover that Install deletes before it
// starts. When the new install is complete, the folders of the
// connector's older versions go. Callers must not run two installs of
// the same connector at once.
//
// It fails on a manifest that doesn't validate, on a download or checksum
// failure, when npm, uv or docker exits with an error (the error ends
// with their last lines of output), and with ErrDockerMissing or
// ErrDockerNotRunning for a container.
func (in *Installer) Install(ctx context.Context, m Manifest, progress func(string)) (Installed, error) {
	if err := Validate(m); err != nil {
		return Installed{}, err
	}
	if m.Install.Type == InstallNone {
		return Installed{}, fmt.Errorf("connector %s: Meru installs nothing for it", m.ID)
	}
	if done, ok := in.Installed(m); ok {
		return done, nil
	}

	var runtimeDir string
	if name := runtimeFor(m.Install.Type); name != "" {
		dir, err := in.EnsureRuntime(ctx, name, progress)
		if err != nil {
			return Installed{}, fmt.Errorf("connector %s: %w", m.ID, err)
		}
		runtimeDir = dir
	}

	dir := in.PkgDir(m)
	// No marker, so anything already here is unfinished.
	if err := os.RemoveAll(dir); err != nil {
		return Installed{}, fmt.Errorf("connector %s: remove the unfinished %s: %w", m.ID, dir, err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Installed{}, fmt.Errorf("connector %s: %w", m.ID, err)
	}
	say(progress, "Installing %s %s", m.Name, installVersion(m))

	var err error
	switch m.Install.Type {
	case InstallNPM:
		err = in.installNPM(ctx, m, runtimeDir, dir, progress)
	case InstallPip:
		err = in.installPip(ctx, m, runtimeDir, dir, progress)
	case InstallBinary:
		err = in.installBinary(ctx, m, dir)
	case InstallContainer:
		err = in.installContainer(ctx, m, progress)
	}
	if err != nil {
		// Leave nothing half-made behind.
		_ = os.RemoveAll(dir)
		return Installed{}, fmt.Errorf("connector %s: %w", m.ID, err)
	}

	done := Installed{ID: m.ID, Version: installVersion(m), Dir: dir}
	if runtimeDir != "" {
		done.Runtime = filepath.Base(runtimeDir)
	}
	if err := writeMarker(dir, done); err != nil {
		_ = os.RemoveAll(dir)
		return Installed{}, fmt.Errorf("connector %s: %w", m.ID, err)
	}
	// The connector's ID ends at the "-", and an ID holds no "-", so
	// "google-" never matches another connector's folder.
	removeOthers(filepath.Dir(dir), m.ID+"-", filepath.Base(dir))
	say(progress, "%s %s is installed", m.Name, installVersion(m))
	return done, nil
}

// baseEnv is the short environment every install command starts from:
// a PATH of the given folders and the system's own, a HOME inside the
// runtime folder, and LANG. The HOME is a trap for any setting file npm
// or uv might still look for: it lands in ~/.meru/runtime/home, never in
// the user's home. TMPDIR comes from merud, when it has one.
func (in *Installer) baseEnv(pathDirs ...string) []string {
	dirs := slices.Concat(pathDirs, []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"})
	env := []string{
		"PATH=" + strings.Join(dirs, string(os.PathListSeparator)),
		"HOME=" + filepath.Join(in.runtimeDir(), "home"),
		"LANG=en_US.UTF-8",
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

// npmCmd builds the command that installs an npm package into dir with
// the Node in nodeDir. It runs npm's own script with that Node, by
// absolute path, so no npm or node on PATH plays any part. The flags:
//
//   - --prefix puts the package in dir/node_modules;
//   - --no-audit, --no-fund and --no-update-notifier stop the calls npm
//     makes home for reports, funding notes and its own version;
//   - --ignore-scripts stops install scripts, the code a package may run
//     while it installs. obsidian-mcp 2.0.1 and its dependencies need
//     none.
//
// The environment keeps npm's cache and settings inside ~/.meru/runtime:
// npm_config_cache is the download cache, and npm_config_userconfig
// names an empty file, so the user's ~/.npmrc and ~/.npm play no part.
// npm's global settings file already sits inside the pinned Node's
// folder, at etc/npmrc, and Meru never writes one there. (Pointing both
// settings at the same file makes npm stop with "double-loading config".)
func (in *Installer) npmCmd(m Manifest, nodeDir, dir string) Cmd {
	rt := in.runtimeDir()
	env := append(in.baseEnv(filepath.Join(nodeDir, "bin")),
		"npm_config_cache="+filepath.Join(rt, "cache", "npm"),
		"npm_config_userconfig="+filepath.Join(rt, "npmrc"),
		"npm_config_update_notifier=false",
		"npm_config_fund=false",
		"npm_config_audit=false",
	)
	return Cmd{
		Path: filepath.Join(nodeDir, "bin", "node"),
		Args: []string{
			filepath.Join(nodeDir, "lib", "node_modules", "npm", "bin", "npm-cli.js"),
			"install", "--prefix", dir, m.Install.Package + "@" + m.Install.Version,
			"--no-audit", "--no-fund", "--no-update-notifier", "--ignore-scripts",
		},
		Env: env,
		Dir: dir,
	}
}

// installNPM runs npmCmd after making the empty settings file it names.
func (in *Installer) installNPM(ctx context.Context, m Manifest, nodeDir, dir string, progress func(string)) error {
	npmrc := filepath.Join(in.runtimeDir(), "npmrc")
	if err := os.WriteFile(npmrc, nil, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(in.runtimeDir(), "home"), 0o750); err != nil {
		return err
	}
	return in.run(ctx, in.npmCmd(m, nodeDir, dir), progress)
}

// uvEnv is the environment for uv. Every folder uv would put in the
// user's home goes under ~/.meru/runtime instead:
//
//   - UV_CACHE_DIR, the download cache;
//   - UV_PYTHON_INSTALL_DIR, the Pythons uv downloads, and
//     UV_PYTHON_BIN_DIR and UV_PYTHON_CACHE_DIR beside them;
//   - UV_TOOL_DIR and UV_TOOL_BIN_DIR, which uv tool would use.
//
// UV_PYTHON_PREFERENCE=only-managed makes uv use a Python it downloaded
// into that folder, never one from Homebrew or the system, so every Mac
// gets the same Python. UV_NO_CONFIG skips the user's uv.toml.
func (in *Installer) uvEnv(uvDir string) []string {
	rt := in.runtimeDir()
	return append(in.baseEnv(uvDir),
		"UV_CACHE_DIR="+filepath.Join(rt, "cache", "uv"),
		"UV_PYTHON_INSTALL_DIR="+filepath.Join(rt, "python"),
		"UV_PYTHON_BIN_DIR="+filepath.Join(rt, "python", "bin"),
		"UV_PYTHON_CACHE_DIR="+filepath.Join(rt, "cache", "uv-python"),
		"UV_TOOL_DIR="+filepath.Join(rt, "uv-tools"),
		"UV_TOOL_BIN_DIR="+filepath.Join(rt, "uv-tools", "bin"),
		"UV_PYTHON_PREFERENCE=only-managed",
		"UV_NO_CONFIG=1",
		"UV_NO_PROGRESS=1",
	)
}

// pipCmds builds the two commands that install a Python package into
// dir: uv venv makes a Python environment in dir/.venv, and uv pip
// install puts the package in it at its pinned version.
func (in *Installer) pipCmds(m Manifest, uvDir, dir string) []Cmd {
	uv := filepath.Join(uvDir, "uv")
	venv := filepath.Join(dir, ".venv")
	env := in.uvEnv(uvDir)
	return []Cmd{
		{Path: uv, Args: []string{"venv", venv, "--python", pythonVersion}, Env: env, Dir: dir},
		{
			Path: uv,
			Args: []string{"pip", "install", "--python", filepath.Join(venv, "bin", "python"), m.Install.Package + "==" + m.Install.Version},
			Env:  env,
			Dir:  dir,
		},
	}
}

// installPip runs pipCmds in order.
func (in *Installer) installPip(ctx context.Context, m Manifest, uvDir, dir string, progress func(string)) error {
	if err := os.MkdirAll(filepath.Join(in.runtimeDir(), "home"), 0o750); err != nil {
		return err
	}
	for _, c := range in.pipCmds(m, uvDir, dir) {
		if err := in.run(ctx, c, progress); err != nil {
			return err
		}
	}
	return nil
}

// installBinary downloads this platform's file into dir, checks its
// SHA-256, and marks it runnable. The file keeps the name its URL ends
// in.
func (in *Installer) installBinary(ctx context.Context, m Manifest, dir string) error {
	b, ok := m.Install.Binaries[in.Platform]
	if !ok {
		return fmt.Errorf("the manifest has no download for %s", in.Platform)
	}
	name := filepath.Base(b.URL)
	if name == "." || name == "/" || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("the URL %s names no file", b.URL)
	}
	dest := filepath.Join(dir, name)
	if err := download(ctx, in.Client, b.URL, b.SHA256, dest); err != nil {
		return err
	}
	// 0o700: the owner may read and run it; nobody else needs to.
	return os.Chmod(dest, 0o700) // #nosec G302 -- a program needs its execute bit; only the owner gets any
}

// ErrDockerMissing means no docker command is at any of DockerPaths.
var ErrDockerMissing = errors.New("docker isn't installed")

// ErrDockerNotRunning means docker is there but its engine doesn't
// answer, most often because Docker Desktop isn't open.
var ErrDockerNotRunning = errors.New("docker isn't running")

// findDocker returns the first of DockerPaths that is a file.
func (in *Installer) findDocker() (string, error) {
	for _, p := range in.DockerPaths {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = filepath.Join(in.Home, rest)
		}
		// os.Stat follows links: Homebrew's and Docker's commands are
		// links into their own folders.
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", ErrDockerMissing
}

// dockerEnv is docker's environment. It keeps the user's real HOME,
// where Docker keeps the socket it talks to and its credential settings,
// and a PATH with Docker's folders, so docker finds its credential
// helper.
func (in *Installer) dockerEnv(docker string) []string {
	dirs := []string{
		filepath.Dir(docker), "/usr/local/bin", "/opt/homebrew/bin",
		filepath.Join(in.Home, ".orbstack", "bin"), filepath.Join(in.Home, ".docker", "bin"),
		"/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}
	env := []string{
		"PATH=" + strings.Join(dirs, string(os.PathListSeparator)),
		"HOME=" + in.Home,
		"LANG=en_US.UTF-8",
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

// installContainer pulls the pinned image by its digest. It first asks
// the engine for its version, so "Docker isn't running" comes out as its
// own error rather than a pull failure.
func (in *Installer) installContainer(ctx context.Context, m Manifest, progress func(string)) error {
	docker, err := in.findDocker()
	if err != nil {
		return err
	}
	env := in.dockerEnv(docker)
	info := Cmd{Path: docker, Args: []string{"info", "--format", "{{.ServerVersion}}"}, Env: env}
	if tail, err := in.Run(ctx, info, nil); err != nil {
		return fmt.Errorf("%w: %s", ErrDockerNotRunning, lastLine(tail))
	}
	pull := Cmd{Path: docker, Args: []string{"pull", m.Install.Image}, Env: env}
	return in.run(ctx, pull, progress)
}

// run runs c through the Installer's Runner, and adds the program's last
// lines of output to the error when it fails, so the user sees why.
func (in *Installer) run(ctx context.Context, c Cmd, progress func(string)) error {
	tail, err := in.Run(ctx, c, progress)
	if err != nil {
		if tail = strings.TrimSpace(tail); tail != "" {
			return fmt.Errorf("%w\n%s", err, tail)
		}
		return err
	}
	return nil
}

// lastLine returns the last line of s that isn't blank, or "" when none.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
