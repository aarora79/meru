// This file holds Reporter: the loop that wakes every Tick, the rule that
// decides whether today's report goes now, and the one HTTP client that
// sends it.

package usagereport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aarora79/meru/internal/obs"
)

// DefaultURL is Meru's own collector, a Cloudflare Worker whose code lives
// in collector/. [usage_report] url replaces it, for the end-to-end tests
// and for anyone who runs a collector of their own.
const DefaultURL = "https://meru-report.meru-workers.workers.dev"

// FileName is the record merud keeps in the Meru home.
const FileName = "usage-report.jsonl"

// Tick is how often the loop checks whether to send. It compares dates on
// each tick, so a computer that slept past midnight sends within one Tick of
// waking, whatever Go's timers did during the sleep.
const Tick = 30 * time.Minute

// sendWindow is the spread of each day's send time: a random minute from 0
// up to this many after midnight UTC, picked once a day, so the time a
// report arrives says nothing about when someone starts work.
const sendWindow = 120

// timeout caps one send. The body is about 100 bytes; a collector that
// takes longer is down.
const timeout = 10 * time.Second

// The reasons merud holds a report back, in the words both clients show
// after the choice itself ("On", "Off" or "Not chosen"), so none repeats it.
// Named constants let the send rule, Status and the tests share one spelling
// of each.
const (
	reasonDoNotTrack = "DO_NOT_TRACK is set, so nothing goes."
	reasonOff        = "You turned it off."
	reasonNotChosen  = "Nothing goes until you say yes."
	reasonDevBuild   = "This build isn't a release, so nothing goes."
	reasonNotSeen    = "Waiting for meru chat or the app to show the notice."
	reasonFirstDay   = "The first one goes tomorrow."
	reasonSentToday  = "Today's has gone."
	reasonNotYet     = "Today's goes later today."
)

// Settings is the [usage_report] table, plus the profile, as merud read it
// for this tick. Taking it as a value keeps this package free of the config
// file.
type Settings struct {
	Send    string // "on", "off", or "" when the user hasn't chosen
	URL     string // the collector; "" means DefaultURL
	Profile string // "lite" or "full"
}

// Questions is the one thing the reporter asks of the store. Declaring the
// interface here, in the package that uses it, keeps usagereport from
// importing store; *store.Store has this method.
type Questions interface {
	QuestionsBetween(ctx context.Context, from, to time.Time) (int, error)
}

// Reporter decides when to send and keeps usage-report.jsonl. merud makes
// one, runs Loop in its errgroup, and calls Status and Mark from the op
// handler.
type Reporter struct {
	path    string // usage-report.jsonl in the Meru home
	version string // about.Version(), such as "v0.7.5"
	store   Questions
	log     *slog.Logger
	client  *http.Client
	env     func(string) string // os.Getenv; tests swap it
	rand    func(n int) int     // rand.IntN; tests swap it
	now     func() time.Time    // time.Now; tests swap it

	// fileMu guards the file. A tick holds it from reading the file to
	// writing the "sent" line, so a Mark can't slip in between and two
	// lines never interleave.
	fileMu sync.Mutex

	// dayMu guards day and minute, today's send minute, and sentDay, the
	// UTC day of the last report the collector took in this run.
	dayMu   sync.Mutex
	day     time.Time
	minute  int
	sentDay time.Time
}

// New returns a Reporter that keeps its record in dir. version is the
// program's version as about.Version gives it; only a release version sends.
func New(dir, version string, store Questions, log *slog.Logger) *Reporter {
	return &Reporter{
		path:    filepath.Join(dir, FileName),
		version: version,
		store:   store,
		log:     log,
		client:  newClient(),
		env:     os.Getenv,
		rand:    rand.IntN,
		now:     time.Now,
	}
}

// newClient returns the client every send uses. Proxy nil means an
// HTTPS_PROXY in merud's environment can't route the report somewhere else,
// and CheckRedirect refuses every redirect, so the collector can't bounce
// the report to another host.
func newClient() *http.Client {
	// DefaultTransport is an interface value; .(*http.Transport) asserts it
	// holds a *http.Transport, so Clone can copy its timeouts.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &http.Client{
		Timeout:   timeout,
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Loop checks once at start and then every Tick until ctx ends. settings
// returns [usage_report] as it stands, so `meru report off` takes effect at
// the next tick without a restart. Loop logs its own errors and never stops
// merud.
func (r *Reporter) Loop(ctx context.Context, settings func() (Settings, error)) {
	r.tick(ctx, settings, r.now())
	ticker := time.NewTicker(Tick)
	defer ticker.Stop()
	for {
		// select waits for whichever case is ready first: merud shutting
		// down, or the next tick.
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.tick(ctx, settings, now)
		}
	}
}

// tick sends today's report if nothing holds it back, and records the send
// in the file either way.
func (r *Reporter) tick(ctx context.Context, settings func() (Settings, error), now time.Time) {
	s, err := settings()
	if err != nil {
		r.log.Debug("usage report: read settings", "err", err)
		return
	}
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	st, err := readState(r.path, now)
	if err != nil {
		r.log.Debug("usage report: read record", "err", err)
		return
	}
	if why := r.blocked(s, st, now); why != "" {
		r.log.Debug("usage report skipped", "why", why)
		return
	}
	_ = r.send(ctx, s, st, now) // send logs and records its own outcome
}

// SendNow sends today's report at once, for the "Send today's now" button
// in the clients. It skips the two waits a tick keeps, the one-day wait after
// the notice and today's random send minute, and holds every other rule: the
// choice is "on", DO_NOT_TRACK is unset, the build is a release, a client has
// shown the notice, and no report went today. So a click never sends a
// second report in one day. It fails with the reason when a rule holds the
// report back, and when the collector doesn't take it.
func (r *Reporter) SendNow(ctx context.Context, s Settings) error {
	now := r.now()
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	st, err := readState(r.path, now)
	if err != nil {
		return err
	}
	if why := r.blockedNow(s, st, now); why != "" {
		return fmt.Errorf("not sent: %s", why)
	}
	return r.send(ctx, s, st, now)
}

// send builds the report, posts it, and records a "sent" or "failed" line
// either way. The caller holds fileMu and has checked the rules. It returns
// the post's error, so SendNow can tell the user.
func (r *Reporter) send(ctx context.Context, s Settings, st state, now time.Time) error {
	body, err := r.build(ctx, s, st, now)
	if err != nil {
		r.log.Debug("usage report: build", "err", err)
		return err
	}
	e := Entry{Type: EntrySent, At: now.UTC(), Body: string(body)}
	postErr := r.post(ctx, s.URL, body)
	if postErr == nil {
		// Remember the day in memory too, so a "sent" line that fails to
		// write below can't lead the next tick to send again.
		r.markSent(startOfDay(now))
	}
	if postErr != nil {
		e.Type, e.Error = EntryFailed, postErr.Error()
		// One info line a day is enough on a computer with no network;
		// the tries after it go to debug.
		level := slog.LevelInfo
		if st.failedToday {
			level = slog.LevelDebug
		}
		r.log.Log(ctx, level, "usage report failed", "err", postErr)
		obs.RecordUsageReport(ctx, "failed")
	} else {
		r.log.Info("usage report sent", "report", Version)
		obs.RecordUsageReport(ctx, "sent")
	}
	if err := appendEntry(r.path, e); err != nil {
		// The record is the user's view of what left the machine, so a
		// failed write is worth an error line; markSent keeps it to one
		// report today all the same.
		r.log.Error("usage report: record", "err", err)
	}
	return postErr
}

// markSent records that the collector took today's report.
func (r *Reporter) markSent(today time.Time) {
	r.dayMu.Lock()
	defer r.dayMu.Unlock()
	r.sentDay = today
}

// sentToday reports whether the collector took a report on today's UTC
// day in this run, whatever the file says.
func (r *Reporter) sentToday(today time.Time) bool {
	r.dayMu.Lock()
	defer r.dayMu.Unlock()
	return r.sentDay.Equal(today)
}

// blocked returns the reason a tick can't send today's report at now, or ""
// when it can.
func (r *Reporter) blocked(s Settings, st state, now time.Time) string {
	return r.check(s, st, now, false)
}

// blockedNow is blocked for SendNow: the same rules without the one-day
// wait after the notice and without today's send minute.
func (r *Reporter) blockedNow(s Settings, st state, now time.Time) string {
	return r.check(s, st, now, true)
}

// check returns the first rule that holds today's report back at now, or
// "". byHand drops the two waits, for a send the user asked for. The order
// sets which reason the Usage screen shows when several hold.
func (r *Reporter) check(s Settings, st state, now time.Time, byHand bool) string {
	today := startOfDay(now)
	switch {
	case doNotTrack(r.env("DO_NOT_TRACK")):
		return reasonDoNotTrack
	case s.Send == "off":
		return reasonOff
	case s.Send != "on":
		return reasonNotChosen
	}
	if _, ok := releaseNumber(r.version); !ok {
		return reasonDevBuild
	}
	switch {
	case st.firstSeen.IsZero():
		return reasonNotSeen
	case !st.lastSent.Before(today) || r.sentToday(today):
		return reasonSentToday
	case byHand:
		return ""
	case !startOfDay(st.firstSeen).Before(today):
		// The first report waits for the next UTC day, so a user who sees
		// the notice and turns it off within hours sends nothing.
		return reasonFirstDay
	case now.Before(today.Add(time.Duration(r.sendMinute(today)) * time.Minute)):
		return reasonNotYet
	}
	return ""
}

// sendMinute returns the minute after midnight UTC that today's report
// waits for, picking a new one the first time it's asked on a new day. A
// restart picks again, which is harmless.
func (r *Reporter) sendMinute(today time.Time) int {
	r.dayMu.Lock()
	defer r.dayMu.Unlock()
	if !r.day.Equal(today) {
		r.day, r.minute = today, r.rand(sendWindow)
	}
	return r.minute
}

// doNotTrack reports whether DO_NOT_TRACK holds a value that asks for no
// reports: anything but "" and "0".
func doNotTrack(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "0"
}

// build returns the report as JSON. A build that isn't a release gets
// "dev" as its version, so the preview in Status still shows every field;
// blocked stops such a build before it sends.
func (r *Reporter) build(ctx context.Context, s Settings, st state, now time.Time) ([]byte, error) {
	number, ok := releaseNumber(r.version)
	if !ok {
		number = "dev"
	}
	first := st.first
	if first.IsZero() {
		first = now
	}
	today := startOfDay(now)
	n, err := r.store.QuestionsBetween(ctx, today.Add(-24*time.Hour), today)
	if err != nil {
		return nil, fmt.Errorf("count yesterday's questions: %w", err)
	}
	rep := Report{
		V:         Version,
		Meru:      number,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Profile:   s.Profile,
		Age:       ageBucket(first, now),
		Questions: questionBucket(n),
	}
	// A struct of strings and an int can't fail to marshal.
	body, _ := json.Marshal(rep)
	return body, nil
}

// post sends body to the collector at base. Anything but 204 No Content
// counts as a failure.
func (r *Reporter) post(ctx context.Context, base string, body []byte) error {
	if base == "" {
		base = DefaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Meru")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	// Read the little the collector says, so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("send: the collector answered %d", resp.StatusCode)
	}
	return nil
}

// Mark records that a client showed the notice (EntrySeen) or that the user
// closed it or chose (EntryDismissed), for the current Version. A second
// mark of the same kind adds nothing, so a client may mark on every status
// poll without growing the file.
func (r *Reporter) Mark(kind, client string) error {
	if kind != EntrySeen && kind != EntryDismissed {
		return fmt.Errorf("usage report: unknown mark %q", kind)
	}
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	now := r.now()
	st, err := readState(r.path, now)
	if err != nil {
		return err
	}
	if (kind == EntrySeen && !st.firstSeen.IsZero()) || (kind == EntryDismissed && st.dismissed) {
		return nil
	}
	return appendEntry(r.path, Entry{Type: kind, At: now.UTC(), Report: Version, Client: client})
}
