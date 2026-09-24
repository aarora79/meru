// This file enforces the "thin client" rule from AGENTS.md "Shape": cmd/meru
// talks to merud over the socket and holds no model, store or agent logic.
// One test reads cmd/meru's own imports; the other asks the go command for
// everything cmd/meru pulls in, so a forbidden package reached through a
// helper package fails too.

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

// clientDir is the client's main package, relative to the module root.
const clientDir = "cmd/meru"

// forbiddenClientPackages lists the module's packages that hold daemon-side
// logic, with the reason the client must not reach each one. Paths are
// relative to the module path.
var forbiddenClientPackages = []struct {
	pkg    string
	reason string
}{
	{"internal/engine", "the engine talks to the model runtime; only merud does that"},
	{"internal/transcript", "merud owns the session transcripts"},
	{"internal/agent", "the agent loop and dispatch run in merud"},
	{"internal/store", "merud owns the store"},
	{"internal/retrieve", "retrieval runs in merud next to the store"},
	{"internal/memory", "merud owns memory"},
	{"internal/mcp", "merud owns the MCP clients"},
	{"internal/a2a", "merud owns the A2A clients"},
	{"internal/scheduler", "the scheduler runs in merud"},
}

// forbiddenClientSDKs lists outside packages the client must not import:
// the OpenTelemetry SDK and exporters. Setting up export is merud's job.
var forbiddenClientSDKs = []string{
	"go.opentelemetry.io/otel/sdk",
	"go.opentelemetry.io/otel/exporters",
}

// TestClientImports reads the import lines of every non-test file in
// cmd/meru and fails on a daemon-side package, the OpenTelemetry SDK, or a
// call to obs.Setup.
func TestClientImports(t *testing.T) {
	root := moduleRoot(t)
	if _, err := os.Stat(filepath.Join(root, clientDir)); err != nil {
		t.Skipf("%s doesn't exist yet", clientDir)
	}
	module := modulePath(t, root)
	obsPkg := module + "/internal/obs"

	for _, f := range moduleFiles(t, root) {
		if f.isTest || !strings.HasPrefix(f.rel, clientDir+"/") {
			continue
		}
		for _, imp := range f.file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			for _, fp := range forbiddenClientPackages {
				if full := module + "/" + fp.pkg; path == full || strings.HasPrefix(path, full+"/") {
					t.Errorf("%s: cmd/meru imports %s, but %s; keep the client thin (AGENTS.md \"Shape\")",
						f.where(imp.Pos()), path, fp.reason)
				}
			}
			if prefix, ok := matchModule(path, forbiddenClientSDKs); ok {
				t.Errorf("%s: cmd/meru imports %s (under %s); only merud sets up OpenTelemetry export",
					f.where(imp.Pos()), path, prefix)
			}
			if path == obsPkg {
				for _, pos := range selectorUses(f, importName(imp, "obs"), "Setup") {
					t.Errorf("%s: cmd/meru calls obs.Setup; only merud sets up OpenTelemetry export", pos)
				}
			}
		}
	}
}

// TestClientDependencies asks `go list -deps` for every package cmd/meru
// needs, directly or through other packages, and fails when one of them is
// daemon-side. The message shows the import chain that pulled it in.
func TestClientDependencies(t *testing.T) {
	root := moduleRoot(t)
	if _, err := os.Stat(filepath.Join(root, clientDir)); err != nil {
		t.Skipf("%s doesn't exist yet", clientDir)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command isn't on PATH; TestClientImports still runs")
	}
	module := modulePath(t, root)

	// -deps lists the package and everything it imports, however deep. The
	// -f template prints one line per package: its path, a colon, then the
	// paths it imports directly.
	cmd := exec.Command(goBin, "list", "-deps", "-f", `{{.ImportPath}}:{{join .Imports " "}}`, "./"+clientDir)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./%s: %v\n%s", clientDir, err, stderr.String())
	}

	graph := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		pkg, imports, _ := strings.Cut(sc.Text(), ":")
		graph[pkg] = strings.Fields(imports)
	}
	for _, fp := range forbiddenClientPackages {
		full := module + "/" + fp.pkg
		if chain := importChain(graph, module+"/"+clientDir, full); chain != nil {
			t.Errorf("cmd/meru depends on %s through %s, but %s; keep the client thin (AGENTS.md \"Shape\")",
				full, strings.Join(chain, " -> "), fp.reason)
		}
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
