// This file holds Command and Param, and New, which turns the
// [[commands]] entries of config.toml into checked Commands when merud
// starts. It also holds the placeholder syntax that New and Render share.

package commands

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// The parameter types a [[commands]] entry may declare.
const (
	TypeString = "string"
	TypeInt    = "int"
	TypeEnum   = "enum"
	TypePath   = "path"
)

// Timeouts. 30 seconds covers a git log or a search; five minutes is the
// cap because a turn that waits longer has lost its user.
const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 300 * time.Second
)

// defaultMaxLen caps a string parameter, in bytes, when the entry sets no
// max_len. 4 KiB holds any search term or commit message a model would send.
const defaultMaxLen = 4096

// maxNameLen caps a command's name, as for an MCP server's name.
const maxNameLen = 64

// interpreters lists programs that run code given as text: shells, script
// languages, env (which runs any program) and macOS's osascript. New
// refuses an argv[0] whose base name is one of these, compared without case,
// without ".exe" and without a version such as the "3.12" in "python3.12".
// A string parameter passed to "bash -c" is a shell again, and the whole
// point of a declared command is that no shell exists. The user declared
// the command, so this guards against a slip, not an attacker: a script of
// your own, run by its path, is fine.
var interpreters = []string{
	"sh", "bash", "zsh", "dash", "ksh", "fish",
	"python", "perl", "ruby", "node",
	"env",
	"pwsh", "powershell", "cmd",
	"osascript",
}

// Command is one program the user declared in config.toml. Meru never
// builds a command from the model's text; it fills declared parameters into
// a declared argv. New fills every field, "~" already expanded.
type Command struct {
	Name        string
	Description string
	// Argv is the program and its arguments. Elements may hold {param}
	// placeholders, and "{{" and "}}" for literal braces.
	Argv []string
	// Params holds each parameter by name.
	Params map[string]Param
	// Cwd is the absolute folder the program starts in.
	Cwd     string
	Timeout time.Duration
	// Confirm makes each call ask the user first.
	Confirm bool
	// EnvAllow names the variables the program gets from merud's
	// environment besides PATH, HOME and LANG.
	EnvAllow []string
}

// Param is one declared parameter: its type and the limits on its values.
type Param struct {
	Type        string // TypeString, TypeInt, TypeEnum or TypePath
	Description string
	// Under is the folder a path must resolve inside: absolute, with its
	// own symbolic links resolved, so the check after resolving a value
	// compares like with like.
	Under string
	// Min and Max bound an int; nil means no bound.
	Min, Max *int64
	// Values lists what an enum accepts.
	Values []string
	// MaxLen caps a string, in bytes.
	MaxLen int
}

// ToolName returns the name the model sees, "cmd.<name>".
func (c Command) ToolName() string { return "cmd." + c.Name }

// New checks the [[commands]] entries and returns them as a Set, in config
// order. It reports every problem at once, each naming its command, so the
// user can fix them in one pass; merud then refuses to start. It fails on a
// name that is empty, badly formed or used twice; an empty argv; a
// placeholder in argv[0] or an interpreter there; a brace that isn't part
// of a placeholder or "{{" or "}}"; a placeholder with no parameter, or a
// parameter no placeholder uses; an unknown type, or a key the type
// doesn't take; a path parameter with no under, or an under or cwd that
// isn't an existing folder; an enum with no values; a min above max; a bad
// env_allowlist name; and a timeout that doesn't parse or is over 300s.
//
// A program that isn't on PATH is only a warning to log, not an error: it
// may be installed later, and the call then fails with a clear message.
func New(decls []config.Command, log *slog.Logger) (*Set, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("commands: find home directory: %w", err)
	}
	var errs []error
	seen := map[string]bool{}
	cmds := make([]Command, 0, len(decls))
	for _, d := range decls {
		c, cmdErrs := check(d, home)
		if d.Name != "" && seen[d.Name] {
			cmdErrs = append(cmdErrs, errors.New("the name is used by an earlier [[commands]] entry"))
		}
		seen[d.Name] = true
		for _, e := range cmdErrs {
			errs = append(errs, fmt.Errorf("command %q: %w", d.Name, e))
		}
		if len(cmdErrs) == 0 {
			cmds = append(cmds, c)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	for _, c := range cmds {
		// LookPath finds the program the way Run will: a bare name on
		// PATH, or a path as written.
		if _, err := exec.LookPath(c.Argv[0]); err != nil {
			log.Warn("a declared command's program isn't there; calls to it will fail until it is",
				"command", c.Name, "program", c.Argv[0], "err", err)
		}
	}
	return newSet(cmds), nil
}

// check turns one entry into a Command, expanding "~" against home. It
// returns every problem it finds; the Command is valid only when there
// are none.
func check(d config.Command, home string) (Command, []error) {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if err := checkName(d.Name); err != nil {
		errs = append(errs, err)
	}
	c := Command{
		Name:        d.Name,
		Description: strings.TrimSpace(d.Description),
		Confirm:     d.Confirm,
		EnvAllow:    slices.Clone(d.EnvAllowlist),
		Params:      map[string]Param{},
		Timeout:     defaultTimeout,
	}

	// Argv: expand "~", then find every placeholder.
	used := map[string]bool{}
	if len(d.Argv) == 0 || d.Argv[0] == "" {
		add("argv is empty; name the program first, such as argv = [\"git\", \"status\"]")
	}
	for i, a := range d.Argv {
		a = expandHome(a, home)
		c.Argv = append(c.Argv, a)
		segs, err := parse(a)
		if err != nil {
			add("argv[%d]: %w", i, err)
			continue
		}
		for _, s := range segs {
			if s.param == "" {
				continue
			}
			if i == 0 {
				add("argv[0] %q holds a placeholder; the program must be fixed, and only its arguments may vary", a)
			}
			used[s.param] = true
		}
	}
	if len(c.Argv) > 0 && isInterpreter(c.Argv[0]) {
		add("argv[0] %q runs code given as text, so a parameter could reach a shell; "+
			"declare the program itself, or put the script in a file and name its path", c.Argv[0])
	}

	// Params.
	for name, p := range d.Params {
		param, perrs := checkParam(name, p, home)
		errs = append(errs, perrs...)
		c.Params[name] = param
		if !used[name] {
			add("params.%s appears in no argv element; use it as {%s} or remove it", name, name)
		}
	}
	for name := range used {
		if _, ok := d.Params[name]; !ok {
			add("argv uses {%s}, but params declares no %q; add [commands.params.%s]", name, name, name)
		}
	}

	// Cwd.
	c.Cwd = home
	if d.Cwd != "" {
		cwd := expandHome(d.Cwd, home)
		if err := isFolder(cwd); err != nil {
			add("cwd: %w", err)
		}
		c.Cwd = cwd
	}

	// Timeout.
	if d.Timeout != "" {
		t, err := time.ParseDuration(d.Timeout)
		switch {
		case err != nil || t <= 0:
			add("timeout %q must be a positive duration such as \"30s\"", d.Timeout)
		case t > maxTimeout:
			add("timeout %s is over the %s cap", t, maxTimeout)
		default:
			c.Timeout = t
		}
	}

	for _, e := range d.EnvAllowlist {
		if e == "" || strings.ContainsAny(e, "=\x00") {
			add("env_allowlist: %q is not a variable name", e)
		}
	}
	return c, errs
}

// checkParam checks one parameter declaration and returns it as a Param,
// with under expanded and resolved. The errors name the parameter.
func checkParam(name string, p config.CommandParam, home string) (Param, []error) {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("params.%s: "+format, append([]any{name}, args...)...))
	}
	if !validParamName(name) {
		add("the name must start with a letter or '_' and hold only letters, digits and '_'")
	}
	out := Param{
		Type:        p.Type,
		Description: strings.TrimSpace(p.Description),
		Min:         p.Min,
		Max:         p.Max,
		Values:      slices.Clone(p.Values),
		MaxLen:      p.MaxLen,
	}
	// Each key belongs to one type. A key on the wrong type is most likely
	// a mistake, and ignoring it would drop a limit the user meant to set.
	if p.Under != "" && p.Type != TypePath {
		add("under applies only to type \"path\"")
	}
	if (p.Min != nil || p.Max != nil) && p.Type != TypeInt {
		add("min and max apply only to type \"int\"")
	}
	if len(p.Values) > 0 && p.Type != TypeEnum {
		add("values applies only to type \"enum\"")
	}
	if p.MaxLen != 0 && p.Type != TypeString {
		add("max_len applies only to type \"string\"")
	}

	switch p.Type {
	case TypeString:
		if p.MaxLen < 0 {
			add("max_len is %d; it must be 1 or more", p.MaxLen)
		}
		if out.MaxLen <= 0 {
			out.MaxLen = defaultMaxLen
		}
	case TypeInt:
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			add("min %d is above max %d", *p.Min, *p.Max)
		}
	case TypeEnum:
		if len(p.Values) == 0 {
			add("an enum needs values, such as values = [\"short\", \"long\"]")
		}
		for _, v := range p.Values {
			if v == "" || strings.ContainsRune(v, 0) {
				add("values: %q can't be a value", v)
			}
		}
	case TypePath:
		if p.Under == "" {
			add("a path needs under, the folder it must stay inside, such as under = \"~/repos\"")
			break
		}
		under := expandHome(p.Under, home)
		if err := isFolder(under); err != nil {
			add("under: %w", err)
			break
		}
		// Resolve under's own links once, here: a value is resolved
		// before the check, so under must be too. On macOS /tmp is a link
		// to /private/tmp, and an unresolved under would refuse every
		// path in it.
		real, err := filepath.EvalSymlinks(under)
		if err != nil {
			add("under: %w", err)
			break
		}
		out.Under = real
	default:
		add("type %q is unknown; use \"string\", \"int\", \"enum\" or \"path\"", p.Type)
	}
	return out, errs
}

// checkName fails unless name is 1 to 64 letters, digits, '-' and '_', the
// rule MCP server names follow, so "cmd.<name>" is a valid tool name.
func checkName(name string) error {
	if name == "" || len(name) > maxNameLen {
		return fmt.Errorf("name must be 1 to %d characters", maxNameLen)
	}
	for _, r := range name {
		if !isAlnum(r) && r != '-' && r != '_' {
			return errors.New("name: use only letters, digits, '-' and '_'")
		}
	}
	return nil
}

// validParamName reports whether name can name a parameter: a letter or
// '_', then letters, digits and '_'.
func validParamName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || (isAlnum(r) && (i > 0 || r > '9')) {
			continue
		}
		return false
	}
	return true
}

// isAlnum reports whether r is an ASCII letter or digit.
func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// isInterpreter reports whether program names one of the interpreters, by
// its base name, without case, ".exe" or a trailing version.
func isInterpreter(program string) bool {
	// Split at both separators, so "C:\Windows\cmd.exe" gives "cmd.exe" on
	// every OS.
	base := program
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")
	// "python3.12" and "perl5" are python and perl.
	base = strings.TrimRight(base, "0123456789.")
	return slices.Contains(interpreters, base)
}

// isFolder fails unless path is absolute and names an existing folder.
func isFolder(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q must be an absolute path or start with \"~/\"", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a folder", path)
	}
	return nil
}

// expandHome replaces a leading "~" in s with home: "~" alone, or "~/" or
// "~\" followed by more. Other strings, "~user" included, come back as they
// are.
func expandHome(s, home string) string {
	switch {
	case s == "~":
		return home
	case strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`):
		return filepath.Join(home, s[2:])
	}
	return s
}

// segment is one piece of an argv element: literal text, or a placeholder
// naming a parameter. Exactly one of the two is set.
type segment struct {
	text  string
	param string
}

// parse splits one argv element into segments. "{name}" is a placeholder;
// "{{" and "}}" are a literal "{" and "}". Any other brace is an error, so
// a typo such as "{repo" can't reach a program as literal text. The escape
// rule is the one Python's str.format uses.
func parse(elem string) ([]segment, error) {
	var segs []segment
	var lit strings.Builder
	for i := 0; i < len(elem); i++ {
		switch ch := elem[i]; {
		case ch == '{' && i+1 < len(elem) && elem[i+1] == '{':
			lit.WriteByte('{')
			i++
		case ch == '}' && i+1 < len(elem) && elem[i+1] == '}':
			lit.WriteByte('}')
			i++
		case ch == '{':
			end := strings.IndexByte(elem[i+1:], '}')
			if end < 0 {
				return nil, fmt.Errorf("%q has a \"{\" with no closing \"}\"; write \"{{\" for a literal brace", elem)
			}
			name := elem[i+1 : i+1+end]
			if !validParamName(name) {
				return nil, fmt.Errorf("%q: {%s} is not a placeholder; a name holds letters, digits and '_', "+
					"and \"{{\" and \"}}\" write literal braces", elem, name)
			}
			if lit.Len() > 0 {
				segs = append(segs, segment{text: lit.String()})
				lit.Reset()
			}
			segs = append(segs, segment{param: name})
			i += end + 1
		case ch == '}':
			return nil, fmt.Errorf("%q has a \"}\" that closes nothing; write \"}}\" for a literal brace", elem)
		default:
			lit.WriteByte(ch)
		}
	}
	if lit.Len() > 0 || len(segs) == 0 {
		segs = append(segs, segment{text: lit.String()})
	}
	return segs, nil
}
