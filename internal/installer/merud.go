// This file holds the last step, "Start Meru": it writes merud's launchd
// job from the repo's own plist, loads it so merud starts now and at every
// login, waits for merud to answer on its socket, and follows the scan of
// the chosen folders that merud starts by itself. The hand-off of the
// connectors, which this step runs between the two, lives in
// connectors.go.

package installer

import (
	"context"
	_ "embed" // the blank import turns on the //go:embed directive below
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// merudPlistTemplate is deploy/launchd/com.meru.merud.plist, compiled into
// the installer. A //go:embed file must sit inside the package's folder,
// so launchd/ holds a byte-for-byte copy; TestMerudPlistIsDeployCopy fails
// when the two differ. See docs/coding-notes/go-basics/embed.md.
//
//go:embed launchd/com.meru.merud.plist
var merudPlistTemplate string

// merudLabel is the launchd job's name, the plist's Label.
const merudLabel = "com.meru.merud"

// The waits in this step.
const (
	// merudStartWait covers merud's start: it opens the store, checks
	// Ollama and loads the models before it listens.
	merudStartWait = 60 * time.Second
	// indexWait is how long the screen follows the first scan. A big
	// folder takes longer; merud carries on without the screen.
	indexWait = 2 * time.Minute
)

// xmlText escapes s for a plist's <string> element.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s)) // a strings.Builder never fails a write
	return b.String()
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

// MerudPlist returns the launchd job for merud: the repo's plist with
// __HOME__ replaced by the home folder and merud's path pointed at
// ~/.local/bin, where the Install Meru step puts it.
func MerudPlist(p Paths) string {
	merud := filepath.Join(p.Bin(), "merud")
	text := strings.ReplaceAll(merudPlistTemplate, "__HOME__/go/bin/merud", xmlText(merud))
	return strings.ReplaceAll(text, "__HOME__", xmlText(p.Home))
}

// Ping asks the merud on socket whether it is up. It fails when nothing
// answers or merud answers with an error.
func Ping(ctx context.Context, socket string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// rpc.Do returns an iterator: the loop runs once per event merud sends.
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpPing}, nil) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case rpc.EventDone:
			return nil
		case rpc.EventError:
			return errors.New(ev.Error)
		}
	}
	return errors.New("merud closed the connection without an answer")
}

// StartMerud writes and loads merud's launchd job, which starts merud now
// and at every login, and waits for merud to answer. When merud already
// runs, loading the job again restarts it, so it reads the config this
// run wrote. It returns what it did, and fails when merud isn't installed,
// launchd refuses the job, or merud doesn't answer within a minute; the
// error then quotes the end of merud's error log.
func StartMerud(ctx context.Context, run Runner, p Paths, say func(string)) (string, error) {
	merud := filepath.Join(p.Bin(), "merud")
	if _, err := os.Stat(merud); err != nil {
		return "", fmt.Errorf("merud isn't in %s; go back to Install Meru first", p.Tilde(p.Bin()))
	}
	if _, err := EnsureConfig(p.Config()); err != nil {
		return "", err
	}
	say("Starting merud now and at every login with launchd")
	plist, err := loadJob(ctx, run, p, merudLabel, MerudPlist(p))
	if err != nil {
		return "", err
	}
	say("Waiting for merud to answer")
	if _, err := waitFor(ctx, merudStartWait, func() (string, error) { return "", Ping(ctx, p.Socket()) }); err != nil {
		log := filepath.Join(p.MeruDir(), "merud.err.log")
		return "", fmt.Errorf("merud didn't answer within a minute. The end of %s says:\n%s", p.Tilde(log), lastLines(log, 8))
	}
	return "merud runs, and launchd starts it at every login: " + p.Tilde(plist) + ".", nil
}

// lastLines returns up to n lines from the end of the file at path, or a
// note that it can't be read.
func lastLines(path string, n int) string {
	data, err := os.ReadFile(path) // #nosec G304 -- merud's own log
	if err != nil {
		return "(nothing: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// IndexStatus asks the merud on socket what its search index holds and
// whether a scan runs. It fails when merud can't be reached or refuses.
func IndexStatus(ctx context.Context, socket string) (rpc.IndexStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpIndexStatus}, nil) {
		if err != nil {
			return rpc.IndexStatus{}, err
		}
		switch ev.Type {
		case rpc.EventStatus:
			if ev.Status != nil {
				return *ev.Status, nil
			}
		case rpc.EventError:
			return rpc.IndexStatus{}, errors.New(ev.Error)
		}
	}
	return rpc.IndexStatus{}, errors.New("merud sent no index status")
}

// FollowScan shows the first scan of the [index] folders. merud starts
// that scan by itself each time it starts, so the step only watches: it
// asks for the index status every two seconds and passes a line to say
// each time the file count changes. It returns a line on what the scan
// did, or, when the scan outlasts indexWait, a line saying merud carries
// on. Leaving early stops nothing, because the scan belongs to merud. It
// fails when merud can't be reached.
func FollowScan(ctx context.Context, socket string, every time.Duration, say func(string)) (string, error) {
	deadline := time.Now().Add(indexWait)
	last := -1
	for {
		st, err := IndexStatus(ctx, socket)
		if err != nil {
			return "", fmt.Errorf("ask merud about the scan: %w", err)
		}
		if len(st.Folders) == 0 {
			return "No folders to scan. Add some later in Meru.app's Settings, Folders.", nil
		}
		if st.Documents != last {
			last = st.Documents
			say(fmt.Sprintf("Scanning your folders: %d files in the index so far", st.Documents))
		}
		if !st.Scanning && st.LastScan != nil {
			r := st.LastScan
			return fmt.Sprintf("merud scanned your folders: %d files in the index, %d skipped, %d it couldn't read.",
				st.Documents, r.Skipped, r.Failed), nil
		}
		if time.Now().After(deadline) {
			return "The first scan goes on in the background. Meru answers from each file once merud has read it.", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
	}
}
