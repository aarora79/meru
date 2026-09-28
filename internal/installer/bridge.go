// This file holds the Bridge, the one Go value the installer's window
// binds: the page calls its methods by name to read each screen, run or
// skip a step, pick a folder and open a link. The Bridge asks Flow before
// each step and sends every line of progress to the page as the event
// "installer:progress".

package installer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/catalog"
	"github.com/aarora79/meru/internal/config"
)

// ProgressEvent is the name of the event that carries each Progress.
const ProgressEvent = "installer:progress"

// Progress is one line of news from a running step. Fraction is between 0
// and 1 while a download has a known size, and -1 otherwise.
type Progress struct {
	Step     string  `json:"step"`
	Line     string  `json:"line"`
	Fraction float64 `json:"fraction"`
}

// Options is what the Bridge needs from the window and the Mac. Tests fill
// it with fakes; DefaultOptions fills it for real.
type Options struct {
	// Home is the user's home folder. Every path the installer writes
	// sits under it, except Meru.app and Ollama.app in Apps.
	Home string
	// Payload is the folder inside the installer's bundle that holds
	// meru, merud and Meru.app.
	Payload string
	// Apps is the Applications folder the apps go in.
	Apps string
	// Run runs one program from the allowlist.
	Run Runner
	// Local reaches services on this Mac: Ollama, SearXNG and the Google
	// server. Web reaches the internet, for the Ollama download only.
	Local, Web *http.Client
	// Emit sends an event to the page; PickFolder shows the system's
	// folder dialog and returns "" on a cancel; Quit closes the window.
	// nil turns each off.
	Emit       func(name string, data any)
	PickFolder func() (string, error)
	Quit       func()
}

// DefaultOptions returns the Options for the user running the installer,
// with the payload in the app bundle's Resources folder: the program runs
// as Install Meru.app/Contents/MacOS/meru-installer. It fails only when the
// home folder or the program's own path can't be found.
func DefaultOptions() (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, fmt.Errorf("find the home folder: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return Options{}, fmt.Errorf("find the installer's own folder: %w", err)
	}
	return Options{
		Home:    home,
		Payload: filepath.Join(filepath.Dir(exe), "..", "Resources", "payload"),
		Apps:    AppsDir(home),
		Run:     ExecRunner(home),
		// A Transport with Proxy nil goes straight to 127.0.0.1, even when
		// the user has a proxy set for the web.
		Local: &http.Client{Transport: &http.Transport{Proxy: nil}},
		Web:   &http.Client{},
	}, nil
}

// Bridge is what the page calls. Build one with New.
type Bridge struct {
	o     Options
	paths Paths
	flow  *Flow
	// ctx ends when the window closes, which stops a running step.
	ctx    context.Context
	cancel context.CancelFunc

	// mu guards the fields below it, which steps fill in for later ones.
	mu sync.Mutex
	// machine is what the first step found, and choice the model set the
	// user picked there.
	machine *Machine
	choice  int
}

// New returns a Bridge for o.
func New(o Options) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{o: o, paths: Paths{Home: o.Home}, flow: NewFlow(), ctx: ctx, cancel: cancel, choice: -1}
}

// ServiceShutdown stops a running step. Wails calls it when the window
// closes.
func (b *Bridge) ServiceShutdown() error {
	b.cancel()
	return nil
}

// View is what the page draws the step list and the summary from.
type View struct {
	Steps []Step `json:"steps"`
	// Config is config.toml's path as the screen shows it, such as
	// ~/.meru/config.toml, and ConfigExists whether it is there yet.
	Config       string `json:"config"`
	ConfigExists bool   `json:"configExists"`
	// Apps is where Meru.app goes.
	Apps string `json:"apps"`
}

// State returns the steps as they stand.
func (b *Bridge) State() View {
	_, err := os.Stat(b.paths.Config())
	return View{Steps: b.flow.Steps(), Config: b.paths.Tilde(b.paths.Config()), ConfigExists: err == nil, Apps: b.paths.Tilde(b.o.Apps)}
}

// Detect looks for what an earlier run, or the user, already set up, and
// notes it on each step, so the screen can say "already done" and offer
// Skip. It changes nothing and sends nothing off this Mac.
func (b *Bridge) Detect() View {
	ctx := b.ctx
	p := b.paths
	b.flow.SetFound(StepMeru, MeruFound(p, b.o.Payload, b.o.Apps))

	cfg, cfgErr := config.Load(p.Config())
	if _, err := os.Stat(p.Config()); err != nil {
		cfgErr = err // config.Load gives defaults for a missing file
	}
	ollamaURL := "http://127.0.0.1:11434"
	if cfgErr == nil {
		ollamaURL = cfg.Ollama.BaseURL
	}
	if v, err := OllamaVersion(ctx, b.o.Local, ollamaURL); err == nil {
		b.flow.SetFound(StepOllama, "Ollama "+v+" runs.")
	}
	if cfgErr == nil && len(cfg.Index.Folders) > 0 {
		b.flow.SetFound(StepFolders, "config.toml lists "+strings.Join(cfg.Index.Folders, ", ")+".")
	}
	// CheckSearXNG sends an empty search, which SearXNG answers without
	// asking any search engine.
	if err := catalog.CheckSearXNG(ctx, SearXNGURL); err == nil {
		b.flow.SetFound(StepWeb, "SearXNG answers JSON at "+SearXNGURL+".")
	}
	if cfgErr == nil && len(cfg.Commands) > 0 {
		b.flow.SetFound(StepSkills, fmt.Sprintf("config.toml has %d commands.", len(cfg.Commands)))
	}
	b.flow.SetFound(StepGoogle, GoogleFound(p, cfgErr == nil && hasServer(cfg, "google")))
	if prof, err := LoadProfile(p); err == nil && prof.Name != "" {
		b.flow.SetFound(StepProfile, "Meru knows your name: "+prof.Name+". Check the form and press Continue.")
	}
	if Ping(ctx, p.Socket()) == nil {
		b.flow.SetFound(StepStart, "merud runs. Continue restarts it, so it reads the settings from this run.")
	}
	return b.State()
}

// hasServer reports whether cfg has an MCP server called name.
func hasServer(cfg config.Config, name string) bool {
	return slices.ContainsFunc(cfg.MCP.Servers, func(s config.MCPServer) bool { return s.Name == name })
}

// Screen is the extra data one step's screen shows. Only the fields for
// that step are set.
type Screen struct {
	Machine  *Machine        `json:"machine,omitempty"`
	Models   []ModelState    `json:"models,omitempty"`
	OllamaAt string          `json:"ollamaAt,omitempty"`
	Brew     bool            `json:"brew"`
	Download string          `json:"download,omitempty"`
	Folders  []Folder        `json:"folders,omitempty"`
	SkipNote string          `json:"skipNote,omitempty"`
	Docker   bool            `json:"docker"`
	Commands []CommandOption `json:"commands,omitempty"`
	Skills   []SkillOption   `json:"skills,omitempty"`
	Profile  *Profile        `json:"profile,omitempty"`
	// EmailRequired is true after the Google step, whose tools need the
	// address.
	EmailRequired bool `json:"emailRequired"`
}

// ModelState is one model the Ollama step will pull, and whether Ollama
// has it already.
type ModelState struct {
	Name string `json:"name"`
	Have bool   `json:"have"`
}

// Screen returns the data the screen for step id shows. It fails for a
// step that can't read what it needs, such as a config that doesn't load.
func (b *Bridge) Screen(id string) (Screen, error) {
	p := b.paths
	switch id {
	case StepCheck:
		m, err := CheckMachine(b.ctx, b.o.Run, p.Home)
		if err != nil {
			return Screen{}, err
		}
		return Screen{Machine: &m}, nil
	case StepOllama:
		s := Screen{Download: OllamaDownloadURL}
		_, err := Locate("brew", p.Home)
		s.Brew = err == nil
		have, _ := OllamaModels(b.ctx, b.o.Local, b.ollamaURL())
		for _, m := range b.chosen().Models() {
			s.Models = append(s.Models, ModelState{Name: m, Have: HasModel(have, m)})
		}
		if app := OllamaApp(p.Home); app != "" {
			s.OllamaAt = p.Tilde(app)
		}
		return s, nil
	case StepFolders:
		cfg, err := config.Load(p.Config())
		if err != nil {
			return Screen{}, err
		}
		return Screen{Folders: SuggestFolders(b.ctx, p, cfg.Index.Folders), SkipNote: SkipNote}, nil
	case StepWeb:
		_, err := Locate("docker", p.Home)
		return Screen{Docker: err == nil}, nil
	case StepSkills:
		cfg, err := config.Load(p.Config())
		if err != nil {
			return Screen{}, err
		}
		return Screen{Commands: SampleCommands(p, cfg), Skills: BuiltinSkills(cfg)}, nil
	case StepProfile:
		prof, err := LoadProfile(p)
		if err != nil {
			return Screen{}, err
		}
		return Screen{Profile: &prof, EmailRequired: b.flow.Is(StepGoogle, StatusDone)}, nil
	}
	return Screen{}, nil
}

// ollamaURL returns [ollama] base_url from config.toml, or Ollama's
// default when config doesn't load yet.
func (b *Bridge) ollamaURL() string {
	if cfg, err := config.Load(b.paths.Config()); err == nil {
		return cfg.Ollama.BaseURL
	}
	return "http://127.0.0.1:11434"
}

// chosen returns the model set the user picked in the first step, or the
// smallest one when the step was skipped.
func (b *Bridge) chosen() config.Recommendation {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.machine != nil && b.choice >= 0 && b.choice < len(b.machine.Choices) {
		return b.machine.Choices[b.choice].rec
	}
	return config.Recommendations()[0]
}

// Input is everything a step's screen can send with Continue. Each step
// reads only its own part.
type Input struct {
	Choice   int         `json:"choice"`
	Meru     MeruInput   `json:"meru"`
	Folders  []string    `json:"folders"`
	Commands []string    `json:"commands"`
	Google   GoogleInput `json:"google"`
	Profile  Profile     `json:"profile"`
}

// Run runs step id with in, for Continue and Retry, and returns the new
// state. A step that fails ends as failed with the reason in its Detail,
// so the screen can show it with Retry and Skip; Run itself fails only
// when the step can't start, such as while another runs.
func (b *Bridge) Run(id string, in Input) (View, error) {
	if err := b.flow.Start(id); err != nil {
		return b.State(), err
	}
	say := func(line string) { b.progress(id, line, -1) }
	detail, err := b.run(b.ctx, id, in, say)
	if ferr := b.flow.Finish(id, detail, err); ferr != nil {
		return b.State(), ferr
	}
	return b.State(), nil
}

// Skip marks step id skipped and returns the new state. It fails for About
// you, which can't be skipped, and while the step runs.
func (b *Bridge) Skip(id string) (View, error) {
	err := b.flow.Skip(id)
	return b.State(), err
}

// progress sends one line of news to the page.
func (b *Bridge) progress(step, line string, fraction float64) {
	if b.o.Emit != nil {
		b.o.Emit(ProgressEvent, Progress{Step: step, Line: line, Fraction: fraction})
	}
}

// run does the work of step id and returns what it did.
func (b *Bridge) run(ctx context.Context, id string, in Input, say func(string)) (string, error) {
	p := b.paths
	switch id {
	case StepCheck:
		m, err := CheckMachine(ctx, b.o.Run, p.Home)
		if err != nil {
			return "", err
		}
		b.mu.Lock()
		b.machine = &m
		b.choice = m.Recommended
		if in.Choice >= 0 && in.Choice < len(m.Choices) {
			b.choice = in.Choice
		}
		picked := m.Choices[b.choice]
		b.mu.Unlock()
		return fmt.Sprintf("%s, macOS %s, %d GB of memory, %d GB free. Models: %s.", m.Chip, m.MacOS, m.MemoryGB, m.FreeDiskGB, picked.Label), nil
	case StepMeru:
		detail, err := InstallMeru(ctx, b.o.Run, p, b.o.Payload, b.o.Apps, in.Meru, say)
		if err != nil {
			return "", err
		}
		if wrote, err := EnsureConfig(p.Config()); err != nil {
			return "", err
		} else if wrote {
			detail += "\nWrote " + p.Tilde(p.Config()) + " from the template: every setting, with its default and a comment."
		}
		return detail, nil
	case StepOllama:
		return b.runOllama(ctx, say)
	case StepFolders:
		return SaveFolders(p, in.Folders)
	case StepWeb:
		return SetUpWebSearch(ctx, b.o.Run, b.o.Local, p, say)
	case StepSkills:
		return SaveSkillsAndCommands(p, in.Commands)
	case StepGoogle:
		return SetUpGoogle(ctx, b.o.Run, b.o.Local, p, in.Google, say)
	case StepProfile:
		return SaveProfile(p, in.Profile, b.flow.Is(StepGoogle, StatusDone))
	case StepStart:
		if !b.flow.Is(StepProfile, StatusDone) {
			return "", errors.New("tell Meru your name first, in About you")
		}
		detail, err := StartMerud(ctx, b.o.Run, p, say)
		if err != nil {
			return "", err
		}
		scan, err := FollowScan(ctx, p.Socket(), 2*time.Second, say)
		if err != nil {
			return "", err
		}
		return detail + "\n" + scan, nil
	}
	return "", fmt.Errorf("no step %q", id)
}

// runOllama installs Ollama when it's missing, starts it, pulls the
// chosen models with live progress, and names the answer model in
// config.toml when the set has its own.
func (b *Bridge) runOllama(ctx context.Context, say func(string)) (string, error) {
	p := b.paths
	if _, err := EnsureConfig(p.Config()); err != nil {
		return "", err
	}
	base := b.ollamaURL()
	var done []string
	if _, err := OllamaVersion(ctx, b.o.Local, base); err != nil && OllamaApp(p.Home) == "" && !ollamaFromBrew() {
		msg, err := InstallOllama(ctx, b.o.Run, b.o.Web, p.Home, b.o.Apps, say)
		if err != nil {
			return "", err
		}
		done = append(done, msg)
	}
	v, err := StartOllama(ctx, b.o.Run, b.o.Local, p.Home, base, say)
	if err != nil {
		return "", err
	}
	done = append(done, "Ollama "+v+" runs at "+base+".")

	rec := b.chosen()
	have, err := OllamaModels(ctx, b.o.Local, base)
	if err != nil {
		return "", err
	}
	for _, m := range rec.Models() {
		if HasModel(have, m) {
			done = append(done, "Ollama already has "+m+".")
			continue
		}
		say("Downloading " + m)
		last := ""
		err := PullModel(ctx, b.o.Local, base, m, func(pr Pull) {
			// Ollama sends many lines a second; the page gets a new one
			// only when the text changes.
			if t := pr.Text(); t != last {
				last = t
				frac := -1.0
				if pr.Total > 0 {
					frac = float64(pr.Completed) / float64(pr.Total)
				}
				b.progress(StepOllama, t, frac)
			}
		})
		if err != nil {
			return "", err
		}
		done = append(done, "Downloaded "+m+".")
	}
	if rec.Main != "" {
		if err := catalog.SetTableString(p.Config(), "models", "main", rec.Main, nil); err != nil {
			return "", err
		}
		done = append(done, "Set [models] main = \""+rec.Main+"\" in "+p.Tilde(p.Config())+".")
	}
	return strings.Join(done, "\n"), nil
}

// PickFolder shows the system's folder dialog and returns the folder
// picked, with its file count. It returns an empty Folder when the user
// cancels.
func (b *Bridge) PickFolder() (Folder, error) {
	if b.o.PickFolder == nil {
		return Folder{}, errors.New("this window can't show a folder dialog")
	}
	path, err := b.o.PickFolder()
	if err != nil || path == "" {
		return Folder{}, err
	}
	return PickedFolder(b.ctx, b.paths, path)
}

// Links returns the pages the screens may open, by name. Only these open:
// the page names one, and never passes a URL of its own.
func Links() map[string]string {
	return map[string]string{
		"ollama":          "https://ollama.com/download",
		"docker":          DockerDownloadURL,
		"uv":              "https://docs.astral.sh/uv/getting-started/installation/",
		"google-guide":    "https://github.com/aarora79/meru/blob/main/docs/google-setup.md",
		"google-project":  "https://console.cloud.google.com/projectcreate",
		"gmail-api":       "https://console.cloud.google.com/apis/library/gmail.googleapis.com",
		"calendar-api":    "https://console.cloud.google.com/apis/library/calendar-json.googleapis.com",
		"drive-api":       "https://console.cloud.google.com/apis/library/drive.googleapis.com",
		"docs-api":        "https://console.cloud.google.com/apis/library/docs.googleapis.com",
		"google-auth":     "https://console.cloud.google.com/auth/overview",
		"google-audience": "https://console.cloud.google.com/auth/audience",
		"google-clients":  "https://console.cloud.google.com/auth/clients",
	}
}

// OpenLink opens the page Links names name in the default browser. It
// fails for a name Links doesn't hold.
func (b *Bridge) OpenLink(name string) error {
	url, ok := Links()[name]
	if !ok {
		return fmt.Errorf("no link %q", name)
	}
	_, err := b.o.Run(b.ctx, "open", []string{url}, nil)
	return err
}

// OpenConfig opens config.toml in the default text editor, creating it
// from the template first when it isn't there.
func (b *Bridge) OpenConfig() error {
	if _, err := EnsureConfig(b.paths.Config()); err != nil {
		return err
	}
	// open -t opens a file in the default text editor, whatever its name.
	_, err := b.o.Run(b.ctx, "open", []string{"-t", b.paths.Config()}, nil)
	return err
}

// OpenMeru opens Meru.app.
func (b *Bridge) OpenMeru() error {
	_, err := b.o.Run(b.ctx, "open", []string{filepath.Join(b.o.Apps, "Meru.app")}, nil)
	return err
}

// Quit closes the installer.
func (b *Bridge) Quit() {
	if b.o.Quit != nil {
		b.o.Quit()
	}
}
