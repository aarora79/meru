// This file checks a change a client asks for through merud's
// connector_set op, before merud writes anything: new field values, new
// secrets, and turning a connector on or off. It uses the same rules the
// supervisor applies to config (checkSettings in status.go), so a change
// merud accepts never leaves the connector at needs config. See
// ARCHITECTURE.md, "Setting up a connector".

package connectors

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// Change is what one connector_set asks for. Enabled is nil when the
// change leaves on or off as it is; a pointer is Go's way to say "maybe
// no value". Values holds non-secret field values by field ID, and
// Secrets the secret ones, which go to secrets.toml and never to
// config.toml.
type Change struct {
	Enabled *bool
	Values  map[string]string
	Secrets map[string]string
}

// ErrNotManaged means the connector is one Meru only reports on, such as
// Ollama: there is nothing to turn on and nothing to set.
var ErrNotManaged = errors.New("not run by Meru, which only reports whether it answers")

// CheckChange checks ch against connector m, given table, the
// [connectors.<id>] table config holds now (nil when none), sec, the
// secrets saved now, and home, the user's home folder for a folder
// written "~/…". It returns nil when merud may write the change.
//
// It refuses:
//   - a dependency, such as Ollama, which Meru doesn't run;
//   - a value for a field the manifest lacks, a secret sent as a plain
//     value or a plain value sent as a secret, a value for an oauth
//     field, which has none, an empty secret, and a value on two lines;
//   - when the connector would be on after the change: any problem
//     checkSettings finds with the whole table, such as a required
//     field left empty or a folder that isn't there;
//   - when it would be off: a value that breaks its own field's rule.
//     A connector that stays off may still lack a required field.
//
// The error says what to fix in one sentence.
func CheckChange(m Manifest, table config.Connector, sec *secrets.Secrets, home string, ch Change) error {
	if m.Kind == KindDependency {
		return fmt.Errorf("%s: %w", m.Name, ErrNotManaged)
	}
	fields := map[string]Field{}
	for _, f := range m.Fields {
		fields[f.ID] = f
	}
	for _, k := range sortedKeys(ch.Values) {
		f, ok := fields[k]
		v := ch.Values[k]
		switch {
		case !ok:
			return fmt.Errorf("%s has no setting called %s", m.Name, k)
		case f.Type == FieldSecret:
			return fmt.Errorf("%s's %s is a secret; send it as one, so it goes to secrets.toml", m.Name, lowerFirst(f.Label))
		case f.Type == FieldOAuth:
			return fmt.Errorf("%s's %s takes no value: the server signs you in", m.Name, lowerFirst(f.Label))
		case strings.ContainsAny(v, "\r\n"):
			return fmt.Errorf("%s's %s must fit on one line", m.Name, lowerFirst(f.Label))
		}
	}
	for _, k := range sortedKeys(ch.Secrets) {
		f, ok := fields[k]
		v := strings.TrimSpace(ch.Secrets[k])
		switch {
		case !ok || f.Type != FieldSecret:
			return fmt.Errorf("%s has no secret called %s", m.Name, k)
		case v == "":
			return fmt.Errorf("%s's %s is empty", m.Name, lowerFirst(f.Label))
		case strings.ContainsAny(v, "\r\n"):
			return fmt.Errorf("%s's %s must fit on one line", m.Name, lowerFirst(f.Label))
		}
	}

	// The table and the secrets as they would be after the change.
	// maps.Clone copies the map, so the caller's config stays as it is.
	next := maps.Clone(table)
	if next == nil {
		next = config.Connector{}
	}
	for k, v := range ch.Values {
		next[k] = strings.TrimSpace(v)
	}
	if ch.Enabled != nil {
		next["enabled"] = *ch.Enabled
	}
	nextSec := sec
	for k, v := range ch.Secrets {
		nextSec = nextSec.With(SecretName(m.ID, k), v)
	}

	st := checkSettings(m, next, nextSec, home)
	if st.enabled {
		if st.problem != "" {
			return errors.New(st.problem)
		}
		return nil
	}
	// Off after the change: only the values given must be right, each by
	// its own field's rule; a required field may stay empty for now.
	for _, k := range sortedKeys(ch.Values) {
		written := strings.TrimSpace(ch.Values[k])
		if written == "" {
			continue
		}
		v := written
		if fields[k].Type == FieldFolder {
			v = expandHome(v, home)
		}
		if p := fieldProblem(m, fields[k], v, written); p != "" {
			return errors.New(p)
		}
	}
	return nil
}
