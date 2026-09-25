// This file holds the Registry: loading every skill folder under a directory,
// checking each SKILL.md, and answering List, Get, Has and Body.

package skills

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Limits on one skill. A SKILL.md larger than maxFileBytes is almost
// certainly a mistake (the explainer skill, one of the longest, is about
// 21 KB). The name and description limits match the public SKILL.md format,
// and they keep the per-skill line in the system prompt short.
const (
	maxFileBytes        = 256 << 10 // 256 KiB
	maxNameBytes        = 64
	maxDescriptionBytes = 1024
)

// fileName is the one file every skill folder must hold.
const fileName = "SKILL.md"

// namePattern matches a skill name: lowercase letters and digits in words
// joined by single hyphens, such as "writing" or "meeting-notes". A name
// that passes can't hold a path separator, "..", spaces or capitals, so it is
// safe as a folder name on every platform and reads the same everywhere.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ErrNotFound reports a skill name the registry doesn't hold. Callers check
// for it with errors.Is.
var ErrNotFound = errors.New("skill not found")

// Skill is one loaded skill: its header fields and where its file lives.
// The body stays on disk until Body asks for it.
type Skill struct {
	Name        string
	Description string
	// Path is the SKILL.md file.
	Path string
	// AllowedTools lists the tools the skill's instructions use, from the
	// optional "allowed-tools" key, such as ["web_search", "web_fetch"].
	// Claude's skills use the same key. It grants nothing: the agent offers
	// a named tool only when config already allows it (ARCHITECTURE.md,
	// "Skills"). nil when the key is missing.
	AllowedTools []string
	// Extra holds every other frontmatter key, such as "license" or
	// "metadata.author". Meru doesn't act on them; they are kept so
	// `meru skills show` can print them.
	Extra map[string]string
}

// Summary is the part of a skill the system prompt carries.
type Summary struct {
	Name        string
	Description string
}

// Registry holds the skills found by one call to Load. It never changes after
// Load returns, so many goroutines can read it at once with no lock. To pick
// up new or edited skills, call Load again and swap in the new Registry.
type Registry struct {
	dir      string
	skills   map[string]Skill
	warnings []error
}

// Load reads every skill folder directly under dir (usually ~/.meru/skills),
// except the folders disabled names ([skills] disabled from config). It
// skips those without a warning: the user asked for it.
//
// A bad skill doesn't stop the load: Load skips it and records why, and
// Warnings returns those reasons. A folder is bad when it has no SKILL.md, the
// file is too large, the frontmatter doesn't parse, the name is unsafe or
// differs from the folder name, the description is empty or too long, or the
// body is empty. Folders whose names start with "." are skipped without a
// warning; editors and InstallBuiltins leave temporary files there.
//
// Load returns an empty registry when dir doesn't exist yet, and fails only
// when dir exists but can't be read.
func Load(dir string, disabled []string) (*Registry, error) {
	// &Registry{...} builds a Registry and returns a pointer to it, so every
	// caller shares the one value instead of copying it.
	r := &Registry{dir: dir, skills: make(map[string]Skill)}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills directory %s: %w", dir, err)
	}
	// os.ReadDir returns entries sorted by name, so warnings come out in a
	// stable order.
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || slices.Contains(disabled, name) {
			continue
		}
		folder := filepath.Join(dir, name)
		// os.Stat follows symbolic links, so a skill folder linked in from
		// another agent's directory counts. A plain file at this level is not
		// a skill and not worth a warning.
		info, err := os.Stat(folder)
		if err != nil {
			r.warnings = append(r.warnings, fmt.Errorf("skill %s: %w", name, err))
			continue
		}
		if !info.IsDir() {
			continue
		}
		s, err := loadSkill(folder, name)
		if err != nil {
			r.warnings = append(r.warnings, fmt.Errorf("skill %s: %w", name, err))
			continue
		}
		r.skills[s.Name] = s
	}
	return r, nil
}

// loadSkill reads and checks <folder>/SKILL.md for the skill named folderName.
// It returns the skill with its header fields, or the first problem found.
func loadSkill(folder, folderName string) (Skill, error) {
	path := filepath.Join(folder, fileName)
	fields, body, err := readSkillFile(path)
	if err != nil {
		return Skill{}, err
	}
	name := fields["name"]
	description := strings.TrimSpace(fields["description"])
	switch {
	case name == "":
		return Skill{}, errors.New("frontmatter has no name")
	case len(name) > maxNameBytes:
		return Skill{}, fmt.Errorf("name is %d bytes, over the %d-byte limit", len(name), maxNameBytes)
	case !namePattern.MatchString(name):
		return Skill{}, fmt.Errorf("name %q must be lowercase letters and digits joined by hyphens", name)
	case name != folderName:
		return Skill{}, fmt.Errorf("name %q doesn't match its folder %q", name, folderName)
	case description == "":
		return Skill{}, errors.New("frontmatter has no description")
	case len(description) > maxDescriptionBytes:
		return Skill{}, fmt.Errorf("description is %d bytes, over the %d-byte limit", len(description), maxDescriptionBytes)
	case strings.TrimSpace(body) == "":
		return Skill{}, errors.New("the body after the frontmatter is empty")
	}

	tools := toolList(fields["allowed-tools"])
	delete(fields, "name")
	delete(fields, "description")
	delete(fields, "allowed-tools")
	return Skill{Name: name, Description: description, Path: path, AllowedTools: tools, Extra: fields}, nil
}

// toolList reads the value of an "allowed-tools" key into tool names. It
// takes the three shapes skill files use: a line of names split by commas
// or spaces ("web_search, web_fetch"), a flow list ("[web_search,
// web_fetch]"), and a block list, which parseFields keeps as "- web_search"
// lines. Quotes around a name are dropped, and so are repeats. It returns
// nil for an empty value.
func toolList(value string) []string {
	// FieldsFunc splits value at every rune for which the function returns
	// true: commas, brackets and white space, line breaks included.
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '[' || r == ']' || r == ' ' || r == '\t' || r == '\n'
	})
	var names []string
	for _, p := range parts {
		p = strings.Trim(p, `"'`)
		if p == "" || p == "-" || slices.Contains(names, p) {
			continue
		}
		names = append(names, p)
	}
	return names
}

// readSkillFile reads one SKILL.md, refusing files over maxFileBytes, and
// splits it into frontmatter fields and body. It fails when the file can't be
// read, is too large, or doesn't parse.
func readSkillFile(path string) (map[string]string, string, error) {
	data, err := readLimited(path)
	if err != nil {
		return nil, "", err
	}
	fields, body, err := parseFrontmatter(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", fileName, err)
	}
	return fields, body, nil
}

// readLimited reads the SKILL.md at path, refusing a file over
// maxFileBytes. It fails when the file can't be opened or read, or is too
// large.
func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- a SKILL.md inside the skills directory
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", fileName, err)
	}
	// defer runs f.Close() when this function returns, on every path.
	defer f.Close()
	// Read one byte past the limit: getting it back means the file is too big.
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fileName, err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%s is over the %d KiB limit", fileName, maxFileBytes>>10)
	}
	return data, nil
}

// Dir returns the directory the registry was loaded from.
func (r *Registry) Dir() string { return r.dir }

// Warnings returns one error per skill folder Load skipped, in folder-name
// order. merud logs them at startup and `meru skills list` shows them.
func (r *Registry) Warnings() []error {
	return slices.Clone(r.warnings)
}

// List returns the name and description of every loaded skill, sorted by name
// so the system prompt reads the same on every run.
func (r *Registry) List() []Summary {
	out := make([]Summary, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, Summary{Name: s.Name, Description: s.Description})
	}
	slices.SortFunc(out, func(a, b Summary) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Has reports whether the registry holds a skill called name.
func (r *Registry) Has(name string) bool {
	_, ok := r.skills[name]
	return ok
}

// Get returns the skill called name and true, or a zero Skill and false when
// there is none. The Extra map and the AllowedTools slice are copies, so the
// caller may change them.
func (r *Registry) Get(name string) (Skill, bool) {
	s, ok := r.skills[name]
	if !ok {
		return Skill{}, false
	}
	s.Extra = maps.Clone(s.Extra)
	s.AllowedTools = slices.Clone(s.AllowedTools)
	return s, true
}

// File returns the whole SKILL.md of the skill called name, header and all,
// as it is on disk now, for `meru skills show`. It fails with ErrNotFound
// for an unknown name, and when the file can't be read or has grown past
// the size limit.
func (r *Registry) File(name string) (string, error) {
	s, ok := r.skills[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	data, err := readLimited(s.Path)
	if err != nil {
		return "", fmt.Errorf("skill %s: %w", name, err)
	}
	return string(data), nil
}

// Body reads the instructions of the skill called name from disk and returns
// them. It reads the file afresh each time, so an edit made after Load shows
// up here. It fails with ErrNotFound for an unknown name, and with a read or
// parse error when the file has changed into something invalid, including a
// new name that no longer matches the folder.
func (r *Registry) Body(name string) (string, error) {
	s, ok := r.skills[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	fields, body, err := readSkillFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("skill %s: %w", name, err)
	}
	if fields["name"] != name {
		return "", fmt.Errorf("skill %s: the file now names %q; reload the skills", name, fields["name"])
	}
	return body, nil
}
