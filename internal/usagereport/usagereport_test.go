// Tests for the usage report: the send rule, the report's fields and
// buckets, the file, the HTTP client and Status. None touches the network
// except through httptest servers on loopback.

package usagereport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/rpc"
)

// day is the test's "now": 10:00 UTC on a Saturday.
var day = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

// fakeQuestions is a Questions that returns n questions and days active
// days, or err.
type fakeQuestions struct {
	n, days int
	err     error
}

func (f fakeQuestions) QuestionsBetween(context.Context, time.Time, time.Time) (int, error) {
	return f.n, f.err
}

func (f fakeQuestions) ActiveDaysBetween(context.Context, time.Time, time.Time) (int, error) {
	return f.days, f.err
}

// newTest returns a Reporter in a temporary home with version, a clock
// fixed at now, a send minute of minute and an empty environment.
func newTest(t *testing.T, version string, now time.Time, minute int) *Reporter {
	t.Helper()
	r := New(t.TempDir(), version, fakeQuestions{n: 3, days: 4}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return now }
	r.rand = func(int) int { return minute }
	r.env = func(string) string { return "" }
	return r
}

// writeLines writes entries to r's file, one per line.
func writeLines(t *testing.T, r *Reporter, entries ...Entry) {
	t.Helper()
	for _, e := range entries {
		if err := appendEntry(r.path, e); err != nil {
			t.Fatal(err)
		}
	}
}

func seenAt(t time.Time) Entry { return Entry{Type: EntrySeen, At: t, Report: Version} }

func TestBlocked(t *testing.T) {
	yesterday := day.Add(-24 * time.Hour)
	on := Settings{Send: "on", Profile: "lite"}
	tests := []struct {
		name    string
		s       Settings
		env     string // DO_NOT_TRACK
		version string
		lines   []Entry
		now     time.Time
		minute  int
		want    string
	}{
		{name: "ready", s: on, lines: []Entry{seenAt(yesterday)}, want: ""},
		{name: "not chosen", s: Settings{}, lines: []Entry{seenAt(yesterday)}, want: reasonNotChosen},
		{name: "off", s: Settings{Send: "off"}, lines: []Entry{seenAt(yesterday)}, want: reasonOff},
		{name: "do not track", s: on, env: "1", lines: []Entry{seenAt(yesterday)}, want: reasonDoNotTrack},
		{name: "do not track 0 sends", s: on, env: "0", lines: []Entry{seenAt(yesterday)}, want: ""},
		{name: "dev build", s: on, version: "(devel) 5325b3e", lines: []Entry{seenAt(yesterday)}, want: reasonDevBuild},
		{name: "fork version", s: on, version: "v0.7.5-acme", lines: []Entry{seenAt(yesterday)}, want: reasonDevBuild},
		{name: "never seen", s: on, want: reasonNotSeen},
		{name: "seen an older version only", s: on, lines: []Entry{{Type: EntrySeen, At: yesterday, Report: Version - 1}}, want: reasonNotSeen},
		{name: "first day", s: on, lines: []Entry{seenAt(day.Add(-time.Hour))}, want: reasonFirstDay},
		{name: "sent today", s: on, lines: []Entry{seenAt(yesterday), {Type: EntrySent, At: day.Add(-time.Hour)}}, want: reasonSentToday},
		{name: "sent yesterday", s: on, lines: []Entry{seenAt(yesterday.Add(-24 * time.Hour)), {Type: EntrySent, At: yesterday}}, want: ""},
		{name: "failed today tries again", s: on, lines: []Entry{seenAt(yesterday), {Type: EntryFailed, At: day.Add(-time.Hour)}}, want: ""},
		{name: "before the send minute", s: on, lines: []Entry{seenAt(yesterday)}, now: time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC), minute: 90, want: reasonNotYet},
		{name: "after the send minute", s: on, lines: []Entry{seenAt(yesterday)}, now: time.Date(2026, 11, 7, 1, 31, 0, 0, time.UTC), minute: 90, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.version == "" {
				tc.version = "v0.7.5"
			}
			if tc.now.IsZero() {
				tc.now = day
			}
			r := newTest(t, tc.version, tc.now, tc.minute)
			r.env = func(string) string { return tc.env }
			writeLines(t, r, tc.lines...)
			st, err := readState(r.path, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.blocked(tc.s, st, tc.now); got != tc.want {
				t.Errorf("blocked = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReportGolden pins the report's fields. The file name carries
// Version, so adding, renaming or dropping a field fails here until both
// the golden file and Version change, and a new Version shows the notice
// again before anything sends.
func TestReportGolden(t *testing.T) {
	got, err := json.Marshal(Report{
		V: Version, Meru: "0.7.7", OS: "darwin", Arch: "arm64", Profile: "lite", Age: "month", Questions: "1-5",
		ActiveDays: "3-5", Streak: "2-4", Previous: "0.7.6", WeekFirst: true, MonthFirst: false, First: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("report-v%d.json", Version)))
	if err != nil {
		t.Fatalf("%v; a new Version needs a new golden file", err)
	}
	if string(got) != strings.TrimSpace(string(want)) {
		t.Errorf("report JSON\n got %s\nwant %s", got, want)
	}
}

func TestBuckets(t *testing.T) {
	for _, tc := range []struct {
		days float64
		want string
	}{{0, "week"}, {6.9, "week"}, {7, "month"}, {30.9, "month"}, {31, "year"}, {365.9, "year"}, {366, "older"}} {
		now := day.Add(time.Duration(tc.days * 24 * float64(time.Hour)))
		if got := ageBucket(day, now); got != tc.want {
			t.Errorf("ageBucket(%v days) = %q, want %q", tc.days, got, tc.want)
		}
	}
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "0"}, {1, "1-5"}, {5, "1-5"}, {6, "6-20"}, {20, "6-20"}, {21, "21+"}, {500, "21+"}} {
		if got := questionBucket(tc.n); got != tc.want {
			t.Errorf("questionBucket(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestReleaseNumber(t *testing.T) {
	for v, want := range map[string]string{
		"v0.7.5": "0.7.5", "v10.20.30": "10.20.30",
		"0.7.5": "", "v0.7.5-acme": "", "v0.4.2-0.20260927021103-5325b3ef94cb": "", "(devel) 5325b3e": "", "": "",
	} {
		got, ok := releaseNumber(v)
		if got != want || ok != (want != "") {
			t.Errorf("releaseNumber(%q) = %q, %v; want %q", v, got, ok, want)
		}
	}
}

// TestTickSends runs one tick against a fake collector: it gets exactly the
// body the file records, and a second tick the same day sends nothing.
func TestTickSends(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if req.URL.Path != "/v1/report" || req.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r := newTest(t, "v0.7.5", day, 0)
	writeLines(t, r, seenAt(day.Add(-20*24*time.Hour)))
	s := func() (Settings, error) { return Settings{Send: "on", URL: srv.URL, Profile: "full"}, nil }
	r.tick(context.Background(), s, day)
	r.tick(context.Background(), s, day.Add(time.Hour))

	if len(bodies) != 1 {
		t.Fatalf("collector got %d reports, want 1", len(bodies))
	}
	want := `{"v":2,"meru":"0.7.5","os":"` + osArch()[0] + `","arch":"` + osArch()[1] + `","profile":"full","age":"month","questions":"1-5",` +
		`"active_days":"3-5","streak":"1","previous":"none","week_first":true,"month_first":true,"first":true}`
	if bodies[0] != want {
		t.Errorf("body\n got %s\nwant %s", bodies[0], want)
	}
	st, err := readState(r.path, day)
	if err != nil {
		t.Fatal(err)
	}
	if st.recent[0].Type != EntrySent || st.recent[0].Body != bodies[0] {
		t.Errorf("last line = %+v, want a sent line with the body sent", st.recent[0])
	}
}

// TestPost checks what counts as sent: 204 only. A redirect is not
// followed, and HTTPS_PROXY doesn't route the report.
func TestPost(t *testing.T) {
	var proxied atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)

	var followed atomic.Int64
	for _, tc := range []struct {
		name   string
		status int
		ok     bool
	}{
		{"204 is sent", http.StatusNoContent, true},
		{"200 is not", http.StatusOK, false},
		{"400 is not", http.StatusBadRequest, false},
		{"429 is not", http.StatusTooManyRequests, false},
		{"a redirect is not followed", http.StatusFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/elsewhere" {
					followed.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if tc.status == http.StatusFound {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			r := newTest(t, "v0.7.5", day, 0)
			err := r.post(context.Background(), srv.URL, []byte(`{}`))
			if (err == nil) != tc.ok {
				t.Errorf("post error = %v, want ok %v", err, tc.ok)
			}
		})
	}
	if followed.Load() != 0 {
		t.Error("the client followed a redirect")
	}
	if proxied.Load() != 0 {
		t.Error("the report went through HTTPS_PROXY")
	}
	// Go never sends a loopback request through a proxy, so the servers
	// above can't show the rule for a real collector. Check the client's
	// setting itself: nil means no proxy, whatever the environment says.
	r := newTest(t, "v0.7.5", day, 0)
	if tr, ok := r.client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Error("the client's transport reads a proxy; the report must go direct")
	}
}

// TestTickRecordsFailure checks that a send the collector refuses leaves a
// "failed" line, and that the next tick tries again.
func TestTickRecordsFailure(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	r := newTest(t, "v0.7.5", day, 0)
	writeLines(t, r, seenAt(day.Add(-48*time.Hour)))
	s := func() (Settings, error) { return Settings{Send: "on", URL: srv.URL, Profile: "lite"}, nil }
	r.tick(context.Background(), s, day)
	r.tick(context.Background(), s, day.Add(30*time.Minute))
	if hits.Load() != 2 {
		t.Errorf("collector got %d tries, want 2", hits.Load())
	}
	st, _ := readState(r.path, day)
	if !st.failedToday || st.recent[0].Type != EntryFailed || st.recent[0].Error == "" {
		t.Errorf("state = %+v, want a failed line with its reason", st)
	}
}

// TestAppendEntryMode checks that the file ends up 0600, whether
// appendEntry creates it or finds it with a looser mode.
func TestAppendEntryMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendEntry(path, seenAt(day)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

// TestAppendEntryConcurrent appends from several goroutines at once, as the
// loop and an op could, and checks every line still parses. Run with -race.
func TestAppendEntryConcurrent(t *testing.T) {
	r := newTest(t, "v0.7.5", day, 0)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		// go starts the function in a goroutine of its own; wg.Wait below
		// waits for all twenty.
		go func() {
			defer wg.Done()
			r.fileMu.Lock()
			defer r.fileMu.Unlock()
			if err := appendEntry(r.path, Entry{Type: EntryFailed, At: day, Error: strings.Repeat("x", i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 20 {
		t.Fatalf("%d lines, want 20", len(lines))
	}
	for _, l := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Errorf("line %q: %v", l, err)
		}
	}
}

// TestReadStateSkipsBadLines checks that a damaged line is skipped and that
// a file with no readable "seen" line holds the report back.
func TestReadStateSkipsBadLines(t *testing.T) {
	r := newTest(t, "v0.7.5", day, 0)
	if err := os.WriteFile(r.path, []byte(fmt.Sprintf("not json\n{\"type\":\"seen\",\"at\":\"2026-11-01T00:00:00Z\",\"report\":%d}\n{\"type\":\"sent\",\"at\":", Version)), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := readState(r.path, day)
	if err != nil {
		t.Fatal(err)
	}
	if st.firstSeen.IsZero() || st.sentEver {
		t.Errorf("state = %+v, want the seen line read and the cut-off sent line skipped", st)
	}
	if err := os.WriteFile(r.path, []byte("garbage\nmore garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ = readState(r.path, day)
	if got := r.blocked(Settings{Send: "on"}, st, day); got != reasonNotSeen {
		t.Errorf("a garbage file gives %q, want %q", got, reasonNotSeen)
	}
}

// TestMarkOnce checks that marking the same thing twice writes one line.
func TestMarkOnce(t *testing.T) {
	r := newTest(t, "v0.7.5", day, 0)
	for range 3 {
		if err := r.Mark(EntrySeen, "desktop"); err != nil {
			t.Fatal(err)
		}
		if err := r.Mark(EntryDismissed, "desktop"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(r.path)
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Errorf("%d lines after three marks of each kind, want 2:\n%s", n, b)
	}
	if err := r.Mark("sent", "desktop"); err == nil {
		t.Error("Mark took a kind it shouldn't")
	}
}

func TestStatus(t *testing.T) {
	yesterday := day.Add(-24 * time.Hour)
	tests := []struct {
		name       string
		s          Settings
		env        string
		lines      []Entry
		wantBanner string
		wantNextAt string
		wantSent   bool
	}{
		{name: "upgrade asks", s: Settings{}, wantBanner: rpc.BannerAsk},
		{name: "new install tells", s: Settings{Send: "on"}, wantBanner: rpc.BannerNew},
		{name: "first day", s: Settings{Send: "on"}, lines: []Entry{seenAt(day.Add(-time.Hour))}, wantBanner: rpc.BannerNew, wantNextAt: "2026-11-08T00:00:00Z"},
		{name: "dismissed", s: Settings{Send: "on"}, lines: []Entry{seenAt(yesterday), {Type: EntryDismissed, At: yesterday, Report: Version}}, wantNextAt: "2026-11-07T10:00:00Z"},
		{name: "off shows nothing", s: Settings{Send: "off"}},
		{name: "do not track shows nothing", s: Settings{}, env: "1"},
		{name: "sent", s: Settings{Send: "on"}, lines: []Entry{seenAt(yesterday), {Type: EntrySent, At: day.Add(-time.Hour)}}, wantBanner: rpc.BannerNew, wantNextAt: "2026-11-08T00:00:00Z", wantSent: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTest(t, "v0.7.5", day, 0)
			r.env = func(string) string { return tc.env }
			writeLines(t, r, tc.lines...)
			tc.s.Profile = "lite"
			got, err := r.Status(context.Background(), tc.s)
			if err != nil {
				t.Fatal(err)
			}
			if got.Banner != tc.wantBanner || got.NextAt != tc.wantNextAt || got.Sent != tc.wantSent {
				t.Errorf("Status = banner %q, next at %q, sent %v; want %q, %q, %v",
					got.Banner, got.NextAt, got.Sent, tc.wantBanner, tc.wantNextAt, tc.wantSent)
			}
			var rep Report
			if err := json.Unmarshal([]byte(got.Next), &rep); err != nil || rep.Meru != "0.7.5" || rep.Questions != "1-5" {
				t.Errorf("Next = %s, %v; want the report merud would send", got.Next, err)
			}
			if len(got.Recent) != len(tc.lines) {
				t.Errorf("%d recent lines, want %d", len(got.Recent), len(tc.lines))
			}
		})
	}
}

// TestStatusShowsURL checks that a collector other than Meru's own is named,
// so a changed destination is in plain view.
func TestStatusShowsURL(t *testing.T) {
	r := newTest(t, "v0.7.5", day, 0)
	got, err := r.Status(context.Background(), Settings{Send: "on", URL: "https://collector.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://collector.example.org" {
		t.Errorf("URL = %q", got.URL)
	}
	got, _ = r.Status(context.Background(), Settings{Send: "on"})
	if got.URL != "" {
		t.Errorf("URL = %q for the default collector, want empty", got.URL)
	}
}

// TestStatusDevBuildPreview checks that a development build still shows a
// full preview, with "dev" as its version, while it sends nothing.
func TestStatusDevBuildPreview(t *testing.T) {
	r := newTest(t, "(devel) 5325b3e", day, 0)
	got, err := r.Status(context.Background(), Settings{Send: "on", Profile: "lite"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Blocked != reasonDevBuild || !strings.Contains(got.Next, `"meru":"dev"`) {
		t.Errorf("Status = %+v, want blocked as a dev build with a dev preview", got)
	}
}

// TestLoopStops checks that Loop returns when its context ends.
func TestLoopStops(t *testing.T) {
	r := newTest(t, "v0.7.5", day, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Loop(ctx, func() (Settings, error) { return Settings{}, nil })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop didn't stop when its context ended")
	}
}

// osArch returns this machine's system and chip, as the report names them.
func osArch() [2]string {
	return [2]string{runtime.GOOS, runtime.GOARCH}
}

// TestSendNow checks the button's rules: it skips the one-day wait and the
// send minute, and keeps every other rule, so a click never sends a second
// report in one day.
func TestSendNow(t *testing.T) {
	early := time.Date(2026, 11, 7, 0, 10, 0, 0, time.UTC)
	tests := []struct {
		name   string
		s      Settings
		lines  []Entry
		now    time.Time
		status int // what the fake collector answers
		sends  bool
		errHas string
	}{
		{name: "the day the notice first showed", s: Settings{Send: "on"}, lines: []Entry{seenAt(day.Add(-time.Hour))}, now: day, status: 204, sends: true},
		{name: "before the send minute", s: Settings{Send: "on"}, lines: []Entry{seenAt(day.Add(-48 * time.Hour))}, now: early, status: 204, sends: true},
		{name: "already sent today", s: Settings{Send: "on"}, lines: []Entry{seenAt(day.Add(-48 * time.Hour)), {Type: EntrySent, At: day.Add(-time.Hour)}}, now: day, errHas: reasonSentToday},
		{name: "off", s: Settings{Send: "off"}, lines: []Entry{seenAt(day.Add(-48 * time.Hour))}, now: day, errHas: reasonOff},
		{name: "not chosen", s: Settings{}, lines: []Entry{seenAt(day.Add(-48 * time.Hour))}, now: day, errHas: reasonNotChosen},
		{name: "notice never shown", s: Settings{Send: "on"}, now: day, errHas: reasonNotSeen},
		{name: "collector refuses", s: Settings{Send: "on"}, lines: []Entry{seenAt(day.Add(-48 * time.Hour))}, now: day, status: 500, sends: true, errHas: "500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			r := newTest(t, "v0.7.5", tc.now, 119)
			writeLines(t, r, tc.lines...)
			tc.s.URL, tc.s.Profile = srv.URL, "lite"
			err := r.SendNow(context.Background(), tc.s)
			if (hits.Load() == 1) != tc.sends {
				t.Errorf("collector got %d reports, want sends %v", hits.Load(), tc.sends)
			}
			if tc.errHas == "" && err != nil {
				t.Errorf("SendNow: %v", err)
			}
			if tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)) {
				t.Errorf("SendNow error = %v, want it to say %q", err, tc.errHas)
			}
			// A send writes its line, sent or failed; a refusal writes none.
			st, _ := readState(r.path, tc.now)
			if wrote := len(st.recent) > len(tc.lines); wrote != tc.sends {
				t.Errorf("record grew %v, want %v", wrote, tc.sends)
			}
		})
	}
}

// TestStatusCanSend checks that the button is live only when a click would
// send: on, the notice shown, and nothing sent today.
func TestStatusCanSend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     Settings
		lines []Entry
		want  bool
	}{
		{"on and seen today", Settings{Send: "on"}, []Entry{seenAt(day.Add(-time.Hour))}, true},
		{"sent today", Settings{Send: "on"}, []Entry{seenAt(day.Add(-48 * time.Hour)), {Type: EntrySent, At: day.Add(-time.Hour)}}, false},
		{"off", Settings{Send: "off"}, []Entry{seenAt(day.Add(-time.Hour))}, false},
		{"never seen", Settings{Send: "on"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTest(t, "v0.7.5", day, 0)
			writeLines(t, r, tc.lines...)
			got, err := r.Status(context.Background(), tc.s)
			if err != nil {
				t.Fatal(err)
			}
			if got.CanSend != tc.want {
				t.Errorf("CanSend = %v, want %v", got.CanSend, tc.want)
			}
		})
	}
}

// TestRecordWriteFailsSendsOnce checks the promise of one report a day when
// the record can't be written: after the collector takes a report and the
// "sent" line fails to land, neither the next tick nor the button sends
// again that day.
func TestRecordWriteFailsSendsOnce(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	r := newTest(t, "v0.7.5", day, 0)
	writeLines(t, r, seenAt(day.Add(-48*time.Hour)))
	// A read-only record: readState still reads it, appendEntry can't add.
	if err := os.Chmod(r.path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.path, 0o600) })
	s := Settings{Send: "on", URL: srv.URL, Profile: "lite"}
	settings := func() (Settings, error) { return s, nil }
	r.tick(context.Background(), settings, day)
	if hits.Load() != 1 {
		t.Fatalf("first tick: collector got %d, want 1", hits.Load())
	}
	st, _ := readState(r.path, day)
	if st.sentEver {
		t.Skip("the record took the line anyway; this system ignores 0400 for its owner")
	}
	r.tick(context.Background(), settings, day.Add(30*time.Minute))
	if err := r.SendNow(context.Background(), s); err == nil || !strings.Contains(err.Error(), reasonSentToday) {
		t.Errorf("SendNow after a lost record = %v, want it refused as sent today", err)
	}
	if hits.Load() != 1 {
		t.Errorf("collector got %d reports in one day, want 1", hits.Load())
	}
	r.tick(context.Background(), settings, day.Add(24*time.Hour))
	if hits.Load() != 2 {
		t.Errorf("next day: collector got %d in all, want 2", hits.Load())
	}
}
