// This file holds `meru setup user`: a few short questions about who you
// are, each answer saved as one memory through merud. merud puts every
// memory of kind "me" and "preferences" into every prompt, so the model
// knows your name and can tell you apart from people in your files. See
// ARCHITECTURE.md, "Memory", step 0.

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/aarora79/meru/internal/rpc"
)

// A profile question and the label its answer is saved under. Each answer
// becomes one memory written as "Label: answer", such as "Name: Dana
// Arora". The label says what the fact is, and the prompt section these go
// into ("What you know about the user") says whose it is, so every line
// reads as a fact about the user in the third person. A full sentence such
// as "The user's name is Dana Reyes" says the same thing in more words, and
// many answers ("staff engineer at Acme") don't fit into one without
// rewording. merud names each file after its text, so the label also gives
// tidy IDs such as me/name-dana-reyes.md.
type profileQuestion struct {
	prompt string
	label  string
	kind   string
}

// profileQuestions are the questions setup user asks, in order. The free
// lines about anything else come between work and answers; askProfile asks
// for them.
var profileQuestions = []profileQuestion{
	{"Your name:", "Name", "me"},
	{"What you do, your role and where you work:", "Work", "me"},
	{"Where you live (a city is enough):", "Lives in", "me"},
}

// answersQuestion asks how the user likes answers. It saves to
// "preferences", the other kind every prompt carries.
var answersQuestion = profileQuestion{"How you like answers, for example \"short, with bullet points\":", "Answers", "preferences"}

// setupUserCmd runs `meru setup user`. It shows what Meru already knows
// about the user and offers to keep it or start over, then asks the
// profile questions and saves each answer. It fails when merud can't be
// reached or refuses a request.
func setupUserCmd(ctx context.Context, socket string, c *console) error {
	mems, err := listMemories(ctx, socket)
	if err != nil {
		return err
	}
	known := ofKinds(mems, rpc.ProfileKinds()...)
	if len(known) > 0 {
		fmt.Fprintln(c.out, "Meru already knows this about you:")
		for _, m := range known {
			fmt.Fprintf(c.out, "  %s\n", oneLine(m.Text))
		}
		keep, err := c.yes("Keep it and add more? Answer n to forget it all and start over.", true)
		if err != nil {
			return err
		}
		if !keep {
			for _, m := range known {
				if err := forgetMemory(ctx, socket, m.ID); err != nil {
					return fmt.Errorf("forget %s: %w", m.ID, err)
				}
			}
			fmt.Fprintln(c.out, "Forgot all of it.")
		}
	}

	fmt.Fprintln(c.out, "Answer a few questions about you. Press Enter to skip one.")
	saved, err := askProfile(ctx, socket, c)
	if err != nil {
		return err
	}
	writeSaved(c.out, saved)
	return nil
}

// askProfile asks each profile question and saves each answer as it comes,
// so an answer typed before Ctrl-C isn't lost. It returns the memories
// merud wrote.
func askProfile(ctx context.Context, socket string, c *console) ([]rpc.MemoryInfo, error) {
	var saved []rpc.MemoryInfo
	save := func(kind, text string) error {
		m, err := addMemory(ctx, socket, kind, text)
		if err != nil {
			return err
		}
		saved = append(saved, m)
		return nil
	}

	for _, q := range profileQuestions {
		a, err := c.ask(q.prompt)
		if err != nil {
			return saved, err
		}
		if a != "" {
			if err := save(q.kind, q.label+": "+a); err != nil {
				return saved, err
			}
		}
	}

	// Free lines go in as the user typed them: Meru can't turn "I have
	// two kids" into the third person without a model, and the prompt
	// already says that "I" means the user.
	// Each line's prompt says how to stop, because a line such as "that's
	// all for now" would otherwise be saved as a fact about the user.
	fmt.Fprintln(c.out, "Anything else Meru should always know about you? One fact per line.")
	for {
		a, err := c.ask("  fact (Enter on an empty line when done):")
		if err != nil {
			return saved, err
		}
		if a == "" {
			break
		}
		if err := save("me", a); err != nil {
			return saved, err
		}
	}

	a, err := c.ask(answersQuestion.prompt)
	if err != nil {
		return saved, err
	}
	if a != "" {
		if err := save(answersQuestion.kind, answersQuestion.label+": "+a); err != nil {
			return saved, err
		}
	}
	return saved, nil
}

// writeSaved prints what setup user saved and where to find it later.
func writeSaved(out io.Writer, saved []rpc.MemoryInfo) {
	if len(saved) == 0 {
		fmt.Fprintln(out, "Nothing saved. Run `meru setup user` again any time.")
		return
	}
	fmt.Fprintln(out, "\nSaved:")
	for _, m := range saved {
		fmt.Fprintf(out, "  %s  %s\n", m.ID, oneLine(m.Text))
	}
	fmt.Fprintln(out, "Meru uses these in every answer, and `meru memory list` shows them.\n"+
		"You can also tell Meru things in chat: \"remember that I work on the registry team\".")
}
