// This file holds Adopt and its undo: moving a server the user set up by
// hand, an [[mcp.servers]] entry named obsidian or google, over to its
// connector, and back. Nothing here runs unless the user asks, with meru
// mcp adopt or unadopt; merud answers the connector_adopt and
// connector_unadopt ops with it. See ARCHITECTURE.md, "Moving to
// connectors".
//
// Adopt reads what the connector needs from the old entry (Obsidian's
// vault from --vault) or from the start script docs/google-setup.md has
// the user write (Google's address, OAuth client ID and secret), keeps the
// entry's tool lists, comments the entry out and writes [connectors.<id>]
// in one edit (catalog.AdoptServer), and stops the launchd job that ran
// the old Google server. Unadopt puts all of it back.

package connectors

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// googleJob is the launchd job docs/google-setup.md and the Mac installer
// set up to run the Google server by hand.
const googleJob = "com.meru.workspace-mcp"

// launchctlPath is launchd's command-line tool, at the one place macOS
// keeps it.
const launchctlPath = "/bin/launchctl"

// portFreeAfterStop is how long Adopt and Unadopt wait for port 8000 to
// come free after they stop a server.
const portFreeAfterStop = 10 * time.Second

// Adopter moves hand-added entries over to their connectors and back. The
// zero value isn't usable; merud fills in every field.
type Adopter struct {
	// ConfigPath is config.toml; SecretsPath is secrets.toml.
	ConfigPath  string
	SecretsPath string
	// Home is the user's home folder, where the Google start script and
	// the LaunchAgents folder live.
	Home string
	// Run runs launchctl. merud passes ExecRunner(); tests pass a fake.
	Run Runner
	// UID is the user's ID, for launchctl's gui/<uid> domain, and OS is
	// runtime.GOOS: only macOS has launchd.
	UID int
	OS  string
	// Now gives the date on the marker line.
	Now func() time.Time
}

// AdoptPlan is what an adopt or unadopt will do, worked out before it
// does anything, so the user can read it first. Changes says each step in
// one line, in order.
type AdoptPlan struct {
	ID      string
	Changes []string
	// Nothing is true when there is nothing to do: the entry is adopted
	// already, or restored already. Changes then says why.
	Nothing bool

	table   string            // the [connectors.<id>] table to write
	secrets map[string]string // secrets.toml entries to save
	job     launchdJob        // the old Google server's launchd job
	addr    string            // Google's "host:port", from its manifest's URL
}

// launchdJob is what Adopt found of the launchd job that ran the old
// Google server.
type launchdJob struct {
	plist    string // its file in ~/Library/LaunchAgents
	loaded   bool   // launchd runs it now
	exists   bool   // the plist file exists
	disabled bool   // the file Adopt renamed it to exists (for unadopt)
}

// Adoptable reports whether Meru can adopt an entry for connector m:
// Obsidian and Google, the two catalog servers people set up by hand.
func Adoptable(m Manifest) bool {
	return m.ID == "obsidian" || m.ID == "google"
}

// PlanAdopt works out how to adopt the [[mcp.servers]] entry named m.ID:
// the table to write, the secrets to save, the launchd job to stop. It
// changes nothing. values holds what the user gave on the command line,
// such as Google's email, client_id and client_secret; each takes the
// place of the one Adopt would read.
//
// It fails, with a sentence that says what to do, when the entry isn't
// one the connector can take over: a different Obsidian server, a vault
// folder that doesn't exist, a Google server at another address, a Google
// field Meru can't find, or a program other than the old launchd job on
// port 8000. It never stops that program.
func (a *Adopter) PlanAdopt(ctx context.Context, m Manifest, values map[string]string) (AdoptPlan, error) {
	if !Adoptable(m) {
		return AdoptPlan{}, fmt.Errorf("adopt takes the obsidian and google entries only, not %s", m.ID)
	}
	p := AdoptPlan{ID: m.ID}
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return p, fmt.Errorf("fix config.toml first: %w", err)
	}
	i := slices.IndexFunc(cfg.MCP.Servers, func(s config.MCPServer) bool { return s.Name == m.ID })
	_, hasTable := cfg.Connectors[m.ID]
	switch {
	case i < 0 && hasTable:
		p.Nothing = true
		p.Changes = []string{fmt.Sprintf("%s is adopted already: config.toml has [connectors.%s] and no %s entry in [[mcp.servers]].", m.Name, m.ID, m.ID)}
		return p, nil
	case i < 0:
		return p, fmt.Errorf("config.toml has no %s entry in [[mcp.servers]], so there is nothing to adopt", m.ID)
	case hasTable:
		return p, fmt.Errorf("config.toml has both the %s entry in [[mcp.servers]] and [connectors.%s]. Take [connectors.%s] out, then run meru mcp adopt %s again", m.ID, m.ID, m.ID, m.ID)
	}
	entry := cfg.MCP.Servers[i]

	var fields map[string]string
	var notes []string
	switch m.ID {
	case "obsidian":
		fields, notes, err = obsidianFields(entry)
	case "google":
		// Validate has made sure the manifest's URL has a port.
		p.addr, _ = hostPort(m.Launch.URL)
		fields, p.secrets, notes, err = a.googleFields(entry, values, m.Launch.URL)
	}
	if err != nil {
		return p, err
	}
	if err := checkFieldValues(m, fields, a.Home); err != nil {
		return p, err
	}
	lists := differentLists(m, entry)
	p.table = catalog.ConnectorTable(m.ID, fields, lists)

	if m.ID == "google" {
		p.job, err = a.findJob(ctx)
		if err != nil {
			return p, err
		}
		if Listening(p.addr) && !p.job.loaded {
			return p, fmt.Errorf("another program listens on %s, and it isn't the launchd job %s: most likely the Google server you started by hand. "+
				"Stop it (press Ctrl+C where it runs), then run meru mcp adopt google again. Meru never stops a program it didn't start", p.addr, googleJob)
		}
	}

	p.Changes = append(p.Changes, fmt.Sprintf("Comment out the %s entry in [[mcp.servers]] in %s, between two marker lines.", m.ID, a.ConfigPath))
	p.Changes = append(p.Changes, "Add this after it:\n"+indent(p.table))
	for _, name := range sortedKeys(p.secrets) {
		p.Changes = append(p.Changes, fmt.Sprintf("Save the client secret in %s as %s.", a.SecretsPath, name))
	}
	if p.job.loaded {
		p.Changes = append(p.Changes, fmt.Sprintf("Stop the launchd job %s: launchctl bootout gui/%d/%s.", googleJob, a.UID, googleJob))
	}
	if p.job.exists {
		p.Changes = append(p.Changes, fmt.Sprintf("Rename %s to %s.disabled, so it doesn't start at login.", a.tilde(p.job.plist), filepath.Base(p.job.plist)))
	}
	p.Changes = append(p.Changes, notes...)
	p.Changes = append(p.Changes, fmt.Sprintf("Reload: merud installs %s %s if it isn't there, checks it, and runs it from then on.", m.Name, installVersion(m)))
	return p, nil
}

// Adopt carries out plan: it stops the old launchd job, saves the
// secrets, then comments out the entry and writes the table in one edit
// of config.toml, and calls reload so merud picks the connector up. When
// the edit fails it starts the launchd job again, so the old server runs
// as before. A plan with Nothing to do does nothing.
func (a *Adopter) Adopt(ctx context.Context, plan AdoptPlan, reload func() error) error {
	if plan.Nothing {
		return nil
	}
	undoJob, err := a.stopJob(ctx, plan.job, plan.addr)
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(plan.secrets) {
		// secrets.Set writes secrets.toml with mode 0600.
		if err := secrets.Set(a.SecretsPath, name, plan.secrets[name]); err != nil {
			return errors.Join(err, undoJob(ctx))
		}
	}
	if _, err := catalog.AdoptServer(a.ConfigPath, plan.ID, a.Now(), plan.table); err != nil {
		return errors.Join(err, undoJob(ctx))
	}
	return reload()
}

// PlanUnadopt works out how to undo Adopt for connector m: restore the
// commented-out entry, take out [connectors.<id>], and start the launchd
// job again if Adopt stopped it. The secrets Adopt saved stay in
// secrets.toml, since they do no harm there and the next adopt reads them.
// It changes nothing.
func (a *Adopter) PlanUnadopt(ctx context.Context, m Manifest) (AdoptPlan, error) {
	if !Adoptable(m) {
		return AdoptPlan{}, fmt.Errorf("adopt takes the obsidian and google entries only, not %s", m.ID)
	}
	p := AdoptPlan{ID: m.ID}
	data, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		return p, fmt.Errorf("read config: %w", err)
	}
	if !strings.Contains(string(data), "; meru mcp unadopt "+m.ID+" restores it\n") {
		cfg, err := config.Load(a.ConfigPath)
		if err == nil && slices.ContainsFunc(cfg.MCP.Servers, func(s config.MCPServer) bool { return s.Name == m.ID }) {
			p.Nothing = true
			p.Changes = []string{fmt.Sprintf("%s runs from the %s entry in [[mcp.servers]] already.", m.Name, m.ID)}
			return p, nil
		}
		return p, fmt.Errorf("config.toml holds no %s entry that meru mcp adopt commented out, so there is nothing to restore", m.ID)
	}
	if m.ID == "google" {
		p.addr, _ = hostPort(m.Launch.URL)
		p.job, err = a.findJob(ctx)
		if err != nil {
			return p, err
		}
	}
	p.Changes = append(p.Changes,
		fmt.Sprintf("Put the %s entry back in [[mcp.servers]] in %s, as it was, and take out [connectors.%s].", m.ID, a.ConfigPath, m.ID),
		fmt.Sprintf("Reload: merud stops running %s and connects to the entry again.", m.Name))
	if p.job.disabled {
		p.Changes = append(p.Changes, fmt.Sprintf("Rename %s.disabled back to %s and start the launchd job %s again.",
			a.tilde(p.job.plist), filepath.Base(p.job.plist), googleJob))
	}
	if m.ID == "google" {
		p.Changes = append(p.Changes, fmt.Sprintf("Keep %s in %s; take it out by hand if you like.", SecretName("google", "client_secret"), a.SecretsPath))
	}
	return p, nil
}

// Unadopt carries out plan: it restores the entry, calls reload so the
// supervisor stops the connector and merud connects to the entry again,
// then renames the launchd job's file back and starts it. A plan with
// Nothing to do does nothing.
func (a *Adopter) Unadopt(ctx context.Context, plan AdoptPlan, reload func() error) error {
	if plan.Nothing {
		return nil
	}
	if _, err := catalog.UnadoptServer(a.ConfigPath, plan.ID); err != nil {
		return err
	}
	if err := reload(); err != nil {
		return err
	}
	if !plan.job.disabled {
		return nil
	}
	// The connector's program stops with the reload; its port may take a
	// moment to come free.
	if err := waitPortFree(ctx, plan.addr, portFreeAfterStop); err != nil {
		return fmt.Errorf("the entry is back, but the launchd job %s can't start: %w", googleJob, err)
	}
	return a.startJob(ctx, plan.job.plist)
}

// obsidianFields reads the vault from an obsidian entry that runs the
// npm obsidian-mcp server, through npx, node or its own program, with
// --vault name=path. It returns the connector's values and notes on what
// Adopt doesn't keep. It fails for an entry that runs another server,
// names no vault or more than one, or sets env.
func obsidianFields(e config.MCPServer) (map[string]string, []string, error) {
	base := filepath.Base(e.Command)
	uses := base == "obsidian-mcp" || slices.ContainsFunc(e.Args, func(a string) bool {
		return strings.Contains(a, "obsidian-mcp") && !strings.Contains(a, "mcp-obsidian")
	})
	switch {
	case e.URL != "":
		return nil, nil, fmt.Errorf("the obsidian entry connects to %s; the Obsidian connector runs obsidian-mcp itself, so Meru leaves this entry as it is", e.URL)
	case base == "uvx" || slices.ContainsFunc(e.Args, func(a string) bool { return strings.Contains(a, "mcp-obsidian") }):
		return nil, nil, errors.New("the obsidian entry runs mcp-obsidian, which reaches Obsidian through its Local REST API plugin. " +
			"The Obsidian connector runs a different server, obsidian-mcp, which reads the vault folder, with other tool names, so Meru leaves this entry as it is")
	case !uses || (base != "npx" && base != "node" && base != "obsidian-mcp"):
		return nil, nil, fmt.Errorf("the obsidian entry runs %s %s, not obsidian-mcp through npx or node, so Meru leaves it as it is", e.Command, strings.Join(e.Args, " "))
	case len(e.Env) > 0:
		return nil, nil, fmt.Errorf("the obsidian entry sets env (%s), which the Obsidian connector doesn't take; take env out, or keep the entry as it is", strings.Join(sortedKeys(e.Env), ", "))
	}

	var vaults []string
	for i, a := range e.Args {
		switch {
		case a == "--vault" && i+1 < len(e.Args):
			vaults = append(vaults, e.Args[i+1])
		case strings.HasPrefix(a, "--vault="):
			vaults = append(vaults, strings.TrimPrefix(a, "--vault="))
		}
	}
	switch len(vaults) {
	case 0:
		return nil, nil, errors.New("the obsidian entry names no vault with --vault name=path, so Meru can't tell which folder to use")
	case 1:
	default:
		return nil, nil, fmt.Errorf("the obsidian entry names %d vaults; the Obsidian connector runs one, so Meru leaves the entry as it is", len(vaults))
	}
	name, path, found := strings.Cut(vaults[0], "=")
	if !found {
		name, path = "", vaults[0]
	}
	fields := map[string]string{"vault_path": path}
	if name != "" {
		fields["vault_name"] = name
	}
	var notes []string
	if e.Timeout != "" {
		notes = append(notes, fmt.Sprintf("Drop the entry's timeout = %q: each connector call gets 60 seconds.", e.Timeout))
	}
	return fields, notes, nil
}

// googleFields works out Google's values for an entry that connects to
// the old server at want, the connector's own URL,
// http://127.0.0.1:8000/mcp: the address, OAuth client ID and client
// secret, from values when the user gave them, and otherwise from the
// start script. It returns the non-secret values, the secrets to save and
// notes on what Adopt doesn't keep. It fails for an entry at another
// address, a start script that moves the port, and a value it can't find,
// naming what to give.
func (a *Adopter) googleFields(e config.MCPServer, values map[string]string, want string) (map[string]string, map[string]string, []string, error) {
	if e.Command != "" {
		return nil, nil, nil, fmt.Errorf("the google entry runs %s as a stdio server; the Google connector is the Streamable HTTP server at %s, so Meru leaves this entry as it is", e.Command, want)
	}
	w, _ := url.Parse(want)
	u, err := url.Parse(e.URL)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Port() != w.Port() || strings.TrimSuffix(u.Path, "/") != w.Path {
		return nil, nil, nil, fmt.Errorf("the google entry connects to %s; the Google connector answers at %s, where the sign-in link calls back, so Meru leaves this entry as it is", e.URL, want)
	}

	script := filepath.Join(a.Home, ".config", "workspace-mcp", "start.sh")
	found, err := readStartScript(script)
	haveScript := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, fmt.Errorf("read %s: %w", a.tilde(script), err)
	}
	if port := firstOf(found, "WORKSPACE_MCP_PORT", "PORT"); port != "" && port != "8000" {
		return nil, nil, nil, fmt.Errorf("%s runs the server on port %s; the Google connector uses 8000, where the sign-in link calls back, so Meru leaves the entry as it is", a.tilde(script), port)
	}
	pick := func(field, env string) string {
		if v := strings.TrimSpace(values[field]); v != "" {
			return v
		}
		return found[env]
	}
	fields := map[string]string{
		"email":     pick("email", "USER_GOOGLE_EMAIL"),
		"client_id": pick("client_id", "GOOGLE_OAUTH_CLIENT_ID"),
	}
	secret := pick("client_secret", "GOOGLE_OAUTH_CLIENT_SECRET")

	if fields["email"] == "" || fields["client_id"] == "" || secret == "" {
		where := "found no " + a.tilde(script) + " to read them from"
		if haveScript {
			where = "couldn't find all of them in " + a.tilde(script)
		}
		return nil, nil, nil, fmt.Errorf("the Google connector needs your Google address, your OAuth client ID and its client secret, and Meru %s. "+
			"Run meru mcp adopt google --email <your Google address> --client-id <your client ID>; it asks for the client secret. "+
			"docs/google-setup.md shows where to find them", where)
	}
	var notes []string
	if len(e.Headers) > 0 {
		notes = append(notes, "Drop the entry's headers: the Google connector's server needs none.")
	}
	if e.Timeout != "" {
		notes = append(notes, fmt.Sprintf("Drop the entry's timeout = %q: each connector call gets 60 seconds.", e.Timeout))
	}
	return fields, map[string]string{SecretName("google", "client_secret"): secret}, notes, nil
}

// exportLine matches one "export NAME=value" line of a shell script, with
// the value in single quotes, double quotes, or none.
var exportLine = regexp.MustCompile(`^\s*export\s+([A-Za-z_][A-Za-z0-9_]*)=(?:'([^']*)'|"([^"$\x60\\]*)"|([^\s'"$\x60\\;]*))\s*(?:#.*)?$`)

// readStartScript reads the variables a start script sets with export,
// such as the one docs/google-setup.md has the user write. It reads the
// file as text and runs nothing; a value that needs a shell to work out,
// such as "$HOME/x", is skipped. It fails when the file can't be read.
func readStartScript(path string) (map[string]string, error) {
	f, err := os.Open(path) // #nosec G304 -- the user's own start script, at a fixed place
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := exportLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		out[m[1]] = m[2] + m[3] + m[4]
	}
	return out, sc.Err()
}

// firstOf returns the first of keys that m holds a value for, or "".
func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// checkFieldValues checks the values Adopt found against m's fields, so
// Adopt never writes a table the connector would refuse: a folder must
// exist, an email needs an "@", and a value must match its pattern. A
// folder written "~/…" counts from home, as the supervisor reads it.
func checkFieldValues(m Manifest, values map[string]string, home string) error {
	for _, f := range m.Fields {
		v := values[f.ID]
		switch {
		case v == "":
		case f.Type == FieldFolder && !isFolder(expandHome(v, home)):
			return fmt.Errorf("the %s %s doesn't exist or isn't a folder, so Meru leaves the entry as it is", lowerFirst(f.Label), v)
		case f.Type == FieldEmail && !strings.Contains(v, "@"):
			return fmt.Errorf("%q isn't an email address, so Meru can't use it as your %s", v, lowerFirst(f.Label))
		case f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(v):
			return fmt.Errorf("the %s %q doesn't fit the form %s needs (%s)", lowerFirst(f.Label), v, m.Name, f.Pattern)
		}
	}
	return nil
}

// differentLists returns the tool lists of the entry e that differ from
// connector m's own, for the table, so the tools the user chose stay as
// they were. A list that matches the manifest's stays out of the table.
func differentLists(m Manifest, e config.MCPServer) map[string][]string {
	out := map[string][]string{}
	for _, l := range []struct {
		key        string
		have, want []string
	}{
		{"allow", e.Allow, m.MCP.Allow},
		{"confirm", e.Confirm, m.MCP.Confirm},
		{"always_confirm", e.AlwaysConfirm, m.MCP.AlwaysConfirm},
	} {
		// slices.Equal counts a nil list and an empty one as equal.
		if !slices.Equal(l.have, l.want) {
			out[l.key] = append([]string{}, l.have...)
		}
	}
	return out
}

// findJob looks for the old Google server's launchd job: its plist file,
// the file Adopt renames it to, and whether launchd runs it now. Off
// macOS there is no launchd, and it finds nothing.
func (a *Adopter) findJob(ctx context.Context) (launchdJob, error) {
	if a.OS != "darwin" {
		return launchdJob{}, nil
	}
	j := launchdJob{plist: filepath.Join(a.Home, "Library", "LaunchAgents", googleJob+".plist")}
	_, err := os.Stat(j.plist)
	j.exists = err == nil
	_, err = os.Stat(j.plist + ".disabled")
	j.disabled = err == nil
	// launchctl print fails when launchd has no such job loaded.
	_, err = a.Run(ctx, a.launchctl("print", a.domain()+"/"+googleJob), nil)
	j.loaded = err == nil
	return j, nil
}

// stopJob stops the old Google server's launchd job j, if launchd runs
// it, renames its plist to .disabled so it doesn't start at the next
// login, and waits for addr, the connector's port, to come free. It
// returns the function that undoes both, for when a later step fails. It
// fails, having undone what it did, when launchctl refuses or the port
// stays taken. For Obsidian, whose addr is empty, it does nothing.
func (a *Adopter) stopJob(ctx context.Context, j launchdJob, addr string) (undo func(context.Context) error, err error) {
	undo = func(context.Context) error { return nil }
	if addr == "" {
		return undo, nil
	}
	if !j.loaded && !j.exists {
		// No job: nothing to stop. The port must be free, as the plan
		// found it; check again, since time has passed.
		if Listening(addr) {
			return undo, fmt.Errorf("another program listens on %s now; stop it, then run meru mcp adopt google again", addr)
		}
		return undo, nil
	}
	if j.loaded {
		if _, err := a.Run(ctx, a.launchctl("bootout", a.domain()+"/"+googleJob), nil); err != nil {
			return undo, fmt.Errorf("stop the launchd job %s: %w", googleJob, err)
		}
	}
	renamed := false
	if j.exists {
		if err := os.Rename(j.plist, j.plist+".disabled"); err != nil {
			return undo, errors.Join(fmt.Errorf("rename %s: %w", j.plist, err), a.restartIf(ctx, j.loaded, j.plist))
		}
		renamed = true
	}
	undo = func(ctx context.Context) error {
		if renamed {
			if err := os.Rename(j.plist+".disabled", j.plist); err != nil {
				return fmt.Errorf("rename %s back: %w", j.plist, err)
			}
		}
		return a.restartIf(ctx, j.loaded, j.plist)
	}
	if err := waitPortFree(ctx, addr, portFreeAfterStop); err != nil {
		return undo, errors.Join(fmt.Errorf("the launchd job %s stopped, but %w", googleJob, err), undo(ctx))
	}
	return undo, nil
}

// restartIf starts the launchd job from plist again when was is true.
func (a *Adopter) restartIf(ctx context.Context, was bool, plist string) error {
	if !was {
		return nil
	}
	return a.startJob(ctx, plist)
}

// startJob renames plist's .disabled file back, when there is one, and
// asks launchd to load and start the job.
func (a *Adopter) startJob(ctx context.Context, plist string) error {
	if _, err := os.Stat(plist + ".disabled"); err == nil {
		if err := os.Rename(plist+".disabled", plist); err != nil {
			return fmt.Errorf("rename %s back: %w", plist, err)
		}
	}
	if _, err := a.Run(ctx, a.launchctl("bootstrap", a.domain(), plist), nil); err != nil {
		return fmt.Errorf("start the launchd job %s: %w", googleJob, err)
	}
	return nil
}

// launchctl builds the Cmd that runs launchctl with args, with only HOME
// in its environment.
func (a *Adopter) launchctl(args ...string) Cmd {
	return Cmd{Path: launchctlPath, Args: args, Env: []string{"HOME=" + a.Home}}
}

// domain is launchd's name for the user's own jobs: gui/<uid>.
func (a *Adopter) domain() string {
	return "gui/" + strconv.Itoa(a.UID)
}

// tilde writes path with the home folder as "~", for a line the user
// reads.
func (a *Adopter) tilde(path string) string {
	if rest, ok := strings.CutPrefix(path, a.Home+string(filepath.Separator)); ok && a.Home != "" {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}

// indent puts four spaces in front of each line of text, for a table
// inside a list of changes.
func indent(text string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// sortedKeys returns m's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
