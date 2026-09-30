// This file turns a manifest's [launch] table into the program, the
// arguments and the environment that start an installed connector. The
// supervisor (supervisor.go) calls LaunchCommand through childCmd; this
// file starts nothing itself.

package connectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrMissingField means a launch placeholder names a field that has no
// value and no default. The supervisor turns it into "<name> needs ...".
var ErrMissingField = errors.New("a field the connector needs has no value")

// LaunchCommand returns how to start the installed connector m: the
// program's absolute path, its arguments, and the variables to add to
// its short environment. fields holds the user's values by field ID,
// secrets already read from secrets.toml; a field missing from it takes
// its default. A value Meru makes up for the user, such as Obsidian's
// vault name from the folder's name, is the caller's to put in fields.
//
// How it finds the program depends on the install type:
//
//   - npm: launch.command names one of the package's programs, such as
//     "obsidian-mcp". LaunchCommand reads the package's package.json,
//     finds the script behind that name, and returns the pinned Node as
//     the program with the script as its first argument. Nothing looks
//     for node on PATH, where launchd's short PATH has none, and the
//     script's own first line, "#!/usr/bin/env node", plays no part.
//   - pip: launch.command names a program in the package's Python
//     environment, and LaunchCommand returns <pkg>/.venv/bin/<name>. uv
//     writes that script's first line as the environment's own Python,
//     by absolute path.
//   - binary: launch.command is a path under {pkg}.
//
// The environment's PATH starts with the runtime's folder, so a program
// the connector starts in turn, node or python, is the pinned one too.
//
// It fails when a placeholder names a field with no value, when the
// program isn't where the install put it, or for a container, which the
// supervisor starts with docker run instead.
func (in *Installer) LaunchCommand(m Manifest, inst Installed, fields map[string]string) (string, []string, map[string]string, error) {
	if m.Kind != KindStdio && m.Kind != KindHTTP {
		return "", nil, nil, fmt.Errorf("connector %s: a %s connector has no command to launch", m.ID, m.Kind)
	}
	fill := func(s string) (string, error) {
		return fillPlaceholders(s, m, inst.Dir, in.MeruDir, fields)
	}

	var (
		program string
		args    []string
		pathDir string
	)
	switch m.Install.Type {
	case InstallNPM:
		nodeDir := filepath.Join(in.runtimeDir(), inst.Runtime)
		program = filepath.Join(nodeDir, "bin", "node")
		script, err := npmScript(inst.Dir, m.Install.Package, m.Launch.Command)
		if err != nil {
			return "", nil, nil, fmt.Errorf("connector %s: %w", m.ID, err)
		}
		args = []string{script}
		pathDir = filepath.Join(nodeDir, "bin")
	case InstallPip:
		pathDir = filepath.Join(inst.Dir, ".venv", "bin")
		program = filepath.Join(pathDir, m.Launch.Command)
	case InstallBinary:
		p, err := fill(m.Launch.Command)
		if err != nil {
			return "", nil, nil, err
		}
		program = filepath.Clean(p)
		pathDir = inst.Dir
	default:
		return "", nil, nil, fmt.Errorf("connector %s: install type %q has no command to launch", m.ID, m.Install.Type)
	}
	// The Node program comes from the runtime's own folder; every other
	// program must sit inside the connector's install folder.
	if m.Install.Type != InstallNPM && !inside(inst.Dir, program) {
		return "", nil, nil, fmt.Errorf("connector %s: %s isn't inside its install folder %s", m.ID, program, inst.Dir)
	}
	if info, err := os.Stat(program); err != nil || info.IsDir() {
		return "", nil, nil, fmt.Errorf("connector %s: %s isn't there; install it again", m.ID, program)
	}

	for _, a := range m.Launch.Args {
		v, err := fill(a)
		if err != nil {
			return "", nil, nil, err
		}
		args = append(args, v)
	}
	env := map[string]string{
		"PATH": strings.Join([]string{pathDir, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, string(os.PathListSeparator)),
	}
	for k, v := range m.Launch.Env {
		filled, err := fill(v)
		if err != nil {
			return "", nil, nil, err
		}
		env[k] = filled
	}
	return program, args, env, nil
}

// fillPlaceholders replaces {pkg}, {meru_dir} and each {field.<id>} in s.
// A field with no value in fields takes the manifest's default; one with
// neither fails with ErrMissingField. Validate has already refused any
// other placeholder.
func fillPlaceholders(s string, m Manifest, pkgDir, meruDir string, fields map[string]string) (string, error) {
	var missing error
	out := placeholder.ReplaceAllStringFunc(s, func(match string) string {
		name := match[1 : len(match)-1]
		switch name {
		case "pkg":
			return pkgDir
		case "meru_dir":
			return meruDir
		}
		id, _ := strings.CutPrefix(name, "field.")
		if v := fields[id]; v != "" {
			return v
		}
		for _, f := range m.Fields {
			if f.ID == id && f.Default != "" {
				return f.Default
			}
		}
		missing = fmt.Errorf("connector %s: field %s: %w", m.ID, id, ErrMissingField)
		return match
	})
	return out, missing
}

// npmScript finds the script behind the program name bin in the npm
// package pkg installed under dir, from the "bin" key of its
// package.json. That key is either one path, whose program takes the
// package's name, or a map from program names to paths. It fails when
// the package has no such program, or when the path would leave the
// package's folder.
func npmScript(dir, pkg, bin string) (string, error) {
	pkgDir := filepath.Join(dir, "node_modules", filepath.FromSlash(pkg))
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json")) // #nosec G304 -- a file inside the install folder Meru made
	if err != nil {
		return "", fmt.Errorf("read %s's package.json: %w", pkg, err)
	}
	// json.RawMessage keeps "bin" undecoded, since it may be a string or
	// an object; the two Unmarshal calls below try each.
	var meta struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("read %s's package.json: %w", pkg, err)
	}
	var rel string
	var one string
	var many map[string]string
	switch {
	case json.Unmarshal(meta.Bin, &one) == nil:
		// A single program takes the package's name, without any
		// "@scope/" in front.
		if bin == path.Base(pkg) {
			rel = one
		}
	case json.Unmarshal(meta.Bin, &many) == nil:
		rel = many[bin]
	}
	if rel == "" {
		return "", fmt.Errorf("the package %s has no program called %s", pkg, bin)
	}
	script := filepath.Join(pkgDir, filepath.FromSlash(rel))
	if !inside(pkgDir, script) {
		return "", fmt.Errorf("the package %s's program %s points outside the package", pkg, bin)
	}
	return script, nil
}

// inside reports whether p lies inside the folder dir, after both are
// cleaned. It works on the names only and follows no link.
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
