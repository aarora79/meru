// This file holds the "Gmail, Calendar and Drive" step. It does what
// docs/google-setup.md asks the user to do by hand from step 6 on:
// install uv, save the start script with the OAuth client (mode 0700),
// start the server at login with launchd, and add the google entry to
// config.toml. The Google Cloud part, steps 1 to 5, stays in the user's
// browser; the screens walk through it.

package installer

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
)

// GoogleInput is what the user typed on the Google screen. Secret never
// goes back to the page, into a log or into config.toml: only into the
// start script, as the guide keeps it.
type GoogleInput struct {
	Email    string `json:"email"`
	ClientID string `json:"clientID"`
	Secret   string `json:"secret"`
}

// The Google server's launchd job and where it answers.
const (
	googleLabel = "com.meru.workspace-mcp"
	// googleProbe is the server's MCP address. Any HTTP answer there,
	// even an error status, means the server runs.
	googleProbe = "http://127.0.0.1:8000/mcp"
	// googleStartWait covers the first start, when uvx downloads the
	// server before it answers.
	googleStartWait = 3 * time.Minute
)

// check returns the first problem with in, in words for the screen. The
// messages never quote the secret.
func (in GoogleInput) check() error {
	for _, f := range []struct{ name, value string }{
		{"your Google address", in.Email}, {"the client ID", in.ClientID}, {"the client secret", in.Secret},
	} {
		switch {
		case f.value == "":
			return fmt.Errorf("%s is empty", f.name)
		case strings.ContainsAny(f.value, " \t\r\n'\"\\`$"):
			return fmt.Errorf("%s holds a space, a quote or another character it can't hold; paste it again", f.name)
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

// StartScript returns the start script for in, the one in
// docs/google-setup.md, step 7. check has made sure no value holds a
// quote or a space, so single quotes keep each value as it is.
func StartScript(in GoogleInput) string {
	return `#!/bin/sh
# Starts the Google server Meru connects to. Keep this file private.
# The Meru installer wrote it; docs/google-setup.md explains each line.
export GOOGLE_OAUTH_CLIENT_ID='` + in.ClientID + `'
export GOOGLE_OAUTH_CLIENT_SECRET='` + in.Secret + `'
export USER_GOOGLE_EMAIL='` + in.Email + `'
export WORKSPACE_ATTACHMENT_DIR="$HOME/meru-output/attachments"
exec uvx workspace-mcp --transport streamable-http \
  --tool-tier extended --tools gmail calendar drive docs
`
}

// WriteStartScript writes the start script to dir/start.sh with mode
// 0700, so only this user can read the secret in it, and returns its path.
// It writes a new file and renames it over the old one, so the script is
// never readable by others, not even for a moment.
func WriteStartScript(dir string, in GoogleInput) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "start.sh")
	// CreateTemp makes the file with mode 0600; Chmod adds the right to
	// run it.
	tmp, err := os.CreateTemp(dir, ".start-*.sh")
	if err != nil {
		return "", fmt.Errorf("write the start script: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(StartScript(in))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write the start script: %w", werr)
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil { // #nosec G302 -- the owner runs it; nobody else may read it
		return "", fmt.Errorf("write the start script: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("write the start script: %w", err)
	}
	return path, nil
}

// xmlText escapes s for a plist's <string> element.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s)) // a strings.Builder never fails a write
	return b.String()
}

// GooglePlist returns the launchd job that starts the start script at
// login and again if it stops, as docs/google-setup.md, step 11, writes
// it by hand.
func GooglePlist(p Paths) string {
	script := filepath.Join(p.Workspace(), "start.sh")
	log := filepath.Join(p.Workspace(), "server.log")
	path := strings.Join([]string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(p.Home, ".local", "bin"), "/usr/bin", "/bin"}, ":")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- The Meru installer wrote this; docs/google-setup.md, step 11, explains it. -->
<plist version="1.0">
<dict>
  <key>Label</key><string>` + googleLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + xmlText(script) + `</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>` + xmlText(path) + `</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>` + xmlText(log) + `</string>
  <key>StandardErrorPath</key><string>` + xmlText(log) + `</string>
</dict>
</plist>
`
}

// uvxPaths are where uv's installers put uvx.
func uvxPaths(home string) []string {
	return []string{"/opt/homebrew/bin/uvx", "/usr/local/bin/uvx", filepath.Join(home, ".local", "bin", "uvx"), filepath.Join(home, ".cargo", "bin", "uvx")}
}

// hasUV reports whether uvx is installed in one of uvxPaths.
func hasUV(home string) bool {
	for _, p := range uvxPaths(home) {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// loadJob writes plist to the LaunchAgents folder as label.plist and asks
// launchd to load it, unloading any older copy first, so a second run
// picks up a changed job. It returns the plist's path.
func loadJob(ctx context.Context, run Runner, p Paths, label, plist string) (string, error) {
	dir := p.LaunchAgents()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, label+".plist")
	// unload fails when the job isn't loaded, which is fine.
	_, _ = run(ctx, "launchctl", []string{"unload", path}, nil)
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil { // #nosec G306 -- launchd jobs are readable, as launchd expects
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := run(ctx, "launchctl", []string{"load", "-w", path}, nil); err != nil {
		return "", err
	}
	return path, nil
}

// GoogleFound says what of the Google step is in place, for a second run.
func GoogleFound(p Paths, hasEntry bool) string {
	_, err := os.Stat(filepath.Join(p.Workspace(), "start.sh"))
	switch {
	case err == nil && hasEntry:
		return "The start script is in " + p.Tilde(p.Workspace()) + " and config.toml has the google entry."
	case hasEntry:
		return "config.toml has the google entry."
	}
	return ""
}

// SetUpGoogle runs the Google step with in and returns what it did. say
// gets each line of news; none of them holds the secret. It fails when in
// has a problem, uv can't be installed, launchd refuses the job, the
// server doesn't answer within three minutes, or config.toml can't take
// the entry.
func SetUpGoogle(ctx context.Context, run Runner, client *http.Client, p Paths, in GoogleInput, say func(string)) (string, error) {
	in.Email, in.ClientID, in.Secret = strings.TrimSpace(in.Email), strings.TrimSpace(in.ClientID), strings.TrimSpace(in.Secret)
	if err := in.check(); err != nil {
		return "", err
	}
	var done []string

	if !hasUV(p.Home) {
		say("Installing uv with Homebrew: brew install uv")
		if _, err := run(ctx, "brew", []string{"install", "uv"}, say); errors.Is(err, ErrMissing) {
			return "", errors.New("the Google server needs uv, and this Mac has no Homebrew to install it with. " +
				"Install uv from its site, then press Retry")
		} else if err != nil {
			return "", err
		}
		done = append(done, "Installed uv with Homebrew.")
	}

	say("Saving the start script")
	script, err := WriteStartScript(p.Workspace(), in)
	if err != nil {
		return "", err
	}
	done = append(done, "Saved the start script in "+p.Tilde(script)+", which only you can read.")
	if err := os.MkdirAll(filepath.Join(p.Home, "meru-output", "attachments"), 0o700); err != nil {
		return "", fmt.Errorf("create the attachments folder: %w", err)
	}

	say("Starting the Google server at login with launchd")
	plist, err := loadJob(ctx, run, p, googleLabel, GooglePlist(p))
	if err != nil {
		return "", err
	}
	done = append(done, "Set launchd to start it at login: "+p.Tilde(plist)+".")

	say("Waiting for the Google server to answer; its first start downloads it")
	if _, err := waitFor(ctx, googleStartWait, func() (string, error) { return "", probeHTTP(ctx, client, googleProbe) }); err != nil {
		return "", fmt.Errorf("the Google server didn't answer at %s. Its log is %s", googleProbe, p.Tilde(filepath.Join(p.Workspace(), "server.log")))
	}

	msg, err := addGoogleEntry(p)
	if err != nil {
		return "", err
	}
	done = append(done, msg,
		"The first time you ask about your mail, the server gives you a Google sign-in link. Sign in there once. "+
			"Google shows \"Google hasn't verified this app\": it is your own project, so click Advanced, then Go to Meru.")
	return strings.Join(done, "\n"), nil
}

// probeHTTP returns nil when anything answers HTTP at url.
func probeHTTP(ctx context.Context, client *http.Client, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// addGoogleEntry adds the catalog's google entry to config.toml, as `meru
// mcp add google` does, unless config already has one.
func addGoogleEntry(p Paths) (string, error) {
	cfg, err := loadConfig(p.Config())
	if err != nil {
		return "", err
	}
	for _, s := range cfg.MCP.Servers {
		if s.Name == "google" {
			return "config.toml already has the google entry, so the installer left it as it is.", nil
		}
	}
	entry, ok := catalog.Find("google")
	if !ok {
		return "", errors.New("the catalog has no google entry")
	}
	if err := catalog.AppendServer(p.Config(), catalog.Block(entry)); err != nil {
		return "", err
	}
	return "Added the google entry to " + p.Tilde(p.Config()) + ". Sending mail and changing an event ask you first.", nil
}
