// This file holds the "Skills and commands" step: it makes sure the
// built-in skills are on, and offers the sample [[commands]] entries from
// the config template, the ones that only read, with a box to tick for
// each. The ticked ones go into config.toml uncommented.

package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/skills"
)

// CommandOption is one sample command on the screen.
type CommandOption struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Argv shows the program and its arguments, as config.toml has them.
	Argv []string `json:"argv"`
	// On is true when config.toml already has a command with this name.
	On bool `json:"on"`
	// Chosen is true when the box starts ticked.
	Chosen bool `json:"chosen"`
	// Missing, when not "", says why the command can't be turned on here,
	// such as "needs gh, which isn't installed".
	Missing string `json:"missing,omitempty"`
	// block is the entry's TOML text, uncommented, ready to append.
	block string
}

// defaultCommands names the samples that start ticked: snapshots of the
// Mac that take no file or folder, so none of them can read a file. The
// others, such as the gh and sed commands, stay one tick away.
func defaultCommands() []string {
	return []string{
		"disk-free", "system-load", "top-processes", "memory-free",
		"macos-version", "hardware-summary", "battery", "uptime",
	}
}

// launchdPath is the PATH launchd gives merud. A program elsewhere, such
// as gh from Homebrew, needs its full path in argv, or merud can't find it.
func launchdPath() []string { return []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} }

// programDirs are the folders the step looks in for a sample's program.
func programDirs() []string {
	return append(launchdPath(), "/opt/homebrew/bin", "/usr/local/bin")
}

// SampleBlocks returns the sample [[commands]] entries in template, each
// as its TOML text with the "# " taken off every line. A sample starts at
// a "# [[commands]]" line and runs until a line that is only "#" or isn't
// a comment.
func SampleBlocks(template string) []string {
	var blocks []string
	var cur []string
	flush := func() {
		if cur != nil {
			blocks = append(blocks, strings.Join(cur, "\n")+"\n")
			cur = nil
		}
	}
	for _, line := range strings.Split(template, "\n") {
		switch {
		case line == "# [[commands]]":
			flush()
			cur = []string{"[[commands]]"}
		case cur != nil && strings.HasPrefix(line, "# "):
			cur = append(cur, strings.TrimPrefix(line, "# "))
		default:
			flush()
		}
	}
	flush()
	return blocks
}

// SampleCommands returns the template's sample commands for the screen:
// which are on already, which start ticked, and which can't run on this
// Mac and why. cfg is what config.toml holds now.
func SampleCommands(p Paths, cfg config.Config) []CommandOption {
	var have []string
	for _, c := range cfg.Commands {
		have = append(have, c.Name)
	}
	var out []CommandOption
	for _, block := range SampleBlocks(config.Template()) {
		var parsed struct {
			Commands []config.Command `toml:"commands"`
		}
		if _, err := toml.Decode(block, &parsed); err != nil || len(parsed.Commands) != 1 {
			continue // TestSampleCommands catches a template sample that doesn't parse
		}
		c := parsed.Commands[0]
		o := CommandOption{Name: c.Name, Description: c.Description, Argv: c.Argv, block: block, On: slices.Contains(have, c.Name)}
		o.Missing, o.block = checkSample(p, c, block)
		o.Chosen = o.On || (o.Missing == "" && slices.Contains(defaultCommands(), c.Name))
		out = append(out, o)
	}
	return out
}

// checkSample says why sample c can't run on this Mac, or "" when it can,
// and returns block with the program's full path in argv when merud
// wouldn't find it on launchd's PATH.
func checkSample(p Paths, c config.Command, block string) (string, string) {
	if len(c.Argv) == 0 {
		return "has no program", block
	}
	prog := c.Argv[0]
	full := ""
	for _, dir := range programDirs() {
		if _, err := os.Stat(filepath.Join(dir, prog)); err == nil {
			full = filepath.Join(dir, prog)
			break
		}
	}
	if full == "" {
		return "needs " + prog + ", which isn't installed", block
	}
	for _, param := range c.Params {
		if param.Type == "path" && !isDir(expandHome(param.Under, p.Home)) {
			return "needs the folder " + param.Under, block
		}
	}
	if c.Cwd != "" && !isDir(expandHome(c.Cwd, p.Home)) {
		return "needs the folder " + c.Cwd, block
	}
	if !slices.Contains(launchdPath(), filepath.Dir(full)) {
		block = strings.Replace(block, `["`+prog+`"`, `["`+full+`"`, 1)
	}
	return "", block
}

// isDir reports whether path is a folder.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// SkillOption is one built-in skill on the screen, and whether it is on.
type SkillOption struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
}

// BuiltinSkills lists the skills Meru ships, each on unless [skills]
// disabled names it.
func BuiltinSkills(cfg config.Config) []SkillOption {
	var out []SkillOption
	for _, name := range skills.Builtins() {
		out = append(out, SkillOption{Name: name, On: !slices.Contains(cfg.Skills.Disabled, name)})
	}
	return out
}

// SaveSkillsAndCommands turns every built-in skill on, by taking the
// built-ins out of [skills] disabled, and appends each command in chosen
// that config.toml doesn't have yet. It returns what it did. A name that
// isn't a sample, or a sample that can't run here, is an error.
func SaveSkillsAndCommands(p Paths, chosen []string) (string, error) {
	cfg, err := loadConfig(p.Config())
	if err != nil {
		return "", err
	}
	var done []string

	builtins := skills.Builtins()
	var keep []string
	for _, name := range cfg.Skills.Disabled {
		if !slices.Contains(builtins, name) {
			keep = append(keep, name)
		}
	}
	if len(keep) != len(cfg.Skills.Disabled) {
		if err := catalog.SetTableLists(p.Config(), "skills", map[string][]string{"disabled": keep}, nil); err != nil {
			return "", err
		}
		done = append(done, "Turned the built-in skills back on in [skills] disabled.")
	}
	done = append(done, "The built-in skills are on: "+strings.Join(builtins, ", ")+".")

	options := SampleCommands(p, cfg)
	var added []string
	for _, name := range chosen {
		i := slices.IndexFunc(options, func(o CommandOption) bool { return o.Name == name })
		switch {
		case i < 0:
			return "", fmt.Errorf("%q isn't one of the sample commands", name)
		case options[i].On:
			continue
		case options[i].Missing != "":
			return "", fmt.Errorf("%s can't run on this Mac: it %s", name, options[i].Missing)
		}
		if err := catalog.AppendCommand(p.Config(), options[i].block); err != nil {
			return "", err
		}
		added = append(added, name)
	}
	if len(added) > 0 {
		done = append(done, "Added these commands to the end of "+p.Tilde(p.Config())+": "+strings.Join(added, ", ")+
			". Each only reads. Delete an entry there to turn it off.")
	} else {
		done = append(done, "Added no commands.")
	}
	return strings.Join(done, "\n"), nil
}
