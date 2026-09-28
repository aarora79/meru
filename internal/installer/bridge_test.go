// This file tests the Bridge the page calls: running and skipping steps
// through it, the rule that About you comes before Start Meru, the links,
// and the page's assets.

package installer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/desktop"
)

// newBridge returns a Bridge over a temporary home, with fake programs
// and a network where nothing answers. It returns the fake Runner too,
// and the events the Bridge sent.
func newBridge(t *testing.T, r *fakeRunner) (*Bridge, *[]Progress) {
	t.Helper()
	p := tempHome(t)
	var events []Progress
	b := New(Options{
		Home: p.Home, Payload: t.TempDir(), Apps: t.TempDir(), Run: r.run,
		Local: deadClient(), Web: deadClient(),
		Emit: func(name string, data any) {
			if name == ProgressEvent {
				events = append(events, data.(Progress))
			}
		},
	})
	t.Cleanup(func() { _ = b.ServiceShutdown() })
	return b, &events
}

// status returns step id's status in v.
func status(v View, id string) Status {
	for _, s := range v.Steps {
		if s.ID == id {
			return s.Status
		}
	}
	return ""
}

// TestBridgeRunAndSkip runs the Mac check with a picked model set, skips a
// step, fails one, and checks that About you can't be skipped and must
// come before Start Meru.
func TestBridgeRunAndSkip(t *testing.T) {
	b, _ := newBridge(t, fakeMac(64))

	v, err := b.Run(StepCheck, Input{Choice: 0})
	if err != nil || status(v, StepCheck) != StatusDone {
		t.Fatalf("check: %v, %v", status(v, StepCheck), err)
	}
	if got := b.chosen(); got.Main != "" {
		t.Errorf("picked the first set, got main %q", got.Main)
	}
	if _, _ = b.Run(StepCheck, Input{Choice: 1}); b.chosen().Main == "" {
		t.Error("picking the second set on a 64 GB Mac left main empty")
	}

	if v, err = b.Skip(StepGoogle); err != nil || status(v, StepGoogle) != StatusSkipped {
		t.Errorf("skip google: %v, %v", status(v, StepGoogle), err)
	}
	if v, err = b.Skip(StepProfile); err == nil || status(v, StepProfile) != StatusPending {
		t.Errorf("skip About you: %v, %v; want a refusal", status(v, StepProfile), err)
	}

	v, _ = b.Run(StepStart, Input{})
	if status(v, StepStart) != StatusFailed {
		t.Errorf("Start Meru before About you = %v, want failed", status(v, StepStart))
	}
	v, _ = b.Run(StepProfile, Input{Profile: Profile{}})
	if status(v, StepProfile) != StatusFailed {
		t.Errorf("About you with no name = %v, want failed", status(v, StepProfile))
	}
	v, _ = b.Run(StepProfile, Input{Profile: Profile{Name: "Dana Reyes"}})
	if status(v, StepProfile) != StatusDone {
		t.Errorf("About you with a name = %v, want done", status(v, StepProfile))
	}
	scr, err := b.Screen(StepProfile)
	if err != nil || scr.Profile == nil || scr.Profile.Name != "Dana Reyes" || scr.EmailRequired {
		t.Errorf("About you screen on a second visit = %+v, %v", scr, err)
	}
}

// TestBridgeEmailAfterGoogle checks that About you needs the email once
// the Google step is done.
func TestBridgeEmailAfterGoogle(t *testing.T) {
	b, _ := newBridge(t, &fakeRunner{})
	_ = b.flow.Start(StepGoogle)
	_ = b.flow.Finish(StepGoogle, "ok", nil)
	v, _ := b.Run(StepProfile, Input{Profile: Profile{Name: "Dana"}})
	if status(v, StepProfile) != StatusFailed {
		t.Error("About you passed with no email after Google")
	}
	if scr, _ := b.Screen(StepProfile); !scr.EmailRequired {
		t.Error("the screen doesn't mark the email as needed")
	}
}

// TestBridgeProgress checks that a step's news reaches the page as events.
func TestBridgeProgress(t *testing.T) {
	noDocker := &fakeRunner{answer: func(string, []string, func(string)) (string, error) { return "", ErrMissing }}
	b, events := newBridge(t, noDocker)
	v, _ := b.Run(StepWeb, Input{})
	if len(*events) == 0 || (*events)[0].Step != StepWeb {
		t.Errorf("events = %+v", *events)
	}
	for _, s := range v.Steps {
		if s.ID == StepWeb && (s.Status != StatusFailed || s.Detail != ErrNoDocker.Error()) {
			t.Errorf("web step = %s %q, want failed with ErrNoDocker", s.Status, s.Detail)
		}
	}
}

// TestLinks checks that every link is https and that OpenLink runs open
// with that one URL and nothing else, and refuses a name it doesn't know.
func TestLinks(t *testing.T) {
	r := &fakeRunner{}
	b, _ := newBridge(t, r)
	for name, url := range Links() {
		if !strings.HasPrefix(url, "https://") {
			t.Errorf("link %s = %q, want https", name, url)
		}
	}
	if err := b.OpenLink("docker"); err != nil {
		t.Fatal(err)
	}
	if err := b.OpenLink("file:///etc/passwd"); err == nil {
		t.Error("OpenLink opened a name it doesn't know")
	}
	if got := r.called(); !slices.Equal(got, []string{"open " + DockerDownloadURL}) {
		t.Errorf("calls = %v", got)
	}
}

// TestAssets checks that the page is served with the policy, that a file
// the installer lacks comes from the fallback, and that the policy matches
// Meru.app's.
func TestAssets(t *testing.T) {
	if ContentSecurityPolicy != desktop.ContentSecurityPolicy {
		t.Error("the installer's Content-Security-Policy differs from Meru.app's")
	}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "from Meru.app") })
	srv := httptest.NewServer(Assets(fallback))
	defer srv.Close()
	for path, want := range map[string]string{
		"/":              "<title>Install Meru</title>",
		"/installer.js":  "installer.Bridge.",
		"/installer.css": ".steps-rail",
		"/app.css":       "from Meru.app",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), want) {
			t.Errorf("%s doesn't hold %q", path, want)
		}
		if resp.Header.Get("Content-Security-Policy") != ContentSecurityPolicy {
			t.Errorf("%s has no policy", path)
		}
	}
}
