// This file holds the remember tool, which saves one fact about the user as
// a memory file. See ARCHITECTURE.md, "Memory", step 3.

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/secrets"
)

// Remember is the remember tool's name, as the model sees it.
const Remember = "remember"

// rememberDescription tells the model when to call remember and how to
// write the fact. A 2B model follows an example better than a rule, so it
// gets one. The third person matters: the profile goes into every prompt as
// a list of facts about the user, and "I work on..." there would read as
// the model talking about itself.
const rememberDescription = "Saves one lasting fact the user tells you about themselves, their work, " +
	"the people they know, their projects or their preferences, so you know it in later chats. " +
	"Save one fact per call, written about the user in the third person, " +
	"for example \"Works on the AI registry team at Example Corp\". " +
	"Use kind \"me\" for who the user is: name, work, where they live, family. " +
	"Use kind \"preferences\" for how they like things done. " +
	"Never save passwords, API keys or other secrets."

// rememberArgs is the JSON object the model sends to remember.
type rememberArgs struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// kinds returns the memory kinds the model may pick from: the folders in
// the memory directory. When the directory can't be read, it falls back to
// the default kinds, so the tool's schema never comes out empty; Add then
// reports the real problem.
func (t *Tools) kinds() []string {
	kinds, err := t.memory.Kinds()
	if err != nil {
		return memory.DefaultKinds()
	}
	return kinds
}

// remember checks args and saves the fact as a new memory file. It returns
// the text the model reads, naming the file. It fails, with text the model
// reads, on a kind that isn't a memory folder, an empty or oversized text,
// or a text that holds a value from secrets.toml.
func (t *Tools) remember(ctx context.Context, raw json.RawMessage) (string, error) {
	var a rememberArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("remember: the arguments aren't a valid JSON object for this tool: %v", err)
	}
	// The model picks from the folders that exist. It can't make a new
	// kind; the user does that by creating a folder.
	kinds := t.kinds()
	if !slices.Contains(kinds, a.Kind) {
		return "", fmt.Errorf("remember: kind %q is unknown; use one of %s. Nothing was saved",
			a.Kind, strings.Join(kinds, ", "))
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("remember: text is empty; pass the one fact to save. Nothing was saved")
	}
	if err := t.checkSecrets(a.Text); err != nil {
		return "", err
	}

	// dispatch puts the call's session on ctx, so the file records which
	// chat the fact came from.
	source := ""
	if s := dispatch.SessionFrom(ctx); s != "" {
		source = "session " + s
	}
	// Add refuses a text over 4 KiB or not in UTF-8, and says why.
	m, err := t.memory.Add(a.Kind, a.Text, source)
	if err != nil {
		return "", fmt.Errorf("remember: %w. Nothing was saved", err)
	}
	if t.onRemember != nil {
		t.onRemember(ctx)
	}
	return "Saved to " + m.ID + ".", nil
}

// checkSecrets refuses text that holds any value from secrets.toml. It
// reads the file on each call, so a key added a moment ago counts. Redact
// already knows which values count as secrets, so checkSecrets uses it as
// the test: when redacting changes the text, the text holds one. It also
// refuses when it can't read secrets.toml, because then it can't check.
func (t *Tools) checkSecrets(text string) error {
	s, err := secrets.Load(secrets.Path(filepath.Dir(t.configPath)))
	if err != nil {
		return fmt.Errorf("remember: can't check the text against secrets.toml: %w. Nothing was saved", err)
	}
	if s.Redact(text) != text {
		return errors.New("remember: the text holds a secret from secrets.toml, and Meru never saves secrets in memory. Nothing was saved")
	}
	return nil
}

// rememberSchema returns the JSON Schema for remember's arguments, with
// kinds as the choices for kind.
func rememberSchema(kinds []string) json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"enum":        kinds,
				"description": "The memory folder: me for who the user is, preferences for how they like things done.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "One fact, one sentence, about the user in the third person.",
			},
		},
		"required":             []string{"kind", "text"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(s)
	return b
}
