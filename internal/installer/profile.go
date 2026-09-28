// This file holds the "About you" step. It saves the user's name, email
// and how they like answers as memory files, the same files `meru setup
// user` makes through merud: "Name: Dana Reyes" in memory/me/, and so on.
// merud reads the memory folder when it starts and watches it after, so
// writing the files here works before merud runs and keeps one format.

package installer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aarora79/meru/internal/memory"
)

// Profile is what the About you screen asks. Name is required; Email is
// required only after the Google step, whose tools need the address.
type Profile struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Answers string `json:"answers"`
}

// profileField is one Profile field, the label its memory starts with,
// and the memory kind it goes in: "me" and "preferences" go into every
// prompt.
type profileField struct {
	label string
	kind  string
	value func(Profile) string
}

// profileFields returns the three fields, with the labels `meru setup user`
// uses, so a memory made either way reads the same.
func profileFields() []profileField {
	return []profileField{
		{"Name", "me", func(p Profile) string { return p.Name }},
		{"Email", "me", func(p Profile) string { return p.Email }},
		{"Answers", "preferences", func(p Profile) string { return p.Answers }},
	}
}

// maxProfileField caps each answer. A name or a sentence on answers fits
// many times over; more means a paste gone wrong.
const maxProfileField = 500

// LoadProfile reads the saved profile from the memory folder, so a second
// run shows what Meru knows instead of an empty form. A field with no
// memory comes back "".
func LoadProfile(p Paths) (Profile, error) {
	store, err := memory.Open(p.Memory())
	if err != nil {
		return Profile{}, err
	}
	var out Profile
	for _, f := range profileFields() {
		mems, err := store.ListKind(f.kind)
		if err != nil && len(mems) == 0 {
			return Profile{}, err
		}
		for _, m := range mems {
			if v, ok := strings.CutPrefix(m.Text, f.label+": "); ok {
				switch f.label {
				case "Name":
					out.Name = v
				case "Email":
					out.Email = v
				case "Answers":
					out.Answers = v
				}
				break
			}
		}
	}
	return out, nil
}

// Check returns the first problem with in, in words for the screen. The
// name is always required; the email is required when emailRequired is
// true, because the Google step ran and its tools take the address.
func (in Profile) Check(emailRequired bool) error {
	switch {
	case strings.TrimSpace(in.Name) == "":
		return errors.New("type your name; Meru needs it to tell you apart from the people in your files")
	case emailRequired && strings.TrimSpace(in.Email) == "":
		return errors.New("type your email address; the Google tools need it on every call")
	case in.Email != "" && !strings.Contains(in.Email, "@"):
		return errors.New("your email address needs an @")
	}
	for _, f := range profileFields() {
		v := f.value(in)
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s must fit on one line", strings.ToLower(f.label))
		}
		if len(v) > maxProfileField {
			return fmt.Errorf("%s is longer than %d characters", strings.ToLower(f.label), maxProfileField)
		}
	}
	return nil
}

// SaveProfile checks in and writes it to the memory folder: one file per
// field, "Label: value". A field that didn't change keeps its file; a
// changed one replaces the old file; an optional field left empty removes
// its file. It returns what it did.
func SaveProfile(p Paths, in Profile, emailRequired bool) (string, error) {
	in = Profile{Name: strings.TrimSpace(in.Name), Email: strings.TrimSpace(in.Email), Answers: strings.TrimSpace(in.Answers)}
	if err := in.Check(emailRequired); err != nil {
		return "", err
	}
	store, err := memory.Open(p.Memory())
	if err != nil {
		return "", err
	}
	var saved []string
	for _, f := range profileFields() {
		want := ""
		if v := f.value(in); v != "" {
			want = f.label + ": " + v
		}
		mems, err := store.ListKind(f.kind)
		if err != nil && len(mems) == 0 {
			return "", err
		}
		kept := false
		for _, m := range mems {
			if !strings.HasPrefix(m.Text, f.label+": ") {
				continue
			}
			if m.Text == want && !kept {
				kept = true
				continue
			}
			if err := store.Forget(m.ID); err != nil {
				return "", fmt.Errorf("replace %s: %w", m.ID, err)
			}
		}
		if want != "" && !kept {
			if _, err := store.Add(f.kind, want, "installer"); err != nil {
				return "", err
			}
		}
		if want != "" {
			saved = append(saved, strings.ToLower(f.label))
		}
	}
	return "Saved your " + strings.Join(saved, ", ") + " in " + p.Tilde(p.Memory()) + ". " +
		"Change them later in Meru.app's Settings, About you, or with /me in meru chat.", nil
}
