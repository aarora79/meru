// This file holds `meru skills list | show | reset`. merud owns the skills
// folder, loads the skills and writes the shipped copies back; this side
// sends requests, asks before a reset throws away your edits, and formats
// the replies. See ARCHITECTURE.md, "Skills" and "Built-in skills".

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/rpc"
)

// errSkillsUsage is the error `meru skills` returns on bad usage.
var errSkillsUsage = errors.New("usage: meru skills list | meru skills show <name> | meru skills reset [--yes] <name>")

// maxDescription is how many characters of a description `meru skills
// list` shows, so each skill fits on one line of a normal terminal.
const maxDescription = 72

// skillsCmd runs `meru skills ...`. args are the words after "skills". in
// is where a reset reads its answer, and interactive says whether in is a
// terminal a person types into. It fails on bad usage, when merud can't be
// reached or replies with an error, and when a reset would replace your
// edits and nobody said yes.
func skillsCmd(ctx context.Context, socket string, args []string, in io.Reader, stdout io.Writer, interactive bool) error {
	switch {
	case len(args) == 1 && args[0] == "list":
		list, warnings, err := skillCall(ctx, socket, rpc.Request{Op: rpc.OpSkills})
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, skillsText(list, warnings, newLook(stdout)))
		return err
	case len(args) == 2 && args[0] == "show":
		list, _, err := skillCall(ctx, socket, rpc.Request{Op: rpc.OpSkillShow, ID: args[1]})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return fmt.Errorf("merud sent no skill called %q", args[1])
		}
		body := list[0].Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		_, err = io.WriteString(stdout, body)
		return err
	case len(args) >= 2 && args[0] == "reset":
		// slices.DeleteFunc drops the flag wherever it sits among the words.
		rest := slices.DeleteFunc(slices.Clone(args[1:]), func(a string) bool { return a == "--yes" || a == "-y" })
		if len(rest) != 1 {
			return errSkillsUsage
		}
		yes := len(rest) < len(args)-1
		return resetSkill(ctx, socket, rest[0], yes, in, stdout, interactive)
	}
	return errSkillsUsage
}

// resetSkill asks merud to put the shipped copy of the built-in skill name
// back. When your copy has edits, or merud couldn't load it, the reset
// would throw your text away, so it asks first on a terminal and needs
// --yes (yes) anywhere else. A copy with no edits resets at once. It fails
// when you say no, when nobody can answer, or when merud refuses, as it
// does for a skill Meru doesn't ship.
func resetSkill(ctx context.Context, socket, name string, yes bool, in io.Reader, stdout io.Writer, interactive bool) error {
	if !yes {
		list, _, err := skillCall(ctx, socket, rpc.Request{Op: rpc.OpSkills})
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(s rpc.SkillInfo) bool { return s.Name == name })
		switch {
		case i >= 0 && !list[i].Builtin:
			return fmt.Errorf("%q isn't a built-in skill, so there is no shipped copy to reset it to", name)
		case i >= 0 && !list[i].Edited:
			// Your copy is the shipped one: nothing to lose.
		case !interactive:
			return fmt.Errorf("resetting %s replaces your copy; run `meru skills reset --yes %s` to do it", name, name)
		default:
			ok, err := confirm(in, stdout, fmt.Sprintf("Replace your copy of %s with the shipped one? [y/N] ", name))
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintln(stdout, "Kept your copy.")
				return nil
			}
		}
	}
	if _, _, err := skillCall(ctx, socket, rpc.Request{Op: rpc.OpSkillReset, ID: name}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Reset %s to the shipped copy.\n", name)
	return nil
}

// confirm prints prompt and reads one line from in. It returns true for
// "y" or "yes" in any case, and false for anything else, an empty line or
// the end of the input.
func confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// skillsText writes one line per skill: the name padded to one width, the
// description on one line and cut to maxDescription characters, and the
// [built-in] and [edited] marks, dim:
//
//	explainer  Build a self-contained HTML explainer for a technical topic…  [built-in]
//	writing    Write prose people will actually read. Use for any prose y…  [built-in] [edited]
//
// The reasons merud skipped any folders follow, so a broken SKILL.md
// doesn't go unnoticed.
func skillsText(list []rpc.SkillInfo, warnings string, lk look) string {
	var b strings.Builder
	if len(list) == 0 {
		b.WriteString("No skills. Add a folder holding a SKILL.md to ~/.meru/skills to make one.\n")
	}
	nameWidth, descWidth := 0, 0
	descs := make([]string, len(list))
	for i, s := range list {
		descs[i] = cutLine(s.Description, maxDescription)
		nameWidth = max(nameWidth, len([]rune(s.Name)))
		descWidth = max(descWidth, len([]rune(descs[i])))
	}
	for i, s := range list {
		var marks []string
		if s.Builtin {
			marks = append(marks, "[built-in]")
		}
		if s.Edited {
			marks = append(marks, "[edited]")
		}
		if len(marks) == 0 {
			// %-*s pads to a width taken from the argument before the text.
			line := fmt.Sprintf("%-*s  %s", nameWidth, s.Name, descs[i])
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			continue
		}
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", nameWidth, s.Name, descWidth, descs[i], lk.dim.Render(strings.Join(marks, " ")))
	}
	if warnings != "" {
		b.WriteString("\n" + lk.amber.Render("Skipped:") + "\n")
		for _, w := range strings.Split(warnings, "\n") {
			b.WriteString("  " + w + "\n")
		}
	}
	return b.String()
}

// cutLine folds s onto one line and cuts it to n characters, ending in "…"
// when it cut anything.
func cutLine(s string, n int) string {
	s = oneLine(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// skillCall sends one skill request and returns the skills in merud's
// "skills" event, if it sent one, with the event's Text: the reasons merud
// skipped any folders. It fails when merud can't be reached or replies with
// an error, as a merud older than the skill ops does.
func skillCall(ctx context.Context, socket string, req rpc.Request) ([]rpc.SkillInfo, string, error) {
	var list []rpc.SkillInfo
	var warnings string
	for ev, err := range rpc.Do(ctx, socket, req, nil) {
		if err != nil {
			return nil, "", err
		}
		switch ev.Type {
		case rpc.EventSkills:
			list, warnings = ev.Skills, ev.Text
		case rpc.EventError:
			return nil, "", errors.New(ev.Error)
		}
	}
	return list, warnings, nil
}
