// This file holds the Secrets type: loading secrets.toml, turning
// "secret:<name>" values into the real ones, hiding values in text, and
// adding an entry.

package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Prefix marks a config value that names a secret instead of holding one:
// "secret:obsidian_api_key".
const Prefix = "secret:"

// minRedactLen is the shortest value Redact hides. A value of a few
// characters, such as "abc", would also match ordinary words in text; real
// keys and tokens run to 20 characters or more.
const minRedactLen = 8

// header opens every file Set writes. Set rewrites the whole file, so any
// comments a user added by hand don't survive; the header says so.
const header = `# Meru secrets: API keys and tokens, one name = "value" per line.
# config.toml refers to an entry as "secret:<name>". Keep this file
# readable only by you (chmod 600). meru mcp add rewrites this file,
# so comments you add here are not kept.
`

// Secrets holds the entries of secrets.toml. The zero value holds none.
type Secrets struct {
	values map[string]string
}

// Path returns the secrets file inside the Meru home dir.
func Path(dir string) string {
	return filepath.Join(dir, "secrets.toml")
}

// Name reports whether v names a secret, and returns the name. "secret:x"
// gives ("x", true); any other value gives ("", false).
func Name(v string) (string, bool) {
	name, ok := strings.CutPrefix(v, Prefix)
	return name, ok
}

// Load reads the secrets file at path. A missing file is not an error: it
// returns an empty Secrets. Load fails when group or others can read the
// file (on Unix), when it isn't valid TOML, or when an entry isn't a string
// with a valid name.
func Load(path string) (*Secrets, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Secrets{values: map[string]string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets %s: %w", path, err)
	}
	if err := checkMode(path, info); err != nil {
		return nil, err
	}
	values, err := read(path)
	if err != nil {
		return nil, err
	}
	return &Secrets{values: values}, nil
}

// read parses the file at path into a name -> value map, without the mode
// check. Set uses it too, so it can repair a file with the wrong mode.
func read(path string) (map[string]string, error) {
	// Decoding into map[string]any accepts any TOML, so the loop below can
	// say which entry is wrong instead of a generic type error.
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("read secrets %s: %w", path, err)
	}
	values := make(map[string]string, len(raw))
	for name, v := range raw {
		// v.(string) is a type assertion: it checks that v holds a string
		// and, when ok is true, gives it back as one.
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("secrets %s: %s must be a quoted string", path, name)
		}
		if err := checkName(name); err != nil {
			return nil, fmt.Errorf("secrets %s: %w", path, err)
		}
		values[name] = s
	}
	return values, nil
}

// Has reports whether the file holds a non-empty entry called name.
func (s *Secrets) Has(name string) bool {
	return s.values[name] != ""
}

// Resolve turns a config value into the value to use. "secret:<name>"
// becomes that entry of secrets.toml; any other string comes back as it is.
// It fails when the named entry is missing or empty, and the error names it,
// never a value.
func (s *Secrets) Resolve(v string) (string, error) {
	name, ok := Name(v)
	if !ok {
		return v, nil
	}
	if !s.Has(name) {
		return "", fmt.Errorf("secret %q is not in secrets.toml; add it with meru mcp add, or by hand", name)
	}
	return s.values[name], nil
}

// Redact returns text with every stored value of 8 or more characters
// replaced by "[secret:<name>]". It replaces longer values first, so a value
// that contains another one is hidden whole.
func (s *Secrets) Redact(text string) string {
	names := make([]string, 0, len(s.values))
	for name, v := range s.values {
		if len(v) >= minRedactLen {
			names = append(names, name)
		}
	}
	// Longest value first; ties break on the name, so the result never
	// depends on map order.
	slices.SortFunc(names, func(a, b string) int {
		if d := len(s.values[b]) - len(s.values[a]); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for _, name := range names {
		text = strings.ReplaceAll(text, s.values[name], "["+Prefix+name+"]")
	}
	return text
}

// Set adds or replaces the entry name in the secrets file at path, keeping
// the other entries. It creates the file and its folder when they don't
// exist. It writes a temporary file with mode 0600 and renames it over the
// old one, so a crash leaves either the old file or the new one, never half
// of one. It fails on a bad name, an empty value, or a file it can't parse.
func Set(path, name, value string) error {
	if err := checkName(name); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("secret %q: the value is empty", name)
	}

	values := map[string]string{}
	if _, err := os.Stat(path); err == nil {
		if values, err = read(path); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read secrets %s: %w", path, err)
	}
	values[name] = value

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// CreateTemp makes the file with mode 0600, in the same folder as the
	// target: a rename only replaces a file in one step within one disk.
	tmp, err := os.CreateTemp(dir, ".secrets-*.toml")
	if err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	// Until the rename succeeds, remove the temporary file on the way out.
	// After the rename, Remove finds nothing, and Set ignores its error.
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(header); err != nil {
		_ = tmp.Close() // the write error is the one worth reporting
		return fmt.Errorf("write secrets: %w", err)
	}
	// The TOML encoder quotes each value and writes the keys in sorted
	// order, so the file reads the same after every Set.
	if err := toml.NewEncoder(tmp).Encode(values); err != nil {
		_ = tmp.Close() // the write error is the one worth reporting
		return fmt.Errorf("write secrets: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write secrets %s: %w", path, err)
	}
	return nil
}

// checkName fails unless name is 1 to 64 ASCII letters, digits, '_' or '-'.
// Those are the characters a TOML bare key allows, so the file never needs
// quoted keys.
func checkName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("secret name %q must be 1 to 64 characters", name)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("secret name %q: use only letters, digits, '_' and '-'", name)
		}
	}
	return nil
}
