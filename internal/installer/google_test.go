// This file tests the Google step: the input checks, the hand-off it
// gives the Start Meru step, with the secret apart from the values, and
// the Adopt choice for a google entry set up by hand. The step runs no
// program and writes no start script, launchd job or config entry.

package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
)

// goodGoogle is a well-formed input. The secret is made up.
func goodGoogle() GoogleInput {
	return GoogleInput{
		Email:    "dana.reyes@example.com",
		ClientID: "12345-abc.apps.googleusercontent.com",
		Secret:   "GOCSPX-madeUpSecretForTests",
	}
}

// TestGoogleInputCheck checks each refusal, and that no message quotes the
// secret.
func TestGoogleInputCheck(t *testing.T) {
	tests := []struct {
		name string
		edit func(*GoogleInput)
		want string // "" means no error
	}{
		{"good", func(*GoogleInput) {}, ""},
		{"no email", func(in *GoogleInput) { in.Email = "" }, "Google address is empty"},
		{"email without @", func(in *GoogleInput) { in.Email = "dana" }, "needs an @"},
		{"wrong client ID", func(in *GoogleInput) { in.ClientID = "12345" }, "apps.googleusercontent.com"},
		{"no secret", func(in *GoogleInput) { in.Secret = "" }, "client secret is empty"},
		{"a space in the secret", func(in *GoogleInput) { in.Secret = "GOCSPX-a b" }, "client secret holds"},
		{"a newline in the secret", func(in *GoogleInput) { in.Secret = "GOCSPX-a\nexport X=1" }, "client secret holds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := goodGoogle()
			tt.edit(&in)
			err := in.check()
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one holding %q", err, tt.want)
			}
			if in.Secret != "" && strings.Contains(err.Error(), in.Secret) {
				t.Errorf("the message quotes the secret: %v", err)
			}
		})
	}
}

// TestSetUpGoogle checks the hand-off: Google turned on, the address and
// client ID as values, the secret apart, and nothing written to config.
// The result never shows the secret.
func TestSetUpGoogle(t *testing.T) {
	p := tempHome(t)
	before := readFile(t, p.Config())
	in := goodGoogle()
	in.Email = "  " + in.Email + " " // pasted with spaces around it
	msg, hand, err := SetUpGoogle(p, ByHand{}, in)
	if err != nil {
		t.Fatal(err)
	}
	if hand == nil || hand.ID != "google" || hand.Adopt || hand.Change == nil {
		t.Fatalf("hand-off = %+v", hand)
	}
	ch := hand.Change
	if ch.Enabled == nil || !*ch.Enabled || ch.Values["email"] != "dana.reyes@example.com" ||
		ch.Values["client_id"] != in.ClientID || ch.Secrets["client_secret"] != in.Secret {
		t.Errorf("change = %+v", ch)
	}
	if _, ok := ch.Values["client_secret"]; ok {
		t.Error("the secret went as a plain value, which would land in config.toml")
	}
	if strings.Contains(msg, in.Secret) {
		t.Error("the result shows the secret")
	}
	if readFile(t, p.Config()) != before {
		t.Error("the step wrote config.toml; merud writes it on the hand-off")
	}
	if _, err := os.Stat(filepath.Join(p.Home, "meru-output", "attachments")); err != nil {
		t.Errorf("the attachments folder: %v", err)
	}
	for _, gone := range []string{filepath.Join(p.Home, ".config", "workspace-mcp"), filepath.Join(p.LaunchAgents(), "com.meru.workspace-mcp.plist")} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("the step wrote %s; merud runs the server now", gone)
		}
	}
	if _, _, err := SetUpGoogle(p, ByHand{}, GoogleInput{Email: "dana"}); err == nil {
		t.Error("a bad input passed")
	}
}

// TestSetUpGoogleByHand checks the two choices for a google entry set up
// by hand: keep it, which hands nothing over, or adopt it, with only the
// values the user gave.
func TestSetUpGoogleByHand(t *testing.T) {
	p := tempHome(t)
	msg, hand, err := SetUpGoogle(p, ByHand{Entry: true}, GoogleInput{})
	if err != nil || hand != nil || !strings.Contains(msg, "stays as it is") {
		t.Errorf("keep: %q, %+v, %v", msg, hand, err)
	}
	msg, hand, err = SetUpGoogle(p, ByHand{Entry: true}, GoogleInput{Adopt: true, Email: "dana@example.com"})
	if err != nil || hand == nil || !hand.Adopt || hand.Change != nil {
		t.Fatalf("adopt: %q, %+v, %v", msg, hand, err)
	}
	if len(hand.Values) != 1 || hand.Values["email"] != "dana@example.com" {
		t.Errorf("values = %v, want the address alone", hand.Values)
	}
}

// TestGoogleFound checks what a second run finds.
func TestGoogleFound(t *testing.T) {
	byHand := config.Config{MCP: config.MCP{Servers: []config.MCPServer{{Name: "google", URL: "http://127.0.0.1:8000/mcp"}}}}
	if got := GoogleFound(byHand); !strings.Contains(got, "set up by hand") {
		t.Errorf("by hand: %q", got)
	}
	on := config.Config{Connectors: map[string]config.Connector{"google": {"enabled": true, "email": "dana@example.com"}}}
	if got := GoogleFound(on); !strings.Contains(got, "dana@example.com") {
		t.Errorf("on: %q", got)
	}
	if got := GoogleFound(config.Config{}); got != "" {
		t.Errorf("nothing: %q", got)
	}
}
