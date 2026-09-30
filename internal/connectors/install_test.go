// This file tests EnsureRuntime, Install and Installed against a local
// https server and a fake Runner that records each command. No test
// here touches the network or runs npm, uv or docker.

package connectors

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// testPlatform is the made-up platform the test pins name, so the tests
// behave the same on every computer.
const testPlatform = "test_os"

// fakeRuns records every Cmd a fake Runner gets.
type fakeRuns struct {
	mu   sync.Mutex
	cmds []Cmd
}

// add records c.
func (f *fakeRuns) add(c Cmd) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, c)
}

// all returns a copy of the recorded commands.
func (f *fakeRuns) all() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

// fakeRunner returns a Runner that records each Cmd and then calls do,
// which plays the program's part, such as writing the files npm would.
func fakeRunner(runs *fakeRuns, do func(c Cmd) (string, error)) Runner {
	return func(ctx context.Context, c Cmd, line func(string)) (string, error) {
		runs.add(c)
		if do == nil {
			return "", nil
		}
		out, err := do(c)
		if line != nil && out != "" {
			for _, l := range strings.Split(out, "\n") {
				line(l)
			}
		}
		return out, err
	}
}

// runtimeFixtures builds the fake Node and uv archives and the server
// that serves them, and returns pins that point at it.
func runtimeFixtures(t *testing.T) (*fileServer, map[string]Runtime) {
	t.Helper()
	node := makeTarGz(t, []tarEntry{
		{name: "node-v99.0.0-test/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "node-v99.0.0-test/bin/node", body: "#!/bin/sh\n", mode: 0o755},
		{name: "node-v99.0.0-test/lib/node_modules/npm/bin/npm-cli.js", body: "// npm"},
	})
	uv := makeTarGz(t, []tarEntry{
		{name: "uv-test/uv", body: "#!/bin/sh\n", mode: 0o755},
	})
	srv := newFileServer(t, map[string][]byte{"/node.tar.gz": node, "/uv.tar.gz": uv})
	pins := map[string]Runtime{
		RuntimeNode: {Name: RuntimeNode, Version: "99.0.0", Program: "bin/node",
			Downloads: map[string]Binary{testPlatform: {URL: srv.URL + "/node.tar.gz", SHA256: sum(node)}}},
		RuntimeUV: {Name: RuntimeUV, Version: "9.9.9", Program: "uv",
			Downloads: map[string]Binary{testPlatform: {URL: srv.URL + "/uv.tar.gz", SHA256: sum(uv)}}},
	}
	return srv, pins
}

// testInstaller returns an Installer over a fresh Meru home, the fixture
// runtimes and run.
func testInstaller(t *testing.T, run Runner) (*Installer, *fileServer) {
	t.Helper()
	srv, pins := runtimeFixtures(t)
	return &Installer{
		MeruDir:  filepath.Join(t.TempDir(), ".meru"),
		Home:     t.TempDir(),
		Platform: testPlatform,
		Runtimes: pins,
		Client:   srv.Client(),
		Run:      run,
	}, srv
}

// npmManifest returns an npm stdio manifest like obsidian's, at version.
func npmManifest(version string) Manifest {
	return Manifest{
		ID: "notes", Name: "Notes", Kind: KindStdio,
		Install: Install{Type: InstallNPM, Package: "notes-mcp", Version: version},
		Launch: Launch{
			Command: "notes-mcp",
			Args:    []string{"serve", "--vault", "{field.vault_name}={field.vault_path}"},
		},
		Fields: []Field{
			{ID: "vault_path", Type: FieldFolder, Label: "Vault", Required: true},
			{ID: "vault_name", Type: FieldText, Label: "Name", Default: "vault"},
		},
		Health: Health{Tool: "list"},
		MCP:    MCP{Allow: []string{"list"}},
	}
}

// fakeNPM plays npm install: it writes the package's package.json and
// its program's script under the --prefix folder, as npm would.
func fakeNPM(bin any) func(c Cmd) (string, error) {
	return func(c Cmd) (string, error) {
		prefix := c.Args[slices.Index(c.Args, "--prefix")+1]
		pkgDir := filepath.Join(prefix, "node_modules", "notes-mcp")
		if err := os.MkdirAll(filepath.Join(pkgDir, "dist"), 0o750); err != nil {
			return "", err
		}
		meta, _ := json.Marshal(map[string]any{"name": "notes-mcp", "bin": bin})
		if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), meta, 0o600); err != nil {
			return "", err
		}
		return "added 4 packages", os.WriteFile(filepath.Join(pkgDir, "dist", "main.js"), []byte("#!/usr/bin/env node\n"), 0o600)
	}
}

// envMap turns "KEY=value" strings into a map, and fails the test on a
// name that appears twice.
func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := out[k]; dup {
			t.Errorf("env names %s twice", k)
		}
		out[k] = v
	}
	return out
}

// TestEnsureRuntime downloads, checks and unpacks the fake Node, and
// checks the marker, the second call, and the removal of an old version.
func TestEnsureRuntime(t *testing.T) {
	in, srv := testInstaller(t, nil)
	rt := in.runtimeDir()
	// An older Node from an earlier pin, which the new one replaces.
	old := filepath.Join(rt, "node-98.0.0")
	if err := os.MkdirAll(old, 0o750); err != nil {
		t.Fatal(err)
	}
	var lines []string
	dir, err := in.EnsureRuntime(context.Background(), RuntimeNode, func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatalf("EnsureRuntime: %v", err)
	}
	if want := filepath.Join(rt, "node-99.0.0"); dir != want {
		t.Errorf("dir = %s, want %s", dir, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
		t.Errorf("bin/node: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, markerName)); err != nil {
		t.Errorf("marker: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the old node-98.0.0 is still there (%v)", err)
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "Downloading node 99.0.0") {
		t.Errorf("progress = %q", lines)
	}
	entries, _ := os.ReadDir(rt)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("left the temporary folder %s", e.Name())
		}
	}

	// A second call finds the marker and downloads nothing.
	before := srv.requests.Load()
	if _, err := in.EnsureRuntime(context.Background(), RuntimeNode, nil); err != nil {
		t.Fatalf("second EnsureRuntime: %v", err)
	}
	if srv.requests.Load() != before {
		t.Error("the second call downloaded again")
	}
}

// TestEnsureRuntimeRefuses checks each way EnsureRuntime must fail, and
// that no failure leaves a runtime folder behind.
func TestEnsureRuntimeRefuses(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(in *Installer)
		wantErr error
		wantMsg string
	}{
		{"checksum mismatch", func(in *Installer) {
			rt := in.Runtimes[RuntimeNode]
			d := rt.Downloads[testPlatform]
			d.SHA256 = sum([]byte("something else"))
			rt.Downloads = map[string]Binary{testPlatform: d}
			in.Runtimes[RuntimeNode] = rt
		}, ErrChecksum, ""},
		{"no pin for this platform", func(in *Installer) { in.Platform = "windows_amd64" }, ErrNoRuntime, ""},
		{"archive without the program", func(in *Installer) {
			// Point node at the uv archive, which has no bin/node.
			rt := in.Runtimes[RuntimeNode]
			rt.Downloads = in.Runtimes[RuntimeUV].Downloads
			in.Runtimes[RuntimeNode] = rt
		}, nil, "has no bin/node"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, _ := testInstaller(t, nil)
			tt.edit(in)
			_, err := in.EnsureRuntime(context.Background(), RuntimeNode, nil)
			switch {
			case err == nil:
				t.Fatal("EnsureRuntime passed, want an error")
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("EnsureRuntime: %v, want %v", err, tt.wantErr)
			case tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg):
				t.Fatalf("EnsureRuntime: %v, want %q", err, tt.wantMsg)
			}
			if _, ok := in.RuntimeDir(RuntimeNode); ok {
				t.Error("RuntimeDir reports the runtime there after a failure")
			}
			entries, _ := os.ReadDir(in.runtimeDir())
			for _, e := range entries {
				t.Errorf("a failure left %s in the runtime folder", e.Name())
			}
		})
	}
}

// TestEnsureRuntimeReplacesPartial checks that a runtime folder without
// a marker, left by an install that stopped part way, is replaced.
func TestEnsureRuntimeReplacesPartial(t *testing.T) {
	in, _ := testInstaller(t, nil)
	partial := filepath.Join(in.runtimeDir(), "node-99.0.0")
	if err := os.MkdirAll(partial, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "half"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := in.RuntimeDir(RuntimeNode); ok {
		t.Fatal("RuntimeDir trusts a folder with no marker")
	}
	dir, err := in.EnsureRuntime(context.Background(), RuntimeNode, nil)
	if err != nil {
		t.Fatalf("EnsureRuntime: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "half")); !os.IsNotExist(err) {
		t.Error("the partial folder's file survived")
	}
}

// TestInstallNPM checks the npm command line and environment, the marker,
// and that a second Install runs nothing.
func TestInstallNPM(t *testing.T) {
	// A variable merud might hold, which no install command may see.
	t.Setenv("MERU_TEST_SECRET", "hunter2")
	runs := &fakeRuns{}
	in, _ := testInstaller(t, fakeRunner(runs, fakeNPM("dist/main.js")))
	m := npmManifest("1.0.0")

	var lines []string
	got, err := in.Install(context.Background(), m, func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	rt := in.runtimeDir()
	nodeDir := filepath.Join(rt, "node-99.0.0")
	pkgDir := filepath.Join(rt, "pkg", "notes-1.0.0")
	want := Installed{ID: "notes", Version: "1.0.0", Runtime: "node-99.0.0", Dir: pkgDir}
	if got != want {
		t.Errorf("Install = %+v, want %+v", got, want)
	}
	if !slices.Contains(lines, "added 4 packages") {
		t.Errorf("progress didn't get npm's output: %q", lines)
	}

	cmds := runs.all()
	if len(cmds) != 1 {
		t.Fatalf("ran %d commands, want 1", len(cmds))
	}
	c := cmds[0]
	if c.Path != filepath.Join(nodeDir, "bin", "node") {
		t.Errorf("Path = %s, want the pinned node", c.Path)
	}
	wantArgs := []string{
		filepath.Join(nodeDir, "lib", "node_modules", "npm", "bin", "npm-cli.js"),
		"install", "--prefix", pkgDir, "notes-mcp@1.0.0",
		"--no-audit", "--no-fund", "--no-update-notifier", "--ignore-scripts",
	}
	if !slices.Equal(c.Args, wantArgs) {
		t.Errorf("Args =\n%q\nwant\n%q", c.Args, wantArgs)
	}
	env := envMap(t, c.Env)
	if !strings.HasPrefix(env["PATH"], filepath.Join(nodeDir, "bin")+string(os.PathListSeparator)) {
		t.Errorf("PATH = %s, want the pinned node's bin first", env["PATH"])
	}
	for _, k := range []string{"HOME", "npm_config_cache", "npm_config_userconfig"} {
		if !strings.HasPrefix(env[k], rt+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a path inside %s", k, env[k], rt)
		}
	}
	if _, ok := env["MERU_TEST_SECRET"]; ok {
		t.Error("the install command got merud's environment")
	}
	if data, err := os.ReadFile(env["npm_config_userconfig"]); err != nil || len(data) != 0 {
		t.Errorf("the npmrc file: %q, %v; want an empty file", data, err)
	}

	if _, ok := in.Installed(m); !ok {
		t.Error("Installed = false after Install")
	}
	if _, err := in.Install(context.Background(), m, nil); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(runs.all()) != 1 {
		t.Error("the second Install ran npm again")
	}
}

// TestInstallFailureCleansUp checks that a failed npm install reports
// npm's last lines, and leaves no folder for Installed to trust.
func TestInstallFailureCleansUp(t *testing.T) {
	runs := &fakeRuns{}
	in, _ := testInstaller(t, fakeRunner(runs, func(c Cmd) (string, error) {
		return "npm error 404 Not Found - notes-mcp@1.0.0", errors.New("exit status 1")
	}))
	m := npmManifest("1.0.0")
	_, err := in.Install(context.Background(), m, nil)
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("Install: %v, want npm's last line in the error", err)
	}
	if _, ok := in.Installed(m); ok {
		t.Error("Installed = true after a failure")
	}
	if _, err := os.Stat(in.PkgDir(m)); !os.IsNotExist(err) {
		t.Errorf("the failed install's folder is still there (%v)", err)
	}
}

// TestInstallReinstall checks the upgrade path: a partial folder goes
// before the install, a new version installs beside the old, and the old
// goes once the new is complete. A new Node pin also means a reinstall.
func TestInstallReinstall(t *testing.T) {
	runs := &fakeRuns{}
	in, _ := testInstaller(t, fakeRunner(runs, fakeNPM("dist/main.js")))
	ctx := context.Background()

	v1 := npmManifest("1.0.0")
	// A folder from an install that stopped part way: no marker.
	if err := os.MkdirAll(in.PkgDir(v1), 0o750); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(in.PkgDir(v1), "junk")
	if err := os.WriteFile(junk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := in.Installed(v1); ok {
		t.Fatal("Installed trusts a folder with no marker")
	}
	if _, err := in.Install(ctx, v1, nil); err != nil {
		t.Fatalf("Install 1.0.0: %v", err)
	}
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Error("the partial folder's file survived the install")
	}

	v2 := npmManifest("1.1.0")
	if _, ok := in.Installed(v2); ok {
		t.Fatal("Installed = true for a version never installed")
	}
	if _, err := in.Install(ctx, v2, nil); err != nil {
		t.Fatalf("Install 1.1.0: %v", err)
	}
	if _, err := os.Stat(in.PkgDir(v1)); !os.IsNotExist(err) {
		t.Errorf("the old 1.0.0 folder is still there (%v)", err)
	}
	if _, ok := in.Installed(v2); !ok {
		t.Error("Installed = false for 1.1.0")
	}

	// A new Node pin: the package must install again with it.
	rt := in.Runtimes[RuntimeNode]
	rt.Version = "100.0.0"
	in.Runtimes[RuntimeNode] = rt
	if _, ok := in.Installed(v2); ok {
		t.Error("Installed = true after the Node pin moved")
	}
}

// TestInstallPip checks the two uv commands and that every uv folder
// sits inside ~/.meru/runtime.
func TestInstallPip(t *testing.T) {
	runs := &fakeRuns{}
	in, _ := testInstaller(t, fakeRunner(runs, nil))
	m := Manifest{
		ID: "mail", Name: "Mail", Kind: KindHTTP,
		Install: Install{Type: InstallPip, Package: "mail-mcp", Version: "1.30.0"},
		Launch:  Launch{Command: "mail-mcp", URL: "http://127.0.0.1:8000/mcp", Port: 8000, Auth: AuthNone},
		Health:  Health{Tool: "list"},
	}
	got, err := in.Install(context.Background(), m, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	rt := in.runtimeDir()
	uvDir := filepath.Join(rt, "uv-9.9.9")
	pkgDir := filepath.Join(rt, "pkg", "mail-1.30.0")
	if got.Runtime != "uv-9.9.9" || got.Dir != pkgDir {
		t.Errorf("Install = %+v", got)
	}
	cmds := runs.all()
	if len(cmds) != 2 {
		t.Fatalf("ran %d commands, want 2", len(cmds))
	}
	venv := filepath.Join(pkgDir, ".venv")
	want := [][]string{
		{"venv", venv, "--python", pythonVersion},
		{"pip", "install", "--python", filepath.Join(venv, "bin", "python"), "mail-mcp==1.30.0"},
	}
	for i, c := range cmds {
		if c.Path != filepath.Join(uvDir, "uv") {
			t.Errorf("command %d Path = %s, want the pinned uv", i, c.Path)
		}
		if !slices.Equal(c.Args, want[i]) {
			t.Errorf("command %d Args = %q, want %q", i, c.Args, want[i])
		}
		env := envMap(t, c.Env)
		for _, k := range []string{
			"HOME", "UV_CACHE_DIR", "UV_PYTHON_INSTALL_DIR", "UV_PYTHON_BIN_DIR",
			"UV_PYTHON_CACHE_DIR", "UV_TOOL_DIR", "UV_TOOL_BIN_DIR",
		} {
			if !strings.HasPrefix(env[k], rt+string(filepath.Separator)) {
				t.Errorf("command %d: %s = %q, want a path inside %s", i, k, env[k], rt)
			}
		}
		if env["UV_PYTHON_PREFERENCE"] != "only-managed" || env["UV_NO_CONFIG"] != "1" {
			t.Errorf("command %d: UV_PYTHON_PREFERENCE = %q, UV_NO_CONFIG = %q", i, env["UV_PYTHON_PREFERENCE"], env["UV_NO_CONFIG"])
		}
	}
}

// binaryManifestFor returns a binary stdio manifest whose download for
// the test platform is url with SHA-256 sha.
func binaryManifestFor(url, sha string) Manifest {
	return Manifest{
		ID: "tool", Name: "Tool", Kind: KindStdio,
		Install: Install{Type: InstallBinary, Version: "1.2.3",
			Binaries: map[string]Binary{"darwin_arm64": {URL: url, SHA256: sha}}},
		Launch: Launch{Command: "{pkg}/tool-bin"},
		Health: Health{Tool: "ping"},
	}
}

// TestInstallBinary checks a download that matches its SHA-256 and one
// that doesn't.
func TestInstallBinary(t *testing.T) {
	body := []byte("#!/bin/sh\necho tool\n")
	srv := newFileServer(t, map[string][]byte{"/tool-bin": body})
	tests := []struct {
		name    string
		sha     string
		wantErr error
	}{
		{"match", sum(body), nil},
		{"mismatch", sum([]byte("changed")), ErrChecksum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, _ := testInstaller(t, nil)
			// The manifest names darwin_arm64, a platform Validate knows;
			// the test Installer pretends to be one.
			in.Platform = "darwin_arm64"
			in.Client = srv.Client()
			m := binaryManifestFor(srv.URL+"/tool-bin", tt.sha)
			got, err := in.Install(context.Background(), m, nil)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Install: %v, want %v", err, tt.wantErr)
				}
				if _, err := os.Stat(in.PkgDir(m)); !os.IsNotExist(err) {
					t.Error("a refused download left its folder")
				}
				return
			}
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			info, err := os.Stat(filepath.Join(got.Dir, "tool-bin"))
			if err != nil || info.Mode()&0o100 == 0 {
				t.Fatalf("tool-bin: %v; want a file the owner can run", err)
			}
			path, _, _, err := in.LaunchCommand(m, got, nil)
			if err != nil || path != filepath.Join(got.Dir, "tool-bin") {
				t.Errorf("LaunchCommand = %s, %v", path, err)
			}
		})
	}
}

// TestInstallContainer checks the three ways a container install ends:
// no docker, docker not running, and a pull by digest.
func TestInstallContainer(t *testing.T) {
	image := "docker.io/acme/search:2026.1@sha256:" + strings.Repeat("a", 64)
	m := Manifest{
		ID: "search", Name: "Search", Kind: KindContainer,
		Install: Install{Type: InstallContainer, Image: image},
		Launch:  Launch{URL: "http://127.0.0.1:8888", Port: 8080},
		Health:  Health{Path: "/healthz"},
	}
	tests := []struct {
		name       string
		haveDocker bool
		engineUp   bool
		wantErr    error
	}{
		{"docker missing", false, false, ErrDockerMissing},
		{"docker not running", true, false, ErrDockerNotRunning},
		{"pulled", true, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := &fakeRuns{}
			in, _ := testInstaller(t, fakeRunner(runs, func(c Cmd) (string, error) {
				if c.Args[0] == "info" && !tt.engineUp {
					return "Cannot connect to the Docker daemon", errors.New("exit status 1")
				}
				return "", nil
			}))
			docker := filepath.Join(in.Home, ".docker", "bin", "docker")
			in.DockerPaths = []string{filepath.Join(t.TempDir(), "absent", "docker"), "~/.docker/bin/docker"}
			if tt.haveDocker {
				if err := os.MkdirAll(filepath.Dir(docker), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(docker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := in.Install(context.Background(), m, nil)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Install: %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if got.Version != "sha256-aaaaaaaaaaaa" {
				t.Errorf("Version = %s", got.Version)
			}
			cmds := runs.all()
			if len(cmds) != 2 || cmds[1].Path != docker || !slices.Equal(cmds[1].Args, []string{"pull", image}) {
				t.Fatalf("commands = %+v, want info then pull %s", cmds, image)
			}
			if env := envMap(t, cmds[1].Env); env["HOME"] != in.Home {
				t.Errorf("docker's HOME = %s, want the user's home", env["HOME"])
			}
		})
	}
}
