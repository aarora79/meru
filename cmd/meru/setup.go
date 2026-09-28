// This file holds `meru setup` and the "do it for me / show me how" flow it
// shares with `meru mcp add`: the terminal flows that write config.toml and
// secrets.toml for you. The `meru mcp` commands live in mcp.go, the step
// that tries a server first in probe.go, and `meru setup user` in user.go.
// See ARCHITECTURE.md, "First run and setup" and "Adding an MCP server".
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

// restartHint tells the user how to make merud pick up a config change they
// make by hand. A change meru makes itself asks merud to reload instead.
const restartHint = "Restart merud to load the change: pkill merud; merud &\nThen run `meru tools` to see the tools it gives the model."

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
	// answers reports whether something listens at an HTTP server's URL
	// (see urlAnswers).
	answers func(ctx context.Context, rawURL string) bool
	// searxng checks that SearXNG answers JSON at a URL (see
	// catalog.CheckSearXNG).
	searxng func(ctx context.Context, baseURL string) error
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
		answers:       urlAnswers,
		searxng:       catalog.CheckSearXNG,
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

// offer shows one server and lets the user pick a path: do it, show how, or
// skip. It returns true when it wrote the server to config.toml. It skips a
// server config already has.
func (c *console) offer(ctx context.Context, socket string, e catalog.Entry) (bool, error) {
	configPath := configPathFor(socket)
	fmt.Fprintf(c.out, "\n%s: %s\n", e.Title, e.Description)
	if e.Docs != "" {
		fmt.Fprintf(c.out, "  %s\n", e.Docs)
	}
	if e.Remote {
		fmt.Fprintf(c.out, "%s is on another machine. Each tool call sends your data there.\n", e.URL)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return false, err
	}
	for _, s := range cfg.MCP.Servers {
		if s.Name == e.Name {
			fmt.Fprintf(c.out, "%s already has a server named %q; skipping it. To replace it, run meru mcp remove %s first.\n", configPath, e.Name, e.Name)
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
			return c.doIt(ctx, socket, e)
		case "s":
			c.showHow(configPath, e)
			return false, nil
		case "k":
			return false, nil
		}
	}
}

// doIt asks for what e needs, tries the server through merud to learn its
// tools, lets the user pick the ones the model may use, shows the block,
// and after a yes saves the keys to secrets.toml and the block to
// config.toml. Then it asks merud to reload, so the server works at once.
// It returns true when it wrote the block. A key already in secrets.toml
// isn't asked for again.
func (c *console) doIt(ctx context.Context, socket string, e catalog.Entry) (bool, error) {
	configPath := configPathFor(socket)
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
			// A value the entry already has is the default.
			prompt := n.Prompt + ":"
			if cur := env[n.Env]; cur != "" {
				prompt = fmt.Sprintf("%s [Enter keeps %s]:", n.Prompt, cur)
			}
			v, err := c.ask(prompt)
			if err != nil {
				return false, err
			}
			if v == "" && env[n.Env] == "" {
				fmt.Fprintln(c.out, "No answer given, so nothing was written.")
				return false, nil
			}
			if v != "" {
				env[n.Env] = v
			}
		case catalog.NeedNote:
			fmt.Fprintln(c.out, "Note: "+n.Prompt)
		}
	}
	e.Env = env

	// A server the user runs themselves: say how to start it. Meru never
	// runs this command (ARCHITECTURE.md, "MCP").
	if e.Start != "" {
		fmt.Fprintf(c.out, "You start this server; Meru only connects to it. In another terminal, run:\n  %s\n", e.Start)
	}

	// Try the server before writing anything to config.toml. merud starts
	// it and resolves its secret: references, so the keys must be in
	// secrets.toml first. A server the user runs gets tried only if
	// something answers at its URL; if not, the entry keeps the catalog's
	// lists and merud connects on the first question after it starts.
	up := ping(ctx, socket, io.Discard) == nil
	probeIt := up
	if up && e.Start != "" && !c.answers(ctx, e.URL) {
		fmt.Fprintf(c.out, "Nothing answers at %s yet, so Meru writes the catalog's tool list. "+
			"Once you start the server, merud connects to it on your next question.\n", e.URL)
		probeIt = false
	}
	if probeIt {
		if len(pending) > 0 {
			if err := saveSecrets(secretsPath, pending); err != nil {
				return false, err
			}
			// maps.Keys yields the keys in random order; slices.Sorted sorts them.
			fmt.Fprintf(c.out, "Saved %s in %s, readable only by you, so merud can start the server.\n",
				strings.Join(slices.Sorted(maps.Keys(pending)), " and "), secretsPath)
			pending = map[string]string{}
		}
		picked, ok, err := c.probeAndPick(ctx, socket, e)
		if err != nil {
			return false, err
		}
		if !ok {
			fmt.Fprintln(c.out, "Nothing was written to config.toml.")
			return false, nil
		}
		e = picked
	} else if !up {
		fmt.Fprintln(c.out, "merud isn't running, so Meru can't try the server to see its tools first.")
	}

	block := catalog.Block(e)
	fmt.Fprintf(c.out, "\nMeru will add this to the end of %s:\n\n%s\n", configPath, block)
	if len(pending) > 0 {
		names := slices.Sorted(maps.Keys(pending))
		fmt.Fprintf(c.out, "and save %s in %s, readable only by you.\n", strings.Join(names, " and "), secretsPath)
	}
	ok, err := c.yes("Write it?", false)
	if err != nil || !ok {
		switch {
		case err != nil:
		case up:
			// The keys went to secrets.toml before the probe.
			fmt.Fprintln(c.out, "Nothing was written to config.toml.")
		default:
			fmt.Fprintln(c.out, "Nothing was written.")
		}
		return false, err
	}

	if err := saveSecrets(secretsPath, pending); err != nil {
		return false, err
	}
	if err := catalog.AppendServer(configPath, block); err != nil {
		return false, err
	}
	fmt.Fprintf(c.out, "Added %q to %s.\n", e.Name, configPath)
	if !up && e.Install != "" {
		fmt.Fprintln(c.out, e.Install)
	}
	c.reload(ctx, socket, e.Name, up)
	return true, nil
}

// saveSecrets writes each key in pending to secrets.toml. It does nothing
// for an empty map.
func saveSecrets(secretsPath string, pending map[string]string) error {
	for name, v := range pending {
		if err := secrets.Set(secretsPath, name, v); err != nil {
			return err
		}
	}
	return nil
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
// config.toml if there is none, check SearXNG for web search, offer the
// catalog servers, offer `meru setup user`, and ask merud a test question
// when it runs.
//
// main, when not "", is the answer model for a new config.toml, in place of
// the profile question: scripts/install.sh passes the model it picked for
// the Mac's memory (`meru setup --main <model>`), so a fresh install answers
// with it from the start. An existing config.toml stays as it is.
func setupCmd(ctx context.Context, socket string, c *console, main string) error {
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
	switch {
	case haveConfig:
		fmt.Fprintf(c.out, "Profile %q, from %s.\n", profile, configPath)
		if main != "" && main != models.Main {
			fmt.Fprintf(c.out, "To answer with %s, set main = %s under [models] there and restart merud.\n",
				main, tomlString(main))
		}
	case main != "":
		// The lite profile fills the router and embedding tiers; main
		// replaces its answer model, as [models] main does.
		profile = "lite"
		models, _ = config.ProfileModels(profile)
		models.Main = main
		fmt.Fprintf(c.out, "Answer model: %s, picked for this Mac's memory.\n", main)
	default:
		if profile, models, err = c.pickProfile(); err != nil {
			return err
		}
	}
	if err := c.pullModels(ctx, models); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n3. Your files")
	if haveConfig {
		// Setup doesn't edit an existing config.toml. Rewriting it would
		// drop the user's comments, and keeping them needs a TOML editor
		// that Meru doesn't have. Saying what to change is simpler.
		fmt.Fprintf(c.out, "Setup leaves %s as it is. To index folders, list them under [index] folders there and restart merud.\n"+
			"meru config template prints every key with its default, to compare with your file.\n", configPath)
	} else if err := c.writeFirstConfig(configPath, profile, main); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n4. Web search")
	if err := c.checkWebSearch(ctx, cfg.Web.SearXNGURL); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n5. Tools")
	fmt.Fprintln(c.out, "Meru can connect to these servers. Pick a path for each, or skip it and run meru mcp add later.")
	for _, e := range catalog.Entries() {
		if _, err := c.offer(ctx, socket, e); err != nil {
			return err
		}
	}

	fmt.Fprintln(c.out, "\n6. About you")
	merudUp := ping(ctx, socket, io.Discard) == nil
	if err := c.offerProfile(ctx, socket, merudUp); err != nil {
		return err
	}

	fmt.Fprintln(c.out, "\n7. A test question")
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
	if !haveConfig {
		// A server added above works already, because doIt asks merud to
		// reload. A new config.toml's profile and folders need a restart.
		fmt.Fprintln(c.out, "Restart merud to load the new config: pkill merud; merud &")
	}
	return nil
}

// searxngStart holds the commands that start SearXNG in Docker, as
// docs/running.md gives them under "Web search". The .env lines bind it to
// 127.0.0.1:8888; upstream's compose file listens on every interface, port
// 8080, unless told otherwise. setup prints them; Meru never runs them.
const searxngStart = `  mkdir -p ~/srv/searxng/core-config && cd ~/srv/searxng
  curl -fsSL -O https://raw.githubusercontent.com/searxng/searxng/master/container/docker-compose.yml \
       -O https://raw.githubusercontent.com/searxng/searxng/master/container/.env.example
  cp -i .env.example .env && printf 'SEARXNG_HOST=127.0.0.1\nSEARXNG_PORT=8888\n' >> .env
  docker compose up -d`

// checkWebSearch checks that SearXNG answers JSON at baseURL, which is
// [web] searxng_url. When it doesn't, it says why and what to do: the
// container commands when nothing answers, the formats setting when it
// answers HTML. Then it waits: Enter checks again, s skips. Web search is
// optional, so the step never stops setup; it fails only when the input
// ends.
func (c *console) checkWebSearch(ctx context.Context, baseURL string) error {
	if baseURL == "" {
		fmt.Fprintln(c.out, "Web search is off: [web] searxng_url is empty in config.toml.")
		return nil
	}
	for {
		err := c.searxng(ctx, baseURL)
		switch {
		case err == nil:
			fmt.Fprintf(c.out, "SearXNG answers JSON at %s, so the model can search the web.\n", baseURL)
			return nil
		case errors.Is(err, catalog.ErrSearXNGNoJSON):
			fmt.Fprintln(c.out, catalog.SearXNGFormatsHint)
		case errors.Is(err, catalog.ErrSearXNGDown):
			fmt.Fprintf(c.out, "SearXNG isn't answering on %s. Meru searches the web through SearXNG, "+
				"a search engine you run in Docker. To start it:\n\n%s\n\n"+
				"Then turn JSON on, as \"Web search\" in docs/running.md shows.\n", baseURL, searxngStart)
		default:
			fmt.Fprintf(c.out, "%v.\n", err)
		}
		a, err := c.ask("Press Enter to check again, or type s to skip web search:")
		if err != nil {
			return err
		}
		if strings.EqualFold(a, "s") {
			fmt.Fprintln(c.out, "Skipped. Meru works without web search; the web_search tool says what's wrong when the model calls it.")
			return nil
		}
	}
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
		a, err := c.ask("Profile: lite (16 GB of memory) or full (64 GB or more)? [lite]")
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
// config.toml: the config template, with the profile, the answer model
// when main isn't "", and the folders filled in. It asks again when a folder isn't an absolute path or a path under
// ~/.
func (c *console) writeFirstConfig(configPath, profile, main string) error {
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
		text, err := firstConfig(profile, main, folders)
		if err != nil {
			return err
		}
		err = writeNewConfig(configPath, text)
		if err == nil {
			fmt.Fprintf(c.out, "Wrote %s. It lists every setting with its default; change any of them there.\n", configPath)
			return nil
		}
		fmt.Fprintln(c.out, err)
	}
}

// mainLine is the template's [models] main line, which firstConfig
// replaces when setup picks the answer model.
const mainLine = `main  = ""   # lite: hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M; writes the answer`

// firstConfig returns the config template with the profile line and the
// [index] folders line changed, and the [models] main line too when main
// isn't "". Replacing whole lines keeps
// every comment, and needs no TOML editor. It fails when the template no
// longer holds each line exactly once; TestFirstConfig catches that before
// a user can.
func firstConfig(profile, main string, folders []string) (string, error) {
	text := config.Template()
	// A slice of anonymous structs: each pairs a template line with the
	// line that replaces it.
	edits := []struct{ old, new string }{
		{`profile = "lite"`, "profile = " + tomlString(profile)},
		{"folders = []", "folders = " + tomlList(folders)},
	}
	if main != "" {
		edits = append(edits, struct{ old, new string }{mainLine, "main  = " + tomlString(main) + "   # picked for this Mac's memory; writes the answer"})
	}
	for _, r := range edits {
		// The newlines on both sides match a whole line, never the same
		// words inside a comment.
		old := "\n" + r.old + "\n"
		if strings.Count(text, old) != 1 {
			return "", fmt.Errorf("the config template doesn't hold the line %s exactly once", r.old)
		}
		text = strings.Replace(text, old, "\n"+r.new+"\n", 1)
	}
	return text, nil
}

// configTemplateCmd runs `meru config template`: it prints the template
// that setup writes, every key with its default, so a user with a
// config.toml can compare or start over.
func configTemplateCmd(stdout io.Writer) error {
	_, err := io.WriteString(stdout, config.Template())
	return err
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
