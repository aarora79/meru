// Package skills reads the skill folders under ~/.meru/skills/ and ships
// Meru's two built-in skills, writing and explainer.
//
// A skill is a folder holding a SKILL.md file: YAML frontmatter with a name
// and a description, then a Markdown body of instructions. See
// ARCHITECTURE.md, "Skills" and "Built-in skills". The system prompt carries
// only each skill's name and description (List); the body loads when a turn
// needs it (Body). That split is called progressive disclosure: adding a
// skill costs the prompt one line, not the whole file.
//
// The built-in skills live in builtin/ and ship inside the binary through Go's
// embed package. They are byte-for-byte copies of the owner's my-ai-assets
// skills (see AGENTS.md), so nobody edits them here. InstallBuiltins copies
// them to disk only where no folder of that name exists, so your edits win;
// Reset puts the shipped version back.
//
// Stamp tells merud when the folder changed, so it can call Load again;
// Edited and File answer `meru skills list` and `meru skills show`.
//
// What this package deliberately doesn't do: it doesn't pick a skill for a
// turn (internal/agent does, with one short call to the fast model), doesn't
// watch the folder with a background goroutine (merud compares stamps), and
// doesn't run anything a skill describes. It reads text files and writes the
// built-ins, nothing more.
package skills
