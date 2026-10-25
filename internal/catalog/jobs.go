// This file adds, replaces and removes one [[jobs]] entry in config.toml,
// for the clients' Jobs screens, and leaves every other line, comments
// included, as it was (ARCHITECTURE.md, "Scheduler").

package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/config"
)

// JobBlock writes j as a [[jobs]] entry: its keys in a fixed order, and
// only the ones set, so a job written here reads as one written by hand.
func JobBlock(j config.Job) string {
	var b strings.Builder
	b.WriteString("[[jobs]]\n")
	key := func(k, v string) { fmt.Fprintf(&b, "%-11s = %s\n", k, v) }
	key("name", quote(j.Name))
	key("schedule", quote(j.Schedule))
	if j.EveryWeeks > 1 {
		key("every_weeks", strconv.Itoa(j.EveryWeeks))
		key("from", quote(j.From))
	}
	if j.Prompt != "" {
		key("prompt", quote(j.Prompt))
	}
	if j.Skill != "" {
		key("skill", quote(j.Skill))
	}
	if j.Command != "" {
		key("command", quote(j.Command))
	}
	if len(j.Args) > 0 {
		names := make([]string, 0, len(j.Args))
		for k := range j.Args {
			names = append(names, k)
		}
		sort.Strings(names)
		var parts []string
		for _, k := range names {
			parts = append(parts, quote(k)+" = "+quote(j.Args[k]))
		}
		key("args", "{ "+strings.Join(parts, ", ")+" }")
	}
	if len(j.Allow) > 0 {
		var parts []string
		for _, t := range j.Allow {
			parts = append(parts, quote(t))
		}
		key("allow", "["+strings.Join(parts, ", ")+"]")
	}
	if j.Output != "" {
		key("output", quote(j.Output))
	}
	if !j.On() {
		key("enabled", "false")
	}
	return b.String()
}

// SetJob writes j into the config file at configPath: in place of the job
// named was, so an edit can rename the job and the job keeps its place and
// its comment, or, with was empty, at the end of the file. It writes through
// writeChecked, so the file changes only when the result loads, passes
// every check, and holds the jobs it should with j among them as given.
// It fails for a name another job has, a was no job has, or a config that
// doesn't load.
func SetJob(configPath, was string, j config.Job) error {
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	before, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("fix config.toml before changing a job: %w", err)
	}
	// was == "" adds a job; otherwise the job named was gives way to j.
	var want []string
	replacing := false
	for _, x := range before.Jobs {
		switch {
		case was != "" && x.Name == was:
			replacing = true
			want = append(want, j.Name)
		case x.Name == j.Name:
			return fmt.Errorf("a job named %q exists already", j.Name)
		default:
			want = append(want, x.Name)
		}
	}
	if was != "" && !replacing {
		return fmt.Errorf("%s has no job named %q", configPath, was)
	}

	text := string(old)
	block := JobBlock(j)
	if replacing {
		lines := splitLines(text)
		start, end, ok := jobSpan(lines, was)
		if !ok {
			return fmt.Errorf("can't find the [[jobs]] block named %q; edit %s by hand", was, configPath)
		}
		// The comment lines above the block stay: they are the user's
		// note on the job, and the edit doesn't touch them.
		for start < end && isComment(lines[start]) {
			start++
		}
		text = strings.Join(lines[:start], "") + block + strings.Join(lines[end:], "")
	} else {
		want = append(want, j.Name)
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if text != "" {
			text += "\n"
		}
		text += block
	}
	return writeChecked(configPath, text, func(next config.Config) error {
		var got []string
		var saved config.Job
		for _, x := range next.Jobs {
			got = append(got, x.Name)
			if x.Name == j.Name {
				saved = x
			}
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("saving %q would leave the jobs %v, want %v; edit %s by hand", j.Name, got, want, configPath)
		}
		if !sameJob(saved, j) {
			return fmt.Errorf("saving %q came out different from what was asked; edit %s by hand", j.Name, configPath)
		}
		return nil
	})
}

// sameJob reports whether two jobs say the same thing, reading a missing
// enabled as on and empty lists and tables as none.
func sameJob(a, b config.Job) bool {
	norm := func(j config.Job) config.Job {
		on := j.On()
		j.Enabled = &on
		if len(j.Args) == 0 {
			j.Args = nil
		}
		if len(j.Allow) == 0 {
			j.Allow = nil
		}
		if j.EveryWeeks <= 1 {
			j.EveryWeeks, j.From = 0, ""
		}
		return j
	}
	// reflect.DeepEqual compares two values field by field, maps and
	// pointers included.
	return reflect.DeepEqual(norm(a), norm(b))
}

// RemoveJob takes the [[jobs]] block named name out of the config file at
// configPath, with the comment lines right above it, and leaves the rest.
// It fails when config doesn't load or has no job called name.
func RemoveJob(configPath, name string) error {
	old, err := os.ReadFile(configPath) // #nosec G304 -- the user's own config.toml
	if err != nil {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("fix config.toml before removing a job: %w", err)
	}
	var want []string
	found := false
	for _, j := range cfg.Jobs {
		if j.Name == name {
			found = true
			continue
		}
		want = append(want, j.Name)
	}
	if !found {
		return fmt.Errorf("%s has no job named %q", configPath, name)
	}
	lines := splitLines(string(old))
	start, end, ok := jobSpan(lines, name)
	if !ok {
		return fmt.Errorf("can't find the [[jobs]] block named %q; edit %s by hand", name, configPath)
	}
	kept := append(slices.Clone(lines[:start]), lines[end:]...)
	if start > 0 && start < len(kept) && isBlank(kept[start-1]) && isBlank(kept[start]) {
		kept = slices.Delete(kept, start, start+1)
	}
	return writeChecked(configPath, strings.Join(kept, ""), func(next config.Config) error {
		var got []string
		for _, j := range next.Jobs {
			got = append(got, j.Name)
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("removing %q would leave the jobs %v, want %v; edit %s by hand", name, got, want, configPath)
		}
		return nil
	})
}

// jobSpan finds the [[jobs]] block named name in lines, as serverSpan finds
// a server: from its [[jobs]] line, with the comments right above it, to
// its last key before the next top-level header, [jobs.args] included.
func jobSpan(lines []string, name string) (start, end int, ok bool) {
	start, end = -1, len(lines)
	for i, line := range lines {
		key, isHeader := header(line)
		if !isHeader || strings.HasPrefix(key, "jobs.") {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if key == "jobs" && jobNamed(lines[i:], name) {
			start = i
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	for end > start+1 && (isComment(lines[end-1]) || isBlank(lines[end-1])) {
		end--
	}
	for start > 0 && isComment(lines[start-1]) && strings.TrimSpace(lines[start-1]) != "#" {
		start--
	}
	return start, end, true
}

// jobNamed reports whether lines, which start at a [[jobs]] header, hold
// the job called name, read with the TOML library up to the next
// top-level header.
func jobNamed(lines []string, name string) bool {
	var b strings.Builder
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		if key, ok := header(line); ok && !strings.HasPrefix(key, "jobs.") {
			break
		}
		b.WriteString(line)
	}
	var parsed struct {
		Jobs []config.Job `toml:"jobs"`
	}
	if _, err := toml.Decode(b.String(), &parsed); err != nil {
		return false
	}
	return len(parsed.Jobs) == 1 && parsed.Jobs[0].Name == name
}
