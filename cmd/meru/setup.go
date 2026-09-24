// This file holds `meru setup` and `meru mcp add`: the terminal flows that
// write config.toml and secrets.toml for you. `meru setup user` lives in
// user.go. See ARCHITECTURE.md, "First
// run and setup" and "Adding an MCP server".
//
// Both flows run in the thin client, because they talk to a person, not to
// a model. They touch files and Ollama's command line; they never import
// the engine or the store. The server list and the config writer live in
// internal/catalog, which the configure tool in merud shares.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/secrets"
)

// restartHint tells the user how to make merud pick up a config change.
// merud reads config.toml only when it starts.
const restartHint = "Restart merud to start it: pkill merud; merud &\nThen run `meru tools` to see the tools it gives the model."

// testQuestion is the question setup asks merud at the end.
const testQuestion = "In one sentence, what can you help me with?"

// console is the terminal a setup flow talks to. The fields that touch the
// real world are functions, so tests can swap in scripted input, a fake
// `ollama pull` and a fake Ollama check.
type console struct {
	in  *bufio.Reader
	out io.Writer
	// readSecret reads one line without showing it on screen, when the
	// input is a terminal.
	readSecret func() (string, error)
	// run starts a program with the terminal attached, so its own progress
	// bar shows.
	run func(ctx context.Context, name string, args ...string) error
	// ollamaVersion asks the Ollama at baseURL for its version.
	ollamaVersion func(ctx context.Context, baseURL string) (string, error)
}

// terminal returns a console on standard input and out. When standard input
// is a terminal, readSecret turns echo off with golang.org/x/term; when it
// is a pipe, nothing shows on screen anyway, so it reads a plain line.
func terminal(out io.Writer) *console {
	c := &console{
		in:            bufio.NewReader(os.Stdin),
		out:           out,
		run:           runCommand,
		ollamaVersion: ollamaVersion,
	}
	c.readSecret = c.line
	fd := int(os.Stdin.Fd()) // #nosec G115 -- a file descriptor fits in an int
	if term.IsTerminal(fd) {
		c.readSecret = func() (string, error) {
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(c.out) // the Enter key didn't echo either
			return strings.TrimSpace(string(b)), err
		}
	}
	return c
}

// line reads one line of input without its line ending. It fails when the
// input ends before any text arrives.
func (c *console) line() (string, error) {
	s, err := c.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || s == "") {
		return "", errors.New("input ended")
	}
	return strings.TrimSpace(s), nil
}

// ask prints prompt and returns the answer, trimmed.
func (c *console) ask(prompt string) (string, error) {
	fmt.Fprint(c.out, prompt+" ")
	return c.line()
}

// yes asks a yes-or-no question. An empty answer gives def.
func (c *console) yes(prompt string, def bool) (bool, error) {
	hint := " [y/N]"
	if def {
		hint = " [Y/n]"
	}
	for {
		a, err := c.ask(prompt + hint)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(a) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// configPathFor returns config.toml in the same folder as the socket. merud
// puts its socket next to its config file, so `meru -socket` also picks the
// config that setup edits.
func configPathFor(socket string) string {
	return filepath.Join(filepath.Dir(socket), "config.toml")
}

// mcpCmd runs `meru mcp ...`. args are the words after "mcp":
//
//	list-catalog                       list the catalog
//	add                                list the catalog
//	add <catalog-name>                 add a catalog server
//	add <name> -- <command> [args...]  add a stdio server of your own
//	add <name> --url <url>             add a Streamable HTTP server of your own
func mcpCmd(ctx context.Context, socket string, args []string, c *console) error {
	usage := errors.New("usage: meru mcp add <name> | meru mcp add <name> -- <command> [args...] | meru mcp add <name> --url <url> | meru mcp list-catalog")
	switch {
	case len(args) == 1 && (args[0] == "list-catalog" || args[0] == "add"):
		listCatalog(c.out)
		return nil
	case len(args) < 2 || args[0] != "add":
		return usage
	}

	name := args[1]
	var e catalog.Entry
	switch {
	case len(args) == 2:
		var ok bool
		if e, ok = catalog.Find(name); !ok {
			return fmt.Errorf("%q is not in the catalog; run `meru mcp list-catalog`, or give its command: meru mcp add %s -- <command> [args...]", name, name)
		}
	case args[2] == "--" && len(args) >= 4:
		e = catalog.Custom(name, args[3], args[4:])
	case (args[2] == "--url" || args[2] == "-url") && len(args) == 4:
		if !strings.HasPrefix(args[3], "http://") && !strings.HasPrefix(args[3], "https://") {
			return fmt.Errorf("url %q must start with http:// or https://", args[3])
		}
		e = catalog.Custom(name, args[3], nil)
	default:
		return usage
	}
	if err := catalog.CheckName(e.Name); err != nil {
		return err
	}
	_, err := c.offer(configPathFor(socket), e)
	return err
}

// listCatalog prints each catalog entry on one line.
func listCatalog(out io.Writer) {
	fmt.Fprintln(out, "Servers Meru knows how to add (meru mcp add <name>):")
	for _, e := range catalog.Entries() {
		fmt.Fprintf(out, "  %-9s %s: %s\n", e.Name, e.Title, e.Description)
	}
	fmt.Fprintln(out, "For another server: meru mcp add <name> -- <command> [args...], or meru mcp add <name> --url <url>")
}

// offer shows one server and lets the user pick a path: do it, show how, or
// skip. It returns true when it wrote the server to config.toml. It skips a
// server config already has.
func (c *console) offer(configPath string, e catalog.Entry) (bool, error) {
	fmt.Fprintf(c.out, "\n%s: %s\n", e.Title, e.Description)
	if e.Docs != "" {
		fmt.Fprintf(c.out, "  %s\n", e.Docs)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return false, err
	}
	for _, s := range cfg.MCP.Servers {
		if s.Name == e.Name {
			fmt.Fprintf(c.out, "%s already has a server named %q; skipping it.\n", configPath, e.Name)
			return false, nil
		}
	}
	for {
		a, err := c.ask("d) do it for me   s) show me how   k) skip   [d/s/k]")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(a) {
		case "d":
			return c.doIt(configPath, e)
		case "s":
			c.showHow(configPath, e)
			return false, nil
		case "k":
			return false, nil
		}
	}
}

// doIt asks for what e needs, shows the block, and after a yes saves the
// keys to secrets.toml and the block to config.toml. It returns true when
// it wrote the block. A key already in secrets.toml isn't asked for again.
func (c *console) doIt(configPath string, e catalog.Entry) (bool, error) {
	secretsPath := secrets.Path(filepath.Dir(configPath))
	saved, err := secrets.Load(secretsPath)
	if err != nil {
		return false, err
	}
	// Copy Env before adding to it. maps.Clone would return nil for an
	// entry with no env, and writing to a nil map panics.
	env := map[string]string{}
	for k, v := range e.Env {
		env[k] = v
	}
	pending := map[string]string{} // keys to save after the yes

	for _, n := range e.Needs {
		switch n.Kind {
		case catalog.NeedAPIKey:
			if saved.Has(n.SecretName) || pending[n.SecretName] != "" {
				fmt.Fprintf(c.out, "Using %s, already in %s.\n", n.SecretName, secretsPath)
				continue
			}
			if n.Help != "" {
				fmt.Fprintln(c.out, n.Help)
			}
			fmt.Fprint(c.out, n.Prompt+" (it won't show as you type): ")
			v, err := c.readSecret()
			if err != nil {
				return false, err
			}
			if v == "" {
				fmt.Fprintln(c.out, "No key given, so nothing was written.")
				return false, nil
			}
			pending[n.SecretName] = v
		case catalog.NeedPath, catalog.NeedURL:
			if n.Help != "" {
				fmt.Fprintln(c.out, n.Help)
			}
			v, err := c.ask(n.Prompt + ":")
			if err != nil {
				return false, err
			}
			if v == "" {
				fmt.Fprintln(c.out, "No answer given, so nothing was written.")
				return false, nil
			}
			env[n.Env] = v
		case catalog.NeedNote:
			fmt.Fprintln(c.out, "Note: "+n.Prompt)
		}
	}
	e.Env = env

	block := catalog.Block(e)
	fmt.Fprintf(c.out, "\nMeru will add this to the end of %s:\n\n%s\n", configPath, block)
	if len(pending) > 0 {
		// maps.Keys yields the keys in random order; slices.Sorted sorts them.
		names := slices.Sorted(maps.Keys(pending))
		fmt.Fprintf(c.out, "and save %s in %s, readable only by you.\n", strings.Join(names, " and "), secretsPath)
	}
	ok, err := c.yes("Write it?", false)
	if err != nil || !ok {
		if err == nil {
			fmt.Fprintln(c.out, "Nothing was written.")
		}
		return false, err
	}

	for name, v := range pending {
		if err := secrets.Set(secretsPath, name, v); err != nil {
			return false, err
		}
	}
	if err := catalog.AppendServer(configPath, block); err != nil {
		return false, err
	}
	fmt.Fprintf(c.out, "Added %q to %s.\n", e.Name, configPath)
	if e.Install != "" {
		fmt.Fprintln(c.out, e.Install)
	}
	fmt.Fprintln(c.out, restartHint)
	return true, nil
}

// showHow prints what to install, the block and where it goes, and the
// secrets.toml lines to add. It writes nothing.
func (c *console) showHow(configPath string, e catalog.Entry) {
	if e.Install != "" {
		fmt.Fprintf(c.out, "\nFirst: %s\n", e.Install)
	}
	fmt.Fprintf(c.out, "\nAdd this to the end of %s:\n\n%s\n", configPath, catalog.Block(e))
	if names := e.SecretNames(); len(names) > 0 {
		secretsPath := secrets.Path(filepath.Dir(configPath))
		fmt.Fprintf(c.out, "Add these lines to %s, then run chmod 600 %s:\n\n", secretsPath, secretsPath)
		for _, name := range names {
			fmt.Fprintf(c.out, "%s = \"<paste it here>\"\n", name)
		}
		fmt.Fprintln(c.out)
	}
	for _, n := range e.Needs {
		if n.Help != "" && n.Kind != catalog.NeedNote {
			fmt.Fprintf(c.out, "%s: %s\n", n.Prompt, n.Help)
		}
		if n.Kind == catalog.NeedNote {
			fmt.Fprintln(c.out, "Note: "+n.Prompt)
		}
	}
	fmt.Fprintln(c.out, restartHint)
}

// setupCmd runs `meru setup`: check Ollama, download the models, write
// config.toml if there is none, offer the catalog servers, offer `meru
// setup user`, and ask merud a test question when it runs.
func setupCmd(ctx context.Context, socket string, c *console) error {
	configPath := configPathFor(socket)
	_, statErr := os.Stat(configPath)
	haveConfig := statErr == nil
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("%w\nFix config.toml, then run meru setup again", err)
	}

	fmt.Fprintln(c.out, "1. Ollama")
	if err := c.waitForOllama(ctx, cfg.Ollama.BaseURL); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n2. Models")
	profile, models := cfg.Profile, cfg.Models
	if haveConfig {
		fmt.Fprintf(c.out, "Profile %q, from %s.\n", profile, configPath)
	} else if profile, models, err = c.pickProfile(); err != nil {
		return err
	}
	if err := c.pullModels(ctx, models); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n3. Your files")
	if haveConfig {
		// Setup doesn't edit an existing config.toml. Rewriting it would
		// drop the user's comments, and keeping them needs a TOML editor
		// that Meru doesn't have. Saying what to change is simpler.
		fmt.Fprintf(c.out, "Setup leaves %s as it is. To index folders, list them under [index] folders there and restart merud.\n", configPath)
	} else if err := c.writeFirstConfig(configPath, profile); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n4. Tools")
	fmt.Fprintln(c.out, "Meru can connect to these servers. Pick a path for each, or skip it and run meru mcp add later.")
	added := 0
	for _, e := range catalog.Entries() {
		wrote, err := c.offer(configPath, e)
		if err != nil {
			return err
		}
		if wrote {
			added++
		}
	}

	fmt.Fprintln(c.out, "\n5. About you")
	merudUp := ping(ctx, socket, io.Discard) == nil
	if err := c.offerProfile(ctx, socket, merudUp); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n6. A test question")
	if !merudUp {
		fmt.Fprintln(c.out, "merud isn't running. Start it with `merud &`, then ask it something: meru \"hello\"")
		return nil
	}
	fmt.Fprintf(c.out, "merud is running. Asking: %s\n", testQuestion)
	// The test question needs no tools, so a nil approve denies any the
	// model tries.
	if err := ask(ctx, socket, testQuestion, c.out, c.out, nil); err != nil {
		return err
	}
	if added > 0 || !haveConfig {
		fmt.Fprintln(c.out, "Restart merud to load the new config: pkill merud; merud &")
	}
	return nil
}

// offerProfile asks whether to run `meru setup user` now, when merud is up
// to save the answers. Without merud it says to run it later.
func (c *console) offerProfile(ctx context.Context, socket string, merudUp bool) error {
	if !merudUp {
		fmt.Fprintln(c.out, "Once merud runs, tell Meru who you are with meru setup user.")
		return nil
	}
	ok, err := c.yes("Tell Meru who you are, so it can tell you apart from people in your files?", true)
	if err != nil || !ok {
		return err
	}
	return setupUserCmd(ctx, socket, c)
}

// waitForOllama checks that Ollama answers at baseURL. When it doesn't, it
// prints how to install it and waits for the user to press Enter, then
// checks again. It fails when the user types q.
func (c *console) waitForOllama(ctx context.Context, baseURL string) error {
	for {
		v, err := c.ollamaVersion(ctx, baseURL)
		if err == nil {
			fmt.Fprintf(c.out, "Ollama %s is running at %s.\n", v, baseURL)
			return nil
		}
		fmt.Fprintf(c.out, "Ollama isn't answering at %s.\n%s\n", baseURL, ollamaInstallHint(runtime.GOOS))
		a, err := c.ask("Start Ollama, then press Enter to check again, or type q to stop:")
		if err != nil {
			return err
		}
		if strings.EqualFold(a, "q") {
			return errors.New("setup stopped: Ollama isn't running")
		}
	}
}

// ollamaInstallHint returns the one install step for goos.
func ollamaInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Install it with `brew install ollama`, or from https://ollama.com/download, then open the Ollama app."
	case "linux":
		return "Install it with: curl -fsSL https://ollama.com/install.sh | sh"
	default:
		return "Install it from https://ollama.com/download, then start it."
	}
}

// ollamaVersion asks Ollama for its version with GET /api/version. The
// check lives here, not in the engine package, because the thin client
// must not import the engine; one plain HTTP call is all it needs. It fails
// when Ollama doesn't answer within five seconds or answers with an error.
func ollamaVersion(ctx context.Context, baseURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama answered %s", resp.Status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return "", fmt.Errorf("read ollama version: %w", err)
	}
	return body.Version, nil
}

// pickProfile asks for lite or full and returns it with its models.
func (c *console) pickProfile() (string, config.Models, error) {
	for {
		a, err := c.ask("Profile: lite (16 GB of memory) or full (32 GB or more)? [lite]")
		if err != nil {
			return "", config.Models{}, err
		}
		if a == "" {
			a = "lite"
		}
		if m, ok := config.ProfileModels(strings.ToLower(a)); ok {
			return strings.ToLower(a), m, nil
		}
	}
}

// pullModels runs `ollama pull` for each distinct model, after a yes.
// Ollama prints its own progress bar.
func (c *console) pullModels(ctx context.Context, m config.Models) error {
	var names []string
	for _, n := range []string{m.Fast, m.Main, m.Embed} {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	fmt.Fprintf(c.out, "Meru uses %s.\n", strings.Join(names, ", "))
	ok, err := c.yes("Download them now with ollama pull?", true)
	if err != nil || !ok {
		return err
	}
	for _, n := range names {
		if err := c.run(ctx, "ollama", "pull", n); err != nil {
			return fmt.Errorf("ollama pull %s: %w", n, err)
		}
	}
	return nil
}

// writeFirstConfig asks which folders to index and writes a new
// config.toml with the profile and the folders. It asks again when a folder
// isn't an absolute path or a path under ~/.
func (c *console) writeFirstConfig(configPath, profile string) error {
	for {
		a, err := c.ask("Folders to index, separated by commas (for example ~/notes), or Enter for none:")
		if err != nil {
			return err
		}
		var folders []string
		for _, f := range strings.Split(a, ",") {
			if f = strings.TrimSpace(f); f != "" {
				folders = append(folders, f)
			}
		}
		text := "# Written by meru setup. config.example.toml in the Meru repo lists every key.\n" +
			"profile = " + tomlString(profile) + "\n\n[index]\nfolders = " + tomlList(folders) + "\n"
		err = writeNewConfig(configPath, text)
		if err == nil {
			fmt.Fprintf(c.out, "Wrote %s.\n", configPath)
			return nil
		}
		fmt.Fprintln(c.out, err)
	}
}

// writeNewConfig writes text as config.toml through a temporary file that
// config.Load must accept first, so a bad folder never lands in the file.
// It fails when the text doesn't load or the file can't be written.
func writeNewConfig(configPath, text string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close() // the write error is the one worth reporting
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := config.Load(tmp.Name()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), configPath)
}

// tomlString quotes s as a TOML string. JSON string escapes are a subset of
// TOML's, so encoding/json does the escaping.
func tomlString(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b)
}

// tomlList writes items as a TOML array of strings.
func tomlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// runCommand runs name with args, attached to this terminal, and waits for
// it to finish.
func runCommand(ctx context.Context, name string, args ...string) error {
	// #nosec G204 -- setup runs `ollama pull` with model names from config
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
