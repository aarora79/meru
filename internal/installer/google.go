// This file holds the "Gmail, Calendar and Drive" step. The Google Cloud
// part, docs/google-setup.md's steps 1 to 5, stays in the user's browser,
// and the screens walk through it. The step then takes the address, the
// OAuth client ID and its secret, and hands them to the Start Meru step,
// which asks merud to turn the Google connector on with them; merud
// installs the pinned workspace-mcp with its own uv and runs it. A google
// entry the user set up by hand, with its start script and launchd job,
// stays as it is unless the user picks Adopt. See ARCHITECTURE.md,
// "Installer" and "Google".

package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/rpc"
)

// GoogleInput is what the user typed or chose on the Google screen.
// Secret never goes back to the page, into a log or into config.toml: only
// to merud, which keeps it in secrets.toml. Adopt moves a google entry set
// up by hand over to the connector; the three values are then optional,
// for an entry whose start script merud can't read.
type GoogleInput struct {
	Email    string `json:"email"`
	ClientID string `json:"clientID"`
	Secret   string `json:"secret"`
	Adopt    bool   `json:"adopt"`
}

// check returns the first problem with in, in words for the screen. The
// messages never quote the secret. merud checks the same rules again.
func (in GoogleInput) check() error {
	for _, f := range []struct{ name, value string }{
		{"your Google address", in.Email}, {"the client ID", in.ClientID}, {"the client secret", in.Secret},
	} {
		switch {
		case f.value == "":
			return fmt.Errorf("%s is empty", f.name)
		case strings.ContainsAny(f.value, " \t\r\n"):
			return fmt.Errorf("%s holds a space or a line break; paste it again", f.name)
		}
	}
	if !strings.Contains(in.Email, "@") {
		return errors.New("your Google address needs an @")
	}
	if !strings.HasSuffix(in.ClientID, ".apps.googleusercontent.com") {
		return errors.New("the client ID should end in .apps.googleusercontent.com; copy it again from the Clients page")
	}
	return nil
}

// GoogleFound says what of the Google step is in place, for a second run,
// or "" when nothing is.
func GoogleFound(cfg config.Config) string {
	st := connectorState(cfg, "google")
	switch {
	case st.Entry:
		return "config.toml has a google entry you set up by hand. It keeps working; Adopt lets Meru run the server instead."
	case st.On:
		email, _ := cfg.Connectors["google"].Value("email")
		return "Meru runs the Google server for " + email + "."
	}
	return ""
}

// SetUpGoogle runs the Google step with in, given the connector's state in
// config.toml, and returns what it did, with the hand-off for the Start
// Meru step. An entry set up by hand is adopted when in says so, with any
// values in gave, and left alone otherwise. It fails when a value has a
// problem. It makes the attachments folder the server saves mail
// attachments in.
func SetUpGoogle(p Paths, st ByHand, in GoogleInput) (string, *HandOff, error) {
	in.Email, in.ClientID, in.Secret = strings.TrimSpace(in.Email), strings.TrimSpace(in.ClientID), strings.TrimSpace(in.Secret)
	if st.Entry {
		if !in.Adopt {
			return "Your own google entry stays as it is. Meru.app's Settings, Connections, can move it over later.", nil, nil
		}
		values := map[string]string{}
		for k, v := range map[string]string{"email": in.Email, "client_id": in.ClientID, "client_secret": in.Secret} {
			if v != "" {
				values[k] = v
			}
		}
		return "When Meru starts, merud moves your google entry over to its connector: it reads the address, client ID and secret from your start script, stops the launchd job that runs the server, and runs the server itself on the same port, so your sign-in still works.",
			&HandOff{ID: "google", Name: "Google", Adopt: true, Values: values}, nil
	}
	if err := in.check(); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Join(p.Home, "meru-output", "attachments"), 0o700); err != nil {
		return "", nil, fmt.Errorf("create the attachments folder: %w", err)
	}
	on := true
	hand := &HandOff{ID: "google", Name: "Google", Change: &rpc.ConnectorChange{
		Enabled: &on,
		Values:  map[string]string{"email": in.Email, "client_id": in.ClientID},
		Secrets: map[string]string{"client_secret": in.Secret},
	}}
	return "When Meru starts, merud installs workspace-mcp at the version pinned in this release, with its own uv and Python, " +
		"saves the client secret in ~/.meru/secrets.toml, which only you can read, and starts the server on 127.0.0.1:8000. " +
		"Then it gives you a Google sign-in link; sign in there once. Google shows \"Google hasn't verified this app\": " +
		"it is your own project, so click Advanced, then Go to Meru.", hand, nil
}
