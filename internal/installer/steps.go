// This file holds the list of steps and the rules for moving between their
// states: which steps there are, what each costs, and when Continue, Skip
// and Retry are allowed. The Bridge asks Flow before it runs anything, so
// the page can't run a step twice at once or skip one that must be done.

package installer

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Status is where one step stands. It is a string type, so the page gets
// readable words in its JSON.
type Status string

// The states a step moves through:
//
//	pending -> running -> done
//	                   -> failed -> running (Retry)
//	pending or failed  -> skipped (Skip, when the step allows it)
//	skipped or done    -> running (the user comes back to it)
const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// The step IDs, in the order the installer shows them.
const (
	StepCheck   = "check"
	StepMeru    = "meru"
	StepOllama  = "ollama"
	StepFolders = "folders"
	StepWeb     = "web"
	StepSkills  = "skills"
	StepGoogle  = "google"
	StepProfile = "profile"
	StepStart   = "start"
)

// Step is one screen of the installer and where it stands. The JSON names
// are what the page reads.
type Step struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// What says what the step does, Why why Meru needs it, and Cost how
	// much it downloads and how long it takes.
	What string `json:"what"`
	Why  string `json:"why"`
	Cost string `json:"cost"`
	// Skippable is false for a step the user must finish: About you.
	Skippable bool   `json:"skippable"`
	Status    Status `json:"status"`
	// Detail is the last thing the step said: what it did, or why it
	// failed.
	Detail string `json:"detail,omitempty"`
	// Found says what the installer found already done, such as "Ollama
	// 0.34 is running", so a second run can offer to skip.
	Found string `json:"found,omitempty"`
}

// ErrBusy means a step is already running. One step runs at a time.
var ErrBusy = errors.New("another step is running; wait for it to finish")

// Flow holds the steps and their states. Its methods are safe to call from
// several goroutines: Wails runs each call from the page on its own.
type Flow struct {
	// mu guards steps. A sync.Mutex is a lock: Lock waits until no other
	// goroutine holds it.
	mu    sync.Mutex
	steps []Step
}

// NewFlow returns a Flow with every step pending.
func NewFlow() *Flow {
	return &Flow{steps: stepList()}
}

// stepList returns the nine steps with their text, every one pending.
// Every string here shows on screen. Each step but About you can be
// skipped: Meru needs to know whose files it reads.
func stepList() []Step {
	steps := []Step{
		{
			ID: StepCheck, Title: "Check this Mac",
			What: "Reads the chip, the macOS version, the memory and the free disk space, and picks models to fit.",
			Why:  "The models run on this Mac, so the memory decides which ones answer well without slowing everything else down.",
			Cost: "No download. A second or two.",
		},
		{
			ID: StepMeru, Title: "Install Meru",
			What: "Copies meru and merud to ~/.local/bin and Meru.app to your Applications folder.",
			Why:  "merud runs in the background and does the work. meru is the command-line tool, and Meru.app is the window you chat in.",
			Cost: "About 90 MB on disk, copied from this installer. A few seconds.",
		},
		{
			ID: StepOllama, Title: "Ollama and the models",
			What: "Installs Ollama if it's missing, starts it, and downloads the models this Mac suits.",
			Why:  "Ollama runs the models on this Mac. No question and no file leaves it to reach a model.",
			Cost: "Ollama takes a few hundred MB. The models take 2 GB or more; the screen shows each download as it runs. Minutes, most of it the models.",
		},
		{
			ID: StepFolders, Title: "Folders to search",
			What: "Lets you pick the folders Meru reads into its search index.",
			Why:  "Meru answers from your own files only when it has read them. It reads nothing you don't pick.",
			Cost: "No download. The first scan starts in the last step and takes a minute or more for a big folder.",
		},
		{
			ID: StepWeb, Title: "Web search",
			What: "Downloads SearXNG, a search engine that runs on your own computer, and starts it in Docker.",
			Why:  "With web search Meru can look up what your files don't hold. SearXNG asks several search engines for you, with no account and no cookies.",
			Cost: "One container image, about 200 MB. A minute or two. Needs Docker Desktop, OrbStack or colima.",
		},
		{
			ID: StepSkills, Title: "Skills and commands",
			What: "Checks that the built-in skills are on and offers sample commands that only read, such as the free disk space.",
			Why:  "Skills teach Meru how to write, research and explain. Commands let it run a program you name, with no shell.",
			Cost: "No download. A few seconds.",
		},
		{
			ID: StepGoogle, Title: "Gmail, Calendar and Drive",
			What: "Walks you through making your own Google sign-in, then sets up the small server Meru reaches Google through.",
			Why:  "With it Meru can search your mail, list your events and read your Drive files. Sending mail and changing an event ask you first.",
			Cost: "uv, about 40 MB, and the server on its first start. About 20 minutes, most of it in Google's console.",
		},
		{
			ID: StepProfile, Title: "About you",
			What: "Asks your name, and your email and how you like answers if you want to give them.",
			Why:  "Meru reads your files and mail, which name many people. Your name tells it who \"I\" and \"my\" mean, so it never mixes you up with someone in your files.",
			Cost: "No download. A minute.",
		},
		{
			ID: StepStart, Title: "Start Meru",
			What: "Starts merud now and at every login, waits for it to answer, and shows its first scan of your folders.",
			Why:  "merud has to run for Meru to answer. launchd, the Mac's own service manager, starts it at login.",
			Cost: "No download. Seconds, then the scan runs in the background.",
		},
	}
	for i := range steps {
		steps[i].Status = StatusPending
		steps[i].Skippable = steps[i].ID != StepProfile
	}
	return steps
}

// Steps returns a copy of every step, in order.
func (f *Flow) Steps() []Step {
	f.mu.Lock()
	// defer runs f.mu.Unlock() when Steps returns.
	defer f.mu.Unlock()
	return slices.Clone(f.steps)
}

// Get returns a copy of the step with this ID, and false when there is
// none.
func (f *Flow) Get(id string) (Step, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(id)
	if i < 0 {
		return Step{}, false
	}
	return f.steps[i], true
}

// index returns where step id sits in f.steps, or -1. The caller holds mu.
func (f *Flow) index(id string) int {
	return slices.IndexFunc(f.steps, func(s Step) bool { return s.ID == id })
}

// Start moves step id to running, for Continue and Retry. It fails for an
// unknown step, and with ErrBusy while any step runs. A done or skipped
// step may start again: the user can come back to one.
func (f *Flow) Start(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(id)
	if i < 0 {
		return fmt.Errorf("no step %q", id)
	}
	if slices.ContainsFunc(f.steps, func(s Step) bool { return s.Status == StatusRunning }) {
		return ErrBusy
	}
	f.steps[i].Status = StatusRunning
	f.steps[i].Detail = ""
	return nil
}

// Finish ends the running step id: done with detail when err is nil,
// failed with err's text otherwise. It fails when the step isn't running.
func (f *Flow) Finish(id, detail string, err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(id)
	if i < 0 {
		return fmt.Errorf("no step %q", id)
	}
	if f.steps[i].Status != StatusRunning {
		return fmt.Errorf("step %q isn't running", id)
	}
	if err != nil {
		f.steps[i].Status = StatusFailed
		f.steps[i].Detail = err.Error()
		return nil
	}
	f.steps[i].Status = StatusDone
	f.steps[i].Detail = detail
	return nil
}

// Skip marks step id skipped. It fails for a step that can't be skipped,
// such as About you, and for a running one.
func (f *Flow) Skip(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(id)
	if i < 0 {
		return fmt.Errorf("no step %q", id)
	}
	switch {
	case !f.steps[i].Skippable:
		return fmt.Errorf("%s can't be skipped", f.steps[i].Title)
	case f.steps[i].Status == StatusRunning:
		return ErrBusy
	}
	f.steps[i].Status = StatusSkipped
	f.steps[i].Detail = ""
	return nil
}

// SetFound records what the installer found already in place for step id.
func (f *Flow) SetFound(id, found string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.index(id); i >= 0 {
		f.steps[i].Found = found
	}
}

// Is reports whether step id has status s.
func (f *Flow) Is(id string, s Status) bool {
	st, ok := f.Get(id)
	return ok && st.Status == s
}
