// This file enforces the "thin client" rule from AGENTS.md "Shape": the
// clients, cmd/meru and the desktop app (cmd/meru-desktop with
// internal/desktop), talk to merud over the socket and hold no model,
// store or agent logic. One test reads each client's own imports; the
// other asks the go command for everything each client pulls in, so a
// forbidden package reached through a helper package fails too.

package policy

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenPackage is a package a client must not reach, with the reason.
// The path is relative to the module path.
type forbiddenPackage struct {
	pkg    string
	reason string
}

// forbiddenClientPackages lists the module's packages that hold daemon-side
// logic, with the reason no client may reach each one.
//
// `meru` may import rpc, config, tui, about and loopback, plus catalog and
// secrets for `meru setup` and `meru mcp add`: those two read and write
// config.toml and secrets.toml and talk to no model and no store.
var forbiddenClientPackages = []forbiddenPackage{
	{"internal/engine", "the engine talks to the model runtime; only merud does that"},
	{"internal/transcript", "merud owns the session transcripts"},
	{"internal/agent", "the agent loop and dispatch run in merud"},
	{"internal/store", "merud owns the store"},
	{"internal/retrieve", "retrieval runs in merud next to the store"},
	{"internal/memory", "merud owns memory"},
	{"internal/summarize", "merud writes the session summaries with the fast model"},
	{"internal/mcp", "merud owns the MCP clients"},
	{"internal/dispatch", "every tool call goes through dispatch in merud"},
	{"internal/builtin", "the built-in tools run in merud, through dispatch"},
	{"internal/commands", "merud runs the local commands, through dispatch"},
	{"internal/a2a", "merud owns the A2A clients"},
	{"internal/scheduler", "the scheduler runs in merud"},
	{"internal/connectors", "merud installs, starts and checks the connectors; a client asks it over the socket"},
	{"internal/render", "merud runs web_fetch's page reader; a client only shows its progress line"},
}

// forbiddenDesktopPackages adds what the desktop app, unlike `meru`, has no
// use for. It only reads config.toml, to name the answer model; `meru
// setup` and `meru mcp add` stay the ways to change config and secrets.
var forbiddenDesktopPackages = []forbiddenPackage{
	{"internal/catalog", "the desktop app doesn't change config.toml; meru setup and meru mcp add do"},
	{"internal/secrets", "the desktop app never reads or writes secrets.toml"},
	{"internal/tui", "the terminal UI belongs to meru chat"},
}

// forbiddenClientSDKs lists outside packages no client may import: the
// OpenTelemetry SDK and exporters, since setting up export is merud's job,
// and Wails' self-updater, since Meru never checks for updates
// (non-negotiable 2). The tests check imports, not dependencies: rpc
// reaches the OpenTelemetry SDK through obs, and Wails' application
// package links its updater in, unconfigured, whether or not the app uses
// it.
var forbiddenClientSDKs = []string{
	"go.opentelemetry.io/otel/sdk",
	"go.opentelemetry.io/otel/exporters",
	"github.com/wailsapp/wails/v3/pkg/updater",
}

// forbiddenInstallerPackages lists what the Mac installer (cmd/meru-installer
// with internal/installer) must not reach. It runs before merud exists, so
// unlike the clients it may write config through catalog and the profile
// through memory, and run a fixed list of programs (installer_test.go).
// It never talks to a model, touches the store or runs a tool.
var forbiddenInstallerPackages = []forbiddenPackage{
	{"internal/engine", "the installer pulls models through Ollama's HTTP API, and never talks to a model"},
	{"internal/transcript", "merud owns the session transcripts"},
	{"internal/agent", "the agent loop runs in merud"},
	{"internal/store", "merud owns the store"},
	{"internal/retrieve", "retrieval runs in merud next to the store"},
	{"internal/index", "merud indexes the folders; the installer only lists them in config"},
	{"internal/summarize", "merud writes the session summaries"},
	{"internal/mcp", "merud owns the MCP clients"},
	{"internal/dispatch", "every tool call goes through dispatch in merud"},
	{"internal/builtin", "the built-in tools run in merud"},
	{"internal/commands", "merud runs the local commands; the installer only lists them in config"},
	{"internal/a2a", "merud owns the A2A clients"},
	{"internal/scheduler", "the scheduler runs in merud"},
	{"internal/tui", "the terminal UI belongs to meru chat"},
	{"internal/connectors", "merud installs, starts and checks the connectors; the installer will hand them to merud over the socket"},
	{"internal/render", "merud runs web_fetch's page reader and installs its browser on first need"},
}

// thinClient is one client the tests check: the directories that hold its
// code, the package go list starts from, the build tags that package
// needs, and what it must not reach. The Mac installer gets the same
// checks with its own list.
type thinClient struct {
	name      string
	dirs      []string
	pkg       string
	tags      string
	forbidden []forbiddenPackage
}

// thinClients returns the clients the layout tests check.
func thinClients() []thinClient {
	desktop := append(append([]forbiddenPackage{}, forbiddenClientPackages...), forbiddenDesktopPackages...)
	return []thinClient{
		{name: "cmd/meru", dirs: []string{"cmd/meru"}, pkg: "cmd/meru", forbidden: forbiddenClientPackages},
		// cmd/meru-desktop builds only with the desktop tag; production is
		// the tag `make desktop` adds.
		{name: "the desktop app", dirs: []string{"cmd/meru-desktop", "internal/desktop"}, pkg: "cmd/meru-desktop",
			tags: "desktop,production", forbidden: desktop},
		{name: "internal/desktop", dirs: []string{"internal/desktop"}, pkg: "internal/desktop", forbidden: desktop},
		{name: "the Mac installer", dirs: []string{"cmd/meru-installer", "internal/installer"}, pkg: "cmd/meru-installer",
			tags: "desktop,production", forbidden: forbiddenInstallerPackages},
		{name: "internal/installer", dirs: []string{"internal/installer"}, pkg: "internal/installer", forbidden: forbiddenInstallerPackages},
	}
}

// TestClientImports reads the import lines of every non-test file of each
// client and fails on a daemon-side package, a forbidden outside package,
// a call to obs.Setup, or a use of Wails' Updater.
func TestClientImports(t *testing.T) {
	root := moduleRoot(t)
	module := modulePath(t, root)
	obsPkg := module + "/internal/obs"
	files := moduleFiles(t, root)

	for _, c := range thinClients() {
		t.Run(c.name, func(t *testing.T) {
			seen := 0
			for _, f := range files {
				if f.isTest || !inDirs(f.rel, c.dirs) {
					continue
				}
				seen++
				checkClientFile(t, f, c, module, obsPkg)
			}
			if seen == 0 {
				t.Fatalf("found no files in %v; the scan is broken", c.dirs)
			}
		})
	}
}

// inDirs reports whether the file at rel sits right inside one of dirs.
func inDirs(rel string, dirs []string) bool {
	for _, d := range dirs {
		if filepath.ToSlash(filepath.Dir(rel)) == d {
			return true
		}
	}
	return false
}

// checkClientFile reports each rule f breaks for client c.
func checkClientFile(t *testing.T, f sourceFile, c thinClient, module, obsPkg string) {
	t.Helper()
	for _, imp := range f.file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		for _, fp := range c.forbidden {
			if full := module + "/" + fp.pkg; path == full || strings.HasPrefix(path, full+"/") {
				t.Errorf("%s: %s imports %s, but %s; keep the client thin (AGENTS.md \"Shape\")",
					f.where(imp.Pos()), c.name, path, fp.reason)
			}
		}
		if prefix, ok := matchModule(path, forbiddenClientSDKs); ok {
			t.Errorf("%s: %s imports %s (under %s); a client sets up no telemetry export and no updater",
				f.where(imp.Pos()), c.name, path, prefix)
		}
		if path == obsPkg {
			for _, pos := range selectorUses(f, importName(imp, "obs"), "Setup") {
				t.Errorf("%s: %s calls obs.Setup; only merud sets up OpenTelemetry export", pos, c.name)
			}
		}
	}
	// app.Updater is Wails' self-updater, reached through the App value
	// rather than an import. Meru never checks for updates.
	ast.Inspect(f.file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Updater" {
			t.Errorf("%s: %s uses Updater; Meru never checks for updates (non-negotiable 2)", f.where(sel.Pos()), c.name)
		}
		return true
	})
}

// TestClientDependencies asks `go list -deps` for every package each client
// needs, directly or through other packages, and fails when one of them is
// forbidden. The message shows the import chain that pulled it in.
func TestClientDependencies(t *testing.T) {
	root := moduleRoot(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command isn't on PATH; TestClientImports still runs")
	}
	module := modulePath(t, root)

	for _, c := range thinClients() {
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(root, c.pkg)); err != nil {
				t.Fatalf("%s is missing: %v", c.pkg, err)
			}
			// -deps lists the package and everything it imports, however
			// deep. The -f template prints one line per package: its path,
			// a colon, then the paths it imports directly.
			args := []string{"list", "-deps", "-f", `{{.ImportPath}}:{{join .Imports " "}}`}
			if c.tags != "" {
				args = append(args, "-tags", c.tags)
			}
			cmd := exec.Command(goBin, append(args, "./"+c.pkg)...)
			cmd.Dir = root
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps ./%s: %v\n%s", c.pkg, err, stderr.String())
			}

			graph := map[string][]string{}
			sc := bufio.NewScanner(bytes.NewReader(out))
			for sc.Scan() {
				pkg, imports, _ := strings.Cut(sc.Text(), ":")
				graph[pkg] = strings.Fields(imports)
			}
			start := module + "/" + c.pkg
			if _, ok := graph[start]; !ok {
				t.Fatalf("go list didn't list %s; the check would pass by finding nothing", start)
			}
			for _, fp := range c.forbidden {
				full := module + "/" + fp.pkg
				if chain := importChain(graph, start, full); chain != nil {
					t.Errorf("%s depends on %s through %s, but %s; keep the client thin (AGENTS.md \"Shape\")",
						c.name, full, strings.Join(chain, " -> "), fp.reason)
				}
			}
		})
	}
}

// importName returns the name a file uses for an import: the alias when the
// import line has one, or def otherwise.
func importName(imp *ast.ImportSpec, def string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	return def
}

// selectorUses returns the positions of every pkg.name expression in f, such
// as obs.Setup, formatted as "file:line".
func selectorUses(f sourceFile, pkg, name string) []string {
	var out []string
	ast.Inspect(f.file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
			out = append(out, f.where(sel.Pos()))
		}
		return true
	})
	return out
}

// importChain finds a shortest path of imports from start to target in
// graph, where graph maps each package to the packages it imports. It
// returns nil when target isn't reachable. It is a breadth-first search: it
// looks at everything one import away, then two, and so on.
func importChain(graph map[string][]string, start, target string) []string {
	prev := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if pkg == target {
			var chain []string
			for p := pkg; p != ""; p = prev[p] {
				chain = append([]string{p}, chain...)
			}
			return chain
		}
		for _, next := range graph[pkg] {
			if _, seen := prev[next]; !seen {
				prev[next] = pkg
				queue = append(queue, next)
			}
		}
	}
	return nil
}

// TestLayoutHelpers checks importChain and selectorUses on small inputs, so
// the layout tests can't pass by finding nothing.
func TestLayoutHelpers(t *testing.T) {
	graph := map[string][]string{
		"m/cmd/meru":     {"m/internal/rpc", "fmt"},
		"m/internal/rpc": {"m/internal/engine"},
	}
	chainTests := []struct {
		target string
		want   string
	}{
		{"m/internal/engine", "m/cmd/meru -> m/internal/rpc -> m/internal/engine"},
		{"m/internal/store", ""},
	}
	for _, tt := range chainTests {
		t.Run("chain to "+tt.target, func(t *testing.T) {
			got := strings.Join(importChain(graph, "m/cmd/meru", tt.target), " -> ")
			if got != tt.want {
				t.Errorf("importChain = %q, want %q", got, tt.want)
			}
		})
	}

	src := "package main\nimport o \"m/internal/obs\"\nfunc main() {\n\to.Setup(nil, o.Config{})\n}\n"
	f, err := parseSource("cmd/meru/main.go", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := selectorUses(f, importName(f.file.Imports[0], "obs"), "Setup")
	if want := []string{"cmd/meru/main.go:4"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("selectorUses = %v, want %v", got, want)
	}
}
