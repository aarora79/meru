// This file holds what a user sees of a connector: the states, the
// one-line sentence for each, and the check of the user's values against
// the manifest's fields, which names the field to fix. See
// ARCHITECTURE.md, "The supervisor".

package connectors

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// The states a user sees. The supervisor keeps finer ones inside (see
// phase in supervisor.go) and folds them into these.
const (
	StateOK          = "ok"           // running, or installed and checked, and starts on first use
	StateOff         = "off"          // config turns it off, or never turned it on
	StateNeedsConfig = "needs_config" // a field is missing or wrong; Fix names it
	StateStarting    = "starting"     // installing, starting, or waiting to restart after a crash
	StateFailed      = "failed"       // stopped; the sentence says why
	StateByHand      = "by_hand"      // an [[mcp.servers]] entry of the same name runs it instead
)

// Status is one connector as the connectors op reports it.
type Status struct {
	ID       string
	Name     string
	Kind     string
	Required bool
	// State is one of the State constants, and Sentence says it in one
	// line, such as "Obsidian needs your vault folder."
	State    string
	Sentence string
	// Fields are the manifest's fields, in order, with what config and
	// secrets.toml hold for each.
	Fields []FieldStatus
	// Fix lists the IDs of the fields to ask the user again, for
	// needs_config.
	Fix []string
	// Link is the sign-in link an oauth connector's server gave, while
	// the connector waits for the user to sign in; empty otherwise. It
	// holds a one-time state value, so merud never logs it.
	Link string
}

// FieldStatus is one field of a connector with its current value. Value
// is empty for a secret, which never leaves merud; Saved says whether
// secrets.toml holds it.
type FieldStatus struct {
	Field
	Value string
	Saved bool
}

// SecretName is the secrets.toml entry that holds a connector's secret
// field: connector_<id>_<field>.
func SecretName(id, field string) string {
	return "connector_" + id + "_" + field
}

// settings is the outcome of checking one connector's config against its
// manifest: the values to start it with, or the problem and the fields
// that cause it.
type settings struct {
	enabled bool
	// values holds every field's value by ID, secrets included, with the
	// defaults and the values Meru makes up filled in. It is for the
	// launch only and never leaves merud.
	values map[string]string
	// shown holds the non-secret values as config writes them, and saved
	// the secret fields secrets.toml holds, for Status.
	shown map[string]string
	saved map[string]bool
	// problem is the needs_config sentence, "" when every field is fine,
	// and fix the fields to ask again.
	problem string
	fix     []string
	// tools holds the tool lists the model gets: the manifest's, with any
	// list the table sets in its place.
	tools MCP
}

// same reports whether a and b would start the connector the same way,
// so a reload that changes nothing leaves a running connector alone. The
// tool lists count too: the pool takes them from the supervisor.
func (a settings) same(b settings) bool {
	return a.enabled == b.enabled && a.problem == b.problem && sameValues(a.values, b.values) &&
		slices.Equal(a.tools.Allow, b.tools.Allow) && slices.Equal(a.tools.Confirm, b.tools.Confirm) &&
		slices.Equal(a.tools.AlwaysConfirm, b.tools.AlwaysConfirm)
}

// sameValues reports whether two string maps hold the same keys and values.
func sameValues(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// checkSettings checks table, the connector's [connectors.<id>] table
// (nil when config has none), and its secrets in sec, against the
// manifest m. home is the user's home folder, for a folder written with
// "~/". It never fails: a problem becomes the needs_config sentence.
//
// The rules, one per field type: a required field needs a value; a folder
// must exist; an email needs an "@"; a choice must be one of the choices;
// and a value must match the field's pattern. An oauth field has no value
// in config: the server signs the user in, and the supervisor reports it
// (phaseSignIn). A key the manifest has no field for is a problem too,
// since it is most often a typo; the tool lists allow, confirm and
// always_confirm are the exception, and checkTools checks them.
func checkSettings(m Manifest, table config.Connector, sec *secrets.Secrets, home string) settings {
	st := settings{enabled: m.DefaultOn, values: map[string]string{}, shown: map[string]string{}, saved: map[string]bool{}, tools: m.MCP}
	if on, ok := table.Enabled(); ok {
		st.enabled = on
	}
	// problem keeps the first sentence and adds id, a field to ask
	// again, to fix; "" adds none.
	problem := func(id, sentence string) {
		if st.problem == "" {
			st.problem = sentence
		}
		if id != "" {
			st.fix = append(st.fix, id)
		}
	}

	known := map[string]bool{"enabled": true}
	for _, f := range m.Fields {
		known[f.ID] = true
		if f.Type == FieldOAuth {
			continue
		}
		var v string
		if f.Type == FieldSecret {
			name := SecretName(m.ID, f.ID)
			if sec != nil && sec.Has(name) {
				// Resolve can't fail for a name Has found.
				v, _ = sec.Resolve("secret:" + name)
				st.saved[f.ID] = true
			}
		} else {
			v, _ = table.Value(f.ID)
			v = strings.TrimSpace(v)
			st.shown[f.ID] = v
		}
		if v == "" {
			v = f.Default
		}
		// written is the value as the user wrote it, or the default, for a
		// sentence; v may become an absolute path below.
		written := v
		if f.Type == FieldFolder && v != "" {
			v = expandHome(v, home)
		}
		st.values[f.ID] = v

		label := lowerFirst(f.Label)
		switch {
		case v == "" && f.Required:
			problem(f.ID, fmt.Sprintf("%s needs your %s.", m.Name, label))
		case v == "":
			// An optional field left empty is fine.
		case f.Type == FieldFolder && !isFolder(v):
			problem(f.ID, fmt.Sprintf("%s can't find the %s %s.", m.Name, label, written))
		case f.Type == FieldEmail && !strings.Contains(v, "@"):
			problem(f.ID, fmt.Sprintf("%s needs an email address as your %s.", m.Name, label))
		case f.Type == FieldChoice && !slices.Contains(f.Choices, v):
			problem(f.ID, fmt.Sprintf("%s's %s must be one of: %s.", m.Name, label, strings.Join(f.Choices, ", ")))
		case f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(v):
			// Validate compiled every pattern already, so MustCompile
			// can't panic here.
			problem(f.ID, fmt.Sprintf("%s's %s doesn't fit the form it needs (%s).", m.Name, label, f.Pattern))
		}
	}
	// Ranging over a map visits the keys in a random order, so sort them
	// first: the same config always gives the same sentence.
	var keys []string
	for k := range table {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !known[k] && !(isMCP(m) && slices.Contains(config.ListKeys, k)) {
			problem("", fmt.Sprintf("%s has no setting called %s; remove it from [connectors.%s].", m.Name, k, m.ID))
		}
	}
	if isMCP(m) {
		var sentence string
		st.tools, sentence = checkTools(m, table)
		if sentence != "" {
			problem("", sentence)
		}
	}
	fillMadeUp(m, st.values)
	return st
}

// isMCP reports whether m is an MCP server, stdio or http, which has tool
// lists.
func isMCP(m Manifest) bool {
	return m.Kind == KindStdio || m.Kind == KindHTTP
}

// checkTools returns the tool lists for connector m: the manifest's, with
// each list the table sets in its place. The lists follow the rules of an
// [[mcp.servers]] entry: each entry names one tool, with no wildcard, and
// every tool in confirm or always_confirm is also in allow. When one
// breaks a rule, it returns the manifest's lists and the sentence that
// says what to fix. A tool the server doesn't offer isn't checked here:
// the MCP pool reports it, as for a server added by hand ("offers no such
// tool" in meru tools), since only the running server knows its tools.
func checkTools(m Manifest, table config.Connector) (MCP, string) {
	lists := m.MCP
	if l, ok := table.List("allow"); ok {
		lists.Allow = l
	}
	if l, ok := table.List("confirm"); ok {
		lists.Confirm = l
	}
	if l, ok := table.List("always_confirm"); ok {
		lists.AlwaysConfirm = l
	}
	// checkMCP is the manifest's own rule for its lists. Its messages name
	// the manifest's keys, "mcp.confirm"; the table's keys have no "mcp.".
	if errs := checkMCP(m.Kind, lists); len(errs) > 0 {
		msg := strings.ReplaceAll(errs[0].Error(), "mcp.", "")
		return m.MCP, fmt.Sprintf("%s's tool lists in [connectors.%s] need a fix: %s.", m.Name, m.ID, strings.TrimSuffix(msg, "."))
	}
	return lists, ""
}

// fillMadeUp fills in the values Meru makes up for the user when a field
// is left empty. There is one today: Obsidian's vault name, from the
// vault folder's name, in the form obsidian-mcp asks for.
func fillMadeUp(m Manifest, values map[string]string) {
	if m.ID != "obsidian" || values["vault_name"] != "" || values["vault_path"] == "" {
		return
	}
	values["vault_name"] = vaultName(filepath.Base(values["vault_path"]))
}

// vaultName turns a folder's name into a vault ID obsidian-mcp accepts:
// lower-case letters, digits, "-" and "_", starting with a letter. Other
// characters become "-". A name with no letter to start with becomes
// "vault".
func vaultName(folder string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(folder) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.TrimLeft(b.String(), "0123456789_-")
	if name == "" {
		return "vault"
	}
	return name
}

// expandHome turns a leading "~/" into the home folder.
func expandHome(p, home string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok && home != "" {
		return filepath.Join(home, rest)
	}
	if p == "~" && home != "" {
		return home
	}
	return p
}

// isFolder reports whether p is a folder merud can open.
func isFolder(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// lowerFirst lower-cases the first letter of a label, so "Vault folder"
// reads "vault folder" inside a sentence. A label that starts with an
// acronym, such as "API token", stays as it is.
func lowerFirst(s string) string {
	if s == "" || (len(s) > 1 && strings.ToUpper(s[1:2]) == s[1:2] && strings.ToLower(s[1:2]) != s[1:2]) {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
