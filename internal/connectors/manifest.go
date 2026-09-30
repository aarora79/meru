// This file holds the manifest: the Go types one manifests/<id>.toml file
// decodes into, Load, which reads every embedded manifest, and Validate,
// which checks one against the rules in ARCHITECTURE.md, "Connectors and
// the supervisor".

package connectors

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/loopback"
)

// manifestFiles holds manifests/*.toml, compiled into the binary. The
// //go:embed line tells the Go compiler to copy the files in at build time
// (docs/coding-notes/go-basics/embed.md), so merud needs no data files
// next to it and nobody can change a pin after the build.
//
//go:embed manifests/*.toml
var manifestFiles embed.FS

// The kinds of connector. The kind says how Meru reaches the program.
const (
	KindStdio      = "stdio"      // Meru starts the program and talks MCP over its stdin and stdout
	KindHTTP       = "http"       // Meru starts the program and talks MCP to it at a loopback URL
	KindContainer  = "container"  // Meru runs a container image and sends HTTP requests to it
	KindDependency = "dependency" // Meru only checks the program, such as Ollama, and reports on it
)

// The ways to install a connector.
const (
	InstallNPM       = "npm"       // an npm package, with Meru's own Node
	InstallPip       = "pip"       // a Python package, with Meru's own uv
	InstallBinary    = "binary"    // a download, checked against its SHA-256
	InstallContainer = "container" // a container image, pulled by its digest
	InstallNone      = "none"      // nothing to install: the user runs it
)

// The kinds of field a connector asks the user for. Every client draws the
// same form from them.
const (
	FieldText   = "text"   // one line of text
	FieldFolder = "folder" // a folder on this machine
	FieldSecret = "secret" // a password or key, kept in secrets.toml
	FieldEmail  = "email"  // an email address
	FieldChoice = "choice" // one of Choices
	FieldOAuth  = "oauth"  // a sign-in the server runs in the browser
)

// The ways an HTTP server signs in to the service behind it.
const (
	AuthNone  = "none"  // nothing to sign in to
	AuthOAuth = "oauth" // the server sends the user a sign-in link
)

// Manifest is one connector, as a manifests/<id>.toml file describes it.
// The `toml:"..."` struct tags name the key each field comes from
// (docs/coding-notes/go-basics/struct-tags.md).
type Manifest struct {
	// ID names the connector in config ([connectors.<id>]), in secrets
	// (secret:connector_<id>_<field>) and, for an MCP server, in tool
	// names (<id>.<tool>). Lower-case letters, digits and "_".
	ID string `toml:"id"`
	// Name is what the user sees, such as "Web search".
	Name string `toml:"name"`
	// Kind is one of the Kind constants.
	Kind string `toml:"kind"`
	// Required means Meru can't answer at all without it (Ollama).
	Required bool `toml:"required"`
	// DefaultOn means the connector is on until config turns it off.
	DefaultOn bool `toml:"default_on"`
	// IdleTimeout, a Go duration such as "10m", is how long a started
	// program may sit unused before Meru stops it. Empty means never.
	IdleTimeout string `toml:"idle_timeout"`

	Install Install `toml:"install"`
	Launch  Launch  `toml:"launch"`
	// Fields are what the connector asks the user, in order. The TOML
	// writes each one as a [[field]] table.
	Fields []Field `toml:"field"`
	Health Health  `toml:"health"`
	MCP    MCP     `toml:"mcp"`
}

// Install says where the connector's program comes from, pinned to one
// exact version.
type Install struct {
	// Type is one of the Install constants.
	Type string `toml:"type"`
	// Package is the npm or pip package name, with no version in it.
	Package string `toml:"package"`
	// Version is the one exact version to install, such as "2.0.1".
	// "latest", ranges and tags are refused.
	Version string `toml:"version"`
	// Image is a container image with its digest:
	// "<name>[:<tag>]@sha256:<64 hex digits>". The digest is the pin.
	Image string `toml:"image"`
	// Binaries maps a platform, such as "darwin_arm64", to its download.
	// The TOML writes each one as two keys, url_<os>_<arch> and
	// sha256_<os>_<arch>; parse gathers them here, since a struct tag
	// can't name a key that changes with the platform.
	Binaries map[string]Binary `toml:"-"`
}

// Binary is one platform's download: where it is and the SHA-256 it must
// have.
type Binary struct {
	URL    string
	SHA256 string
}

// Launch says how to start the program. Which keys apply depends on the
// kind; Validate refuses the rest.
//
// Command, Args, Env values, URL and Volumes may hold placeholders that
// merud fills in when it starts the program: {pkg} is the folder the
// connector is installed in, {meru_dir} is the Meru home (~/.meru), and
// {field.<id>} is the value the user gave for that field.
type Launch struct {
	// Command is the program to run, by absolute path once placeholders
	// are filled in. stdio and http only.
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	// Env adds environment variables for the program.
	Env map[string]string `toml:"env"`
	// URL is where Meru reaches the program, and must be loopback. http
	// and container only.
	URL string `toml:"url"`
	// Port is the port the program itself listens on: the URL's port for
	// an http server, the port inside the container for a container.
	Port int `toml:"port"`
	// Auth is AuthNone or AuthOAuth. http only.
	Auth string `toml:"auth"`
	// Volumes are container mounts, "<folder on this machine>:<folder in
	// the container>". container only.
	Volumes []string `toml:"volumes"`
}

// Field is one thing the connector asks the user for.
type Field struct {
	ID string `toml:"id"`
	// Type is one of the Field constants.
	Type  string `toml:"type"`
	Label string `toml:"label"`
	Help  string `toml:"help"`
	// Required means the connector can't start without a value.
	Required bool `toml:"required"`
	// Pattern is a regular expression, in Go's RE2 syntax, that the value
	// must match. Empty means any value.
	Pattern string `toml:"pattern"`
	// Default is the value before the user gives one. A secret has none.
	Default string `toml:"default"`
	// Choices lists what a choice field accepts.
	Choices []string `toml:"choices"`
}

// Health says how to tell that the connector works. An MCP server gets a
// cheap tool call; a container or a dependency gets an HTTP GET.
type Health struct {
	// Tool is the tool to call, with Args. stdio and http only.
	Tool string         `toml:"tool"`
	Args map[string]any `toml:"args"`
	// Expect says what a good answer holds: "nonempty", "contains:<text>"
	// or "json_key:<key>". Empty means any answer that isn't an error.
	Expect string `toml:"expect"`
	// Path is the URL path to GET, starting with "/". container and
	// dependency only.
	Path string `toml:"path"`
}

// MCP holds the tool lists for an MCP server, with the same meaning as in
// an [[mcp.servers]] entry: deny-by-default, so only Allow reaches the
// model, and Confirm and AlwaysConfirm ask first.
type MCP struct {
	Allow         []string `toml:"allow"`
	Confirm       []string `toml:"confirm"`
	AlwaysConfirm []string `toml:"always_confirm"`
}

// Platforms lists the systems a binary download may name, as
// <GOOS>_<GOARCH>: the five `make build` builds for.
var platforms = []string{"darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64", "windows_amd64"}

// Load parses and checks every manifest compiled into the binary, and
// returns them sorted by file name. It fails when one doesn't parse,
// breaks a rule, has a file name other than <id>.toml, or shares its ID
// with another; the error names every problem it found.
func Load() ([]Manifest, error) {
	return loadFS(manifestFiles, "manifests")
}

// loadFS is Load over any file system, so tests can load made-up files.
// fs.FS is the standard library's interface for a read-only tree of
// files; embed.FS is one.
func loadFS(fsys fs.FS, dir string) ([]Manifest, error) {
	names, err := fs.Glob(fsys, dir+"/*.toml")
	if err != nil {
		return nil, fmt.Errorf("list manifests: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no manifests in %s", dir)
	}
	var out []Manifest
	var errs []error
	seen := map[string]bool{}
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", name, err))
			continue
		}
		m, err := Parse(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if err := Validate(m); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		// The file name must match the ID, so a reader finds a
		// connector's file from its name.
		if want := m.ID + ".toml"; path.Base(name) != want {
			errs = append(errs, fmt.Errorf("%s: the connector's id is %q, so the file must be called %s", name, m.ID, want))
		}
		if seen[m.ID] {
			errs = append(errs, fmt.Errorf("%s: the id %q is used twice", name, m.ID))
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	// errors.Join returns nil when errs is empty.
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// Parse decodes one manifest from TOML. It fails on a TOML syntax error
// and on a key the Manifest type doesn't have, which is most often a typo.
// It doesn't check the values; Validate does.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	md, err := toml.Decode(string(data), &m)
	if err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}

	// The per-platform download keys don't fit a struct, so read the
	// [install] table a second time as a map and pick them out. A map
	// from string to any holds whatever each key holds.
	var raw struct {
		Install map[string]any `toml:"install"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	bins, err := binaries(raw.Install)
	if err != nil {
		return Manifest{}, err
	}
	m.Install.Binaries = bins

	var unknown []string
	for _, k := range md.Undecoded() {
		// install.url_* and install.sha256_* were read just above.
		if len(k) == 2 && k[0] == "install" && (strings.HasPrefix(k[1], "url_") || strings.HasPrefix(k[1], "sha256_")) {
			continue
		}
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		return Manifest{}, fmt.Errorf("unknown keys: %s", strings.Join(unknown, ", "))
	}
	return m, nil
}

// binaries gathers the url_<platform> and sha256_<platform> keys of an
// [install] table into one Binary per platform. It fails when one of the
// values isn't a string. It returns nil when there are none.
func binaries(install map[string]any) (map[string]Binary, error) {
	var out map[string]Binary
	for k, v := range install {
		platform, isURL := strings.CutPrefix(k, "url_")
		if !isURL {
			var isSum bool
			if platform, isSum = strings.CutPrefix(k, "sha256_"); !isSum {
				continue
			}
		}
		// A type assertion, v.(string), asks whether the value in an
		// `any` is a string; ok is false when it isn't.
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("install.%s must be a string", k)
		}
		if out == nil {
			out = map[string]Binary{}
		}
		b := out[platform]
		if isURL {
			b.URL = s
		} else {
			b.SHA256 = s
		}
		out[platform] = b
	}
	return out, nil
}

// idPattern is what a connector ID and a field ID may hold. Both end up in
// a secret's name, secret:connector_<id>_<field>, and in config keys, so
// they keep to characters that need no quotes.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// The exact-version rules, one per install type. Each accepts one release
// and nothing that could match several: no "latest", no range such as
// "^2" or ">=1.0", no wildcard such as "2.x", no bare major such as "2".
var (
	// npmVersion is a full semantic version, with an optional pre-release
	// or build part: "2.0.1", "2.0.0-beta.4".
	npmVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	// pipVersion is a Python release number of two or more parts, with
	// the optional pre-, post- and dev-release parts: "1.30.0", "1.0rc1".
	pipVersion = regexp.MustCompile(`^\d+(\.\d+)+((a|b|rc)\d+)?(\.post\d+)?(\.dev\d+)?$`)
	// binaryVersion is a release number of two or more parts, with an
	// optional "v" and pre-release part: "v0.12.4", "1.2.3-rc.1".
	binaryVersion = regexp.MustCompile(`^v?\d+(\.\d+)+(-[0-9A-Za-z.-]+)?$`)
)

// npmName and pipName are what a package name may hold. Neither allows a
// version inside the name, such as "obsidian-mcp@2", which would slip past
// the version rule.
var (
	npmName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	pipName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
)

// imageDigest matches a container image pinned by its digest. The part
// before "@" is the name and an optional tag; checkImage checks the tag.
var imageDigest = regexp.MustCompile(`^([a-z0-9][a-z0-9._/:-]*)@sha256:[0-9a-f]{64}$`)

// sha256Hex is a SHA-256 written as 64 lower-case hex digits.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// placeholder finds each {name} in a launch value.
var placeholder = regexp.MustCompile(`\{([^{}]*)\}`)

// Validate checks one manifest and returns every problem it finds, joined
// into one error, or nil when it follows every rule. The rules:
//
//   - id and name are set, and id is lower-case letters, digits and "_";
//   - kind, install type, field types and auth are ones Meru knows, and
//     the install type fits the kind;
//   - a version is exact, a container image has a digest, and a binary
//     download has a SHA-256 for each platform it names;
//   - field IDs are unique, a secret has no default, a pattern compiles;
//   - an http server's URL is loopback, and its port matches;
//   - a launch placeholder names a field the manifest has, and a secret
//     never goes on the command line, where any user can read it;
//   - the health check and the tool lists fit the kind.
func Validate(m Manifest) error {
	var errs []error
	// add records one problem. It is a closure: a function value that can
	// read and change errs from the surrounding function.
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	switch {
	case m.ID == "":
		add("id is missing")
	case !idPattern.MatchString(m.ID):
		add("id %q may hold only lower-case letters, digits and \"_\", starting with a letter", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		add("name is missing")
	}
	if m.IdleTimeout != "" {
		if d, err := time.ParseDuration(m.IdleTimeout); err != nil || d <= 0 {
			add("idle_timeout %q must be a positive duration such as \"10m\"", m.IdleTimeout)
		}
	}

	switch m.Kind {
	case KindStdio, KindHTTP, KindContainer, KindDependency:
	default:
		add("kind %q is unknown; use stdio, http, container or dependency", m.Kind)
	}

	errs = append(errs, checkInstall(m.Kind, m.Install)...)
	fields, fieldErrs := checkFields(m.Fields)
	errs = append(errs, fieldErrs...)
	errs = append(errs, checkLaunch(m.Kind, m.Launch, fields)...)
	errs = append(errs, checkHealth(m.Kind, m.Health)...)
	errs = append(errs, checkMCP(m.Kind, m.MCP)...)

	if err := errors.Join(errs...); err != nil {
		name := m.ID
		if name == "" {
			name = "(no id)"
		}
		return fmt.Errorf("connector %s: %w", name, err)
	}
	return nil
}

// kindInstalls says which install types fit each kind. An MCP server comes
// from a package or a download; a container from an image; a dependency
// is the user's own program, so Meru installs nothing.
var kindInstalls = map[string][]string{
	KindStdio:      {InstallNPM, InstallPip, InstallBinary},
	KindHTTP:       {InstallNPM, InstallPip, InstallBinary},
	KindContainer:  {InstallContainer},
	KindDependency: {InstallNone},
}

// checkInstall checks the [install] table against its type and the
// connector's kind, and returns one error per problem.
func checkInstall(kind string, in Install) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	switch in.Type {
	case InstallNPM, InstallPip, InstallBinary, InstallContainer, InstallNone:
		if fit, known := kindInstalls[kind]; known && !slices.Contains(fit, in.Type) {
			add("install.type %q doesn't fit kind %q; use %s", in.Type, kind, strings.Join(fit, " or "))
		}
	default:
		add("install.type %q is unknown; use npm, pip, binary, container or none", in.Type)
		return errs
	}

	// Each type uses some keys and must leave the rest empty, so a
	// manifest can't carry a second, unchecked pin.
	usesPackage := in.Type == InstallNPM || in.Type == InstallPip
	usesVersion := usesPackage || in.Type == InstallBinary
	if !usesPackage && in.Package != "" {
		add("install.package is for npm and pip; take it out for type %q", in.Type)
	}
	if !usesVersion && in.Version != "" {
		add("install.version is for npm, pip and binary; take it out for type %q", in.Type)
	}
	if in.Type != InstallContainer && in.Image != "" {
		add("install.image is for type container; take it out for type %q", in.Type)
	}
	if in.Type != InstallBinary && len(in.Binaries) > 0 {
		add("install.url_* and install.sha256_* are for type binary; take them out for type %q", in.Type)
	}

	switch in.Type {
	case InstallNPM:
		if !npmName.MatchString(in.Package) {
			add("install.package %q isn't an npm package name; give the name alone, with no version", in.Package)
		}
		if !npmVersion.MatchString(in.Version) {
			add("install.version %q isn't one exact version such as \"2.0.1\"; \"latest\", ranges and tags are refused", in.Version)
		}
	case InstallPip:
		if !pipName.MatchString(in.Package) {
			add("install.package %q isn't a Python package name; give the name alone, with no version", in.Package)
		}
		if !pipVersion.MatchString(in.Version) {
			add("install.version %q isn't one exact version such as \"1.30.0\"; \"latest\", ranges and tags are refused", in.Version)
		}
	case InstallBinary:
		if !binaryVersion.MatchString(in.Version) {
			add("install.version %q isn't one exact version such as \"0.12.4\"; \"latest\", ranges and tags are refused", in.Version)
		}
		errs = append(errs, checkBinaries(in.Binaries)...)
	case InstallContainer:
		if err := checkImage(in.Image); err != nil {
			add("install.image: %w", err)
		}
	}
	return errs
}

// checkBinaries checks the downloads of a binary install: at least one,
// each on a platform Meru builds for, each an https URL with a SHA-256.
func checkBinaries(bins map[string]Binary) []error {
	if len(bins) == 0 {
		return []error{errors.New("a binary install needs url_<os>_<arch> and sha256_<os>_<arch> for each platform, such as url_darwin_arm64")}
	}
	var errs []error
	// Walk the platforms in a fixed order, so the errors come out in the
	// same order each run; a map's order changes from run to run.
	keys := make([]string, 0, len(bins))
	for k := range bins {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, p := range keys {
		b := bins[p]
		if !slices.Contains(platforms, p) {
			errs = append(errs, fmt.Errorf("install: platform %q is unknown; use one of %s", p, strings.Join(platforms, ", ")))
			continue
		}
		if u, err := url.Parse(b.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("install.url_%s %q must be an https URL", p, b.URL))
		}
		if !sha256Hex.MatchString(b.SHA256) {
			errs = append(errs, fmt.Errorf("install.sha256_%s must be the file's SHA-256, 64 lower-case hex digits", p))
		}
	}
	return errs
}

// checkImage checks a container image reference: it must end in
// "@sha256:<64 hex digits>", and a tag in front of the digest, if any,
// must not be "latest", which would mislead a reader about what runs.
func checkImage(image string) error {
	sub := imageDigest.FindStringSubmatch(image)
	if sub == nil {
		return fmt.Errorf("%q must be pinned by digest, as <name>[:<tag>]@sha256:<64 hex digits>", image)
	}
	name := sub[1]
	// A tag follows the last ":" after the last "/"; a ":" before that
	// "/" belongs to a registry port, as in "localhost:5000/x".
	last := name[strings.LastIndex(name, "/")+1:]
	if _, tag, ok := strings.Cut(last, ":"); ok && tag == "latest" {
		return fmt.Errorf("%q names the tag latest; name the release's own tag, or none, in front of the digest", image)
	}
	return nil
}

// checkFields checks the [[field]] tables and returns the fields by ID,
// for the launch check, and one error per problem.
func checkFields(fields []Field) (map[string]Field, []error) {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	byID := map[string]Field{}
	for i, f := range fields {
		where := fmt.Sprintf("field %d", i+1)
		switch {
		case f.ID == "":
			add("%s has no id", where)
		case !idPattern.MatchString(f.ID):
			add("%s: id %q may hold only lower-case letters, digits and \"_\", starting with a letter", where, f.ID)
		default:
			where = "field " + f.ID
			if _, dup := byID[f.ID]; dup {
				add("the field id %q is used twice", f.ID)
			}
			byID[f.ID] = f
		}
		switch f.Type {
		case FieldText, FieldFolder, FieldEmail, FieldOAuth:
		case FieldSecret:
			// A default secret would sit in the binary for anyone to read,
			// and every user would share it.
			if f.Default != "" {
				add("%s is a secret and can't have a default", where)
			}
		case FieldChoice:
			if len(f.Choices) == 0 {
				add("%s is a choice and needs choices", where)
			} else if f.Default != "" && !slices.Contains(f.Choices, f.Default) {
				add("%s: the default %q isn't one of its choices", where, f.Default)
			}
		default:
			add("%s: type %q is unknown; use text, folder, secret, email, choice or oauth", where, f.Type)
		}
		if f.Type != FieldChoice && len(f.Choices) > 0 {
			add("%s: choices are for a choice field", where)
		}
		if strings.TrimSpace(f.Label) == "" {
			add("%s has no label", where)
		}
		if f.Pattern != "" {
			re, err := regexp.Compile(f.Pattern)
			switch {
			case err != nil:
				add("%s: pattern %q doesn't compile: %w", where, f.Pattern, err)
			case f.Default != "" && !re.MatchString(f.Default):
				add("%s: the default %q doesn't match its pattern", where, f.Default)
			}
		}
	}
	return byID, errs
}

// checkLaunch checks the [launch] table for the connector's kind, and each
// placeholder in it against fields.
func checkLaunch(kind string, l Launch, fields map[string]Field) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	// Which keys each kind takes. A key the kind doesn't use is an error,
	// so it can't sit in the file looking as if it did something.
	takes := map[string]bool{}
	switch kind {
	case KindStdio:
		takes = map[string]bool{"command": true, "args": true, "env": true}
	case KindHTTP:
		takes = map[string]bool{"command": true, "args": true, "env": true, "url": true, "port": true, "auth": true}
	case KindContainer:
		takes = map[string]bool{"env": true, "url": true, "port": true, "volumes": true}
	}
	set := map[string]bool{
		"command": l.Command != "", "args": len(l.Args) > 0, "env": len(l.Env) > 0, "url": l.URL != "",
		"port": l.Port != 0, "auth": l.Auth != "", "volumes": len(l.Volumes) > 0,
	}
	for _, key := range []string{"command", "args", "env", "url", "port", "auth", "volumes"} {
		if set[key] && !takes[key] {
			add("launch.%s isn't used by kind %q", key, kind)
		}
	}

	if (kind == KindStdio || kind == KindHTTP) && l.Command == "" {
		add("launch.command is missing; a %s connector starts a program", kind)
	}
	if kind == KindHTTP || kind == KindContainer {
		if l.Port < 1 || l.Port > 65535 {
			add("launch.port %d must be between 1 and 65535", l.Port)
		}
		switch err := loopback.CheckURL(l.URL); {
		case l.URL == "":
			add("launch.url is missing; Meru needs a loopback URL to reach a %s connector", kind)
		case err != nil:
			add("launch.url: %w", err)
		case kind == KindHTTP && urlPort(l.URL) != l.Port:
			// An http server listens where Meru connects. A container's
			// port is inside the container, and Docker maps the URL's
			// port to it, so the two may differ.
			add("launch.url %q must use launch.port %d", l.URL, l.Port)
		}
	}
	if kind == KindHTTP && l.Auth != AuthNone && l.Auth != AuthOAuth {
		add("launch.auth %q is unknown; use none or oauth", l.Auth)
	}

	// check looks at each placeholder in one launch value. onCommandLine
	// is true for the command and its arguments, which any user on the
	// machine can read in the process list.
	check := func(where, value string, onCommandLine bool) {
		for _, sub := range placeholder.FindAllStringSubmatch(value, -1) {
			name := sub[1]
			id, isField := strings.CutPrefix(name, "field.")
			switch {
			case name == "pkg" || name == "meru_dir":
			case !isField:
				add("%s: placeholder {%s} is unknown; use {pkg}, {meru_dir} or {field.<id>}", where, name)
			case fields[id].ID == "":
				add("%s: placeholder {%s} names no field; the fields are %s", where, name, fieldList(fields))
			case onCommandLine && fields[id].Type == FieldSecret:
				add("%s: the secret {%s} can't go on the command line, where other users can read it; pass it in launch.env", where, name)
			}
		}
	}
	check("launch.command", l.Command, true)
	for i, a := range l.Args {
		check(fmt.Sprintf("launch.args[%d]", i), a, true)
	}
	for k, v := range l.Env {
		check("launch.env."+k, v, false)
	}
	check("launch.url", l.URL, false)
	for i, v := range l.Volumes {
		check(fmt.Sprintf("launch.volumes[%d]", i), v, false)
	}
	return errs
}

// urlPort returns the port in a URL, or 0 when it has none or it isn't a
// number. The caller has already checked that the URL parses.
func urlPort(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0
	}
	return n
}

// fieldList returns the field IDs, sorted and joined, for an error
// message, or "none" when there are none.
func fieldList(fields map[string]Field) string {
	if len(fields) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(fields))
	for id := range fields {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return strings.Join(ids, ", ")
}

// checkHealth checks the [health] table for the connector's kind: an MCP
// server is checked with a tool call, the rest with an HTTP GET.
func checkHealth(kind string, h Health) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	switch kind {
	case KindStdio, KindHTTP:
		if h.Tool == "" {
			add("health.tool is missing; name a cheap tool the check can call")
		}
		if h.Path != "" {
			add("health.path is for container and dependency; a %s connector is checked with health.tool", kind)
		}
	case KindContainer, KindDependency:
		if !strings.HasPrefix(h.Path, "/") {
			add("health.path %q must be a URL path starting with \"/\"", h.Path)
		}
		if h.Tool != "" || len(h.Args) > 0 {
			add("health.tool and health.args are for stdio and http; a %s connector is checked with health.path", kind)
		}
	}
	switch rest, _ := strings.CutPrefix(h.Expect, "contains:"); {
	case h.Expect == "" || h.Expect == "nonempty":
	case strings.HasPrefix(h.Expect, "contains:") && rest != "":
	case strings.HasPrefix(h.Expect, "json_key:") && len(h.Expect) > len("json_key:"):
	default:
		add("health.expect %q is unknown; use nonempty, contains:<text> or json_key:<key>", h.Expect)
	}
	return errs
}

// checkMCP checks the tool lists: only an MCP server has them, no entry is
// a wildcard, and every tool that asks first is also allowed.
func checkMCP(kind string, m MCP) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	if kind != KindStdio && kind != KindHTTP {
		if len(m.Allow)+len(m.Confirm)+len(m.AlwaysConfirm) > 0 {
			add("[mcp] is for stdio and http connectors; a %s connector offers no MCP tools", kind)
		}
		return errs
	}
	lists := []struct {
		key   string
		names []string
	}{{"allow", m.Allow}, {"confirm", m.Confirm}, {"always_confirm", m.AlwaysConfirm}}
	for _, l := range lists {
		for _, name := range l.names {
			// A wildcard would admit tools a later release adds, which
			// nobody has read (ARCHITECTURE.md, "MCP").
			if name == "" || strings.ContainsAny(name, "*?[") {
				add("mcp.%s: %q must name one tool; wildcards are refused", l.key, name)
			}
			if l.key != "allow" && !slices.Contains(m.Allow, name) {
				add("mcp.%s: %q isn't in mcp.allow", l.key, name)
			}
		}
	}
	return errs
}
