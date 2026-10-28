// This file tests the service kind: its manifest rules, the files a
// start needs and their downloads, and the Service supervisor. The
// supervisor's tests start the test binary itself as the program, through
// the real startListening, playing a small whisper-server: serveHelper
// answers its health check only behind the --request-path it was given,
// and exits when asked, which plays a crash.

package connectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarora79/meru/internal/config"
)

// serveHelper is the child program for the service tests. It reads the
// --port and --request-path flags out of args, as whisper-server takes
// them, and serves on 127.0.0.1: <path>/health answers {"status":"ok"},
// <path>/crash exits with code 5, and anything else is 404.
func serveHelper(args []string) {
	var port, prefix string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--port":
			port = args[i+1]
		case "--request-path":
			prefix = args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc(prefix+"/crash", func(http.ResponseWriter, *http.Request) {
		fmt.Fprintln(os.Stderr, "helper: crashing as asked")
		os.Exit(5)
	})
	fmt.Fprintln(os.Stderr, "helper: serving")
	// #nosec G114 -- a test's child program on loopback, killed when the test ends
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// freePort returns a loopback port nothing listens on now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// The bytes the fake downloads hold.
var (
	fakeProgram = []byte("#!/bin/sh\necho whisper-server\n")
	smallModel  = []byte("a small model")
	bigModel    = []byte("a big model, chosen by the profile")
)

// serviceManifest returns a service manifest shaped like whisper's, whose
// downloads come from base and whose program listens on port.
func serviceManifest(base string, port int) Manifest {
	p := strconv.Itoa(port)
	return Manifest{
		ID: "speech", Name: "Speech to text", Kind: KindService, IdleTimeout: "10m",
		Install: Install{
			Type: InstallBinary, Version: "1.9.5",
			Binaries: map[string]Binary{"darwin_arm64": {URL: base + "/server", SHA256: sum(fakeProgram)}},
			Files: []File{
				{Name: "small.bin", URL: base + "/small.bin", SHA256: sum(smallModel)},
				{Name: "big.bin", URL: base + "/big.bin", SHA256: sum(bigModel)},
			},
		},
		Fields: []Field{{ID: "model", Type: FieldChoice, Label: "Model", Choices: []string{"small.bin", "big.bin"}, Default: "small.bin"}},
		Launch: Launch{
			Command:  "{pkg}/server",
			Args:     []string{"--host", "127.0.0.1", "--port", p, "-m", "{file.model}", "--request-path", "/{token}"},
			URL:      "http://127.0.0.1:" + p,
			Port:     port,
			BasePath: "/{token}",
		},
		Health: Health{Path: "/health", Expect: "json_key:status"},
	}
}

// TestValidateService checks that a service manifest passes, and that each
// rule the service kind adds refuses what it should.
func TestValidateService(t *testing.T) {
	if err := Validate(serviceManifest("https://example.test", 8178)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tests := []struct {
		name   string
		change func(m *Manifest)
		want   string
	}{
		{"files on a pip install", func(m *Manifest) {
			m.Kind, m.Install.Type, m.Install.Package, m.Install.Binaries = KindStdio, InstallPip, "x", nil
			m.Launch = Launch{Command: "x"}
			m.Health = Health{Tool: "ping"}
		}, "[[install.files]] is for type binary"},
		{"a file name with a folder", func(m *Manifest) { m.Install.Files[0].Name = "../small.bin" }, "must be a plain file name"},
		{"a file name used twice", func(m *Manifest) { m.Install.Files[1].Name = "small.bin" }, "is used twice"},
		{"a file over http", func(m *Manifest) { m.Install.Files[0].URL = "http://example.test/small.bin" }, "must be an https URL"},
		{"a file with no SHA-256", func(m *Manifest) { m.Install.Files[0].SHA256 = "" }, "64 lower-case hex digits"},
		{"a choice with no file", func(m *Manifest) { m.Fields[0].Choices = append(m.Fields[0].Choices, "huge.bin") }, `the choice "huge.bin" of field model names no [[install.files]] entry`},
		{"a file placeholder on a text field", func(m *Manifest) { m.Fields[0] = Field{ID: "model", Type: FieldText, Label: "Model"} }, "must be a choice"},
		{"a file placeholder with no field", func(m *Manifest) { m.Launch.Args[5] = "{file.nope}" }, "names no field"},
		{"a token in the environment", func(m *Manifest) { m.Launch.Env = map[string]string{"T": "{token}"} }, "{token} is for a service's launch.args"},
		{"a base path without a slash", func(m *Manifest) { m.Launch.BasePath = "{token}" }, `must start with "/"`},
		{"no command", func(m *Manifest) { m.Launch.Command = "" }, "launch.command is missing"},
		{"a port that differs from the URL's", func(m *Manifest) { m.Launch.Port = 9000 }, "must use launch.port 9000"},
		{"a URL off this machine", func(m *Manifest) { m.Launch.URL = "http://example.test:8178" }, "launch.url"},
		{"a health tool", func(m *Manifest) { m.Health = Health{Tool: "ping"} }, "health.path"},
		{"an npm install", func(m *Manifest) {
			m.Install = Install{Type: InstallNPM, Package: "x", Version: "1.0.0"}
		}, `install.type "npm" doesn't fit kind "service"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := serviceManifest("https://example.test", 8178)
			// A manifest's slices are shared with the next test's unless
			// copied, so give this one its own.
			m.Install.Files = slices.Clone(m.Install.Files)
			m.Fields = slices.Clone(m.Fields)
			m.Launch.Args = slices.Clone(m.Launch.Args)
			tt.change(&m)
			err := Validate(m)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate = %v, want an error holding %q", err, tt.want)
			}
		})
	}
}

// TestParseServiceManifest checks that [[install.files]] and base_path
// come through Parse.
func TestParseServiceManifest(t *testing.T) {
	data := []byte(`
id = "speech"
name = "Speech to text"
kind = "service"
idle_timeout = "10m"

[install]
type = "binary"
version = "1.9.5"
url_darwin_arm64 = "https://example.test/server"
sha256_darwin_arm64 = "` + sum(fakeProgram) + `"

[[install.files]]
name = "small.bin"
url = "https://example.test/small.bin"
sha256 = "` + sum(smallModel) + `"

[[field]]
id = "model"
type = "choice"
label = "Model"
choices = ["small.bin"]
default = "small.bin"

[launch]
command = "{pkg}/server"
args = ["-m", "{file.model}", "--request-path", "/{token}"]
url = "http://127.0.0.1:8178"
port = 8178
base_path = "/{token}"

[health]
path = "/health"
expect = "json_key:status"
`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(m); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(m.Install.Files) != 1 || m.Install.Files[0].Name != "small.bin" || m.Launch.BasePath != "/{token}" {
		t.Errorf("parsed %+v and %q", m.Install.Files, m.Launch.BasePath)
	}
	if len(m.Install.Binaries) != 1 {
		t.Errorf("binaries = %v", m.Install.Binaries)
	}
}

// TestLaunchFiles checks which file a start needs, by the field's value
// or its default, and how {file.<id>} and {token} fill in.
func TestLaunchFiles(t *testing.T) {
	m := serviceManifest("https://example.test", 8178)
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"the default", nil, "small.bin"},
		{"the user's choice", map[string]string{"model": "big.bin"}, "big.bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := launchFiles(m, tt.fields)
			if err != nil || len(files) != 1 || files[0].Name != tt.want {
				t.Fatalf("launchFiles = %v, %v; want %s", files, err, tt.want)
			}
			got, err := fillPlaceholders("{file.model} /{token}", m, "/pkg", "/meru", tt.fields, "abc")
			if want := filepath.Join("/pkg", tt.want) + " /abc"; err != nil || got != want {
				t.Errorf("fillPlaceholders = %q, %v; want %q", got, err, want)
			}
		})
	}
	if _, err := launchFiles(m, map[string]string{"model": "huge.bin"}); err == nil {
		t.Error("launchFiles took a file the manifest doesn't list")
	}
}

// TestEnsureFiles checks that a file downloads once, and that a mismatch
// leaves nothing behind.
func TestEnsureFiles(t *testing.T) {
	srv := newFileServer(t, map[string][]byte{"/small.bin": smallModel})
	in, _ := testInstaller(t, nil)
	in.Client = srv.Client()
	inst := Installed{ID: "speech", Dir: t.TempDir()}
	f := File{Name: "small.bin", URL: srv.URL + "/small.bin", SHA256: sum(smallModel)}
	for i := 0; i < 2; i++ {
		if err := in.EnsureFiles(context.Background(), inst, []File{f}, nil); err != nil {
			t.Fatalf("EnsureFiles: %v", err)
		}
	}
	if got := srv.requests.Load(); got != 1 {
		t.Errorf("%d downloads, want 1", got)
	}
	bad := File{Name: "bad.bin", URL: srv.URL + "/small.bin", SHA256: sum([]byte("other"))}
	if err := in.EnsureFiles(context.Background(), inst, []File{bad}, nil); !errors.Is(err, ErrChecksum) {
		t.Errorf("EnsureFiles = %v, want ErrChecksum", err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "bad.bin")); !os.IsNotExist(err) {
		t.Error("a refused download left its file")
	}
}

// testService returns a Service for serviceManifest, with its downloads on
// a test server, whose program is the test binary playing whisper-server.
// It closes the Service when the test ends.
func testService(t *testing.T) (*Service, *fileServer) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the connectors run on macOS and Linux first")
	}
	srv := newFileServer(t, map[string][]byte{"/server": fakeProgram, "/small.bin": smallModel, "/big.bin": bigModel})
	in, _ := testInstaller(t, nil)
	in.Platform = "darwin_arm64"
	in.Client = srv.Client()
	s, err := NewService(serviceManifest(srv.URL, freePort(t)), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The program the install put in place is a shell script; the test
	// starts itself in its stead, with the same arguments.
	s.start = func(ctx, procCtx context.Context, c Cmd, stderr io.Writer, endpoint string) (<-chan struct{}, error) {
		h := helperCmd(append([]string{"serve"}, c.Args...)...)
		h.Path = mustAbs(t, h.Path)
		return startListening(ctx, procCtx, h, stderr, endpoint)
	}
	t.Cleanup(s.Close)
	return s, srv
}

// mustAbs returns p as an absolute path, which startListening needs.
func mustAbs(t *testing.T, p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// waitState waits up to five seconds for the Service to reach state.
func waitState(t *testing.T, s *Service, state string) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := s.Status()
		if st.State == state {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %s (%s), want %s", st.State, st.Sentence, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tokenPath matches the base URL a start gives: the manifest's URL and a
// 32-digit token.
var tokenPath = regexp.MustCompile(`^http://127\.0\.0\.1:\d+/[0-9a-f]{32}$`)

// get returns the status code of a GET of url, or 0 when nothing answers.
func get(url string) int {
	resp, err := http.Get(url) // #nosec G107 -- a test's own loopback URL
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestServiceLifecycle runs a service from off to running and back: off
// until config turns it on, an install of the program and the default
// model only, a start behind a token path that two requests share, a stop
// when idle, and a new token at the next start.
func TestServiceLifecycle(t *testing.T) {
	s, srv := testService(t)
	if _, _, err := s.Use(context.Background()); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatalf("Use before Configure = %v, want the off sentence", err)
	}
	s.Configure(config.Connector{"enabled": true}, nil)
	waitState(t, s, StateOK)
	pkg := s.in.PkgDir(s.m)
	if _, err := os.Stat(filepath.Join(pkg, "small.bin")); err != nil {
		t.Errorf("the default model isn't installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pkg, "big.bin")); !os.IsNotExist(err) {
		t.Error("the install downloaded a model nobody chose")
	}

	// Two requests at once start one program.
	var wg sync.WaitGroup
	bases := make([]string, 2)
	dones := make([]func(), 2)
	for i := range bases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, done, err := s.Use(context.Background())
			if err != nil {
				t.Errorf("Use: %v", err)
				return
			}
			bases[i], dones[i] = b, done
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if bases[0] != bases[1] || !tokenPath.MatchString(bases[0]) {
		t.Fatalf("bases = %q, want one token path", bases)
	}
	if code := get(bases[0] + "/health"); code != http.StatusOK {
		t.Errorf("health behind the token = %d", code)
	}
	root := strings.TrimSuffix(bases[0], bases[0][strings.LastIndex(bases[0], "/"):])
	if code := get(root + "/health"); code != http.StatusNotFound {
		t.Errorf("health without the token = %d, want 404", code)
	}
	if st := s.Status(); st.State != StateOK || !strings.Contains(st.Sentence, "is running") {
		t.Errorf("status = %+v", st)
	}

	// Idle: the program stops once both requests are done.
	s.mu.Lock()
	s.idle = 50 * time.Millisecond
	s.mu.Unlock()
	dones[0]()
	dones[0]() // a second call does nothing
	dones[1]()
	deadline := time.Now().Add(5 * time.Second)
	for get(bases[0]+"/health") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the program still answers after its idle time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := waitState(t, s, StateOK); !strings.Contains(st.Sentence, "starts when you use it") {
		t.Errorf("after idle: %q", st.Sentence)
	}

	b, done, err := s.Use(context.Background())
	if err != nil {
		t.Fatalf("Use after idle: %v", err)
	}
	defer done()
	if b == bases[0] {
		t.Error("the second start reused the first token")
	}
	before := srv.requests.Load()
	s.Configure(config.Connector{"enabled": true}, nil)
	if got := srv.requests.Load(); got != before {
		t.Errorf("a Configure that changed nothing downloaded %d files", got-before)
	}
	if code := get(b + "/health"); code != http.StatusOK {
		t.Error("a Configure that changed nothing stopped the program")
	}
}

// TestServiceDefaults checks that a default from merud, such as the model
// a profile picks, takes the place of the manifest's.
func TestServiceDefaults(t *testing.T) {
	s, _ := testService(t)
	s.Configure(config.Connector{"enabled": true}, map[string]string{"model": "big.bin"})
	st := waitState(t, s, StateOK)
	if got := s.Value("model"); got != "big.bin" {
		t.Errorf("Value(model) = %q, want big.bin", got)
	}
	if _, err := os.Stat(filepath.Join(s.in.PkgDir(s.m), "big.bin")); err != nil {
		t.Errorf("the profile's model isn't installed: %v", err)
	}
	if len(st.Fields) != 1 || st.Fields[0].Default != "big.bin" || st.Fields[0].Value != "" {
		t.Errorf("fields = %+v, want the default big.bin and no value", st.Fields)
	}
	// The user's own choice wins over the default.
	s.Configure(config.Connector{"enabled": true, "model": "small.bin"}, map[string]string{"model": "big.bin"})
	waitState(t, s, StateOK)
	if _, err := os.Stat(filepath.Join(s.in.PkgDir(s.m), "small.bin")); err != nil {
		t.Errorf("the user's model isn't installed: %v", err)
	}
}

// TestServiceNeedsConfig checks that a choice the manifest doesn't offer
// and a key it doesn't know each stop the service with the field to fix.
func TestServiceNeedsConfig(t *testing.T) {
	s, srv := testService(t)
	s.Configure(config.Connector{"enabled": true, "model": "huge.bin"}, nil)
	st := waitState(t, s, StateNeedsConfig)
	if !slices.Equal(st.Fix, []string{"model"}) || !strings.Contains(st.Sentence, "must be one of") {
		t.Errorf("status = %+v", st)
	}
	if _, _, err := s.Use(context.Background()); err == nil {
		t.Error("Use ran a service that needs config")
	}
	s.Configure(config.Connector{"enabled": true, "modle": "small.bin"}, nil)
	if st := waitState(t, s, StateNeedsConfig); !strings.Contains(st.Sentence, "no setting called modle") {
		t.Errorf("status = %+v", st)
	}
	if got := srv.requests.Load(); got != 0 {
		t.Errorf("a service that needs config downloaded %d files", got)
	}
}

// TestServiceCrash checks that a program that exits on its own starts
// again at the next request, and that the fifth crash in ten minutes
// stops the service until Recheck.
func TestServiceCrash(t *testing.T) {
	s, _ := testService(t)
	s.Configure(config.Connector{"enabled": true}, nil)
	waitState(t, s, StateOK)
	for i := 1; i <= crashLimit; i++ {
		base, done, err := s.Use(context.Background())
		if err != nil {
			t.Fatalf("Use %d: %v", i, err)
		}
		_ = get(base + "/crash")
		done()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.mu.Lock()
			n, phase := len(s.crashes), s.phase
			s.mu.Unlock()
			if n == i && phase != servicePhaseRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("crash %d: %d crashes counted, phase %v", i, n, phase)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	st := waitState(t, s, StateFailed)
	if !strings.Contains(st.Sentence, "keeps stopping") {
		t.Errorf("sentence = %q", st.Sentence)
	}
	if _, _, err := s.Use(context.Background()); err == nil || !strings.Contains(err.Error(), "keeps stopping") {
		t.Errorf("Use when failed = %v", err)
	}
	s.Recheck()
	waitState(t, s, StateOK)
	_, done, err := s.Use(context.Background())
	if err != nil {
		t.Fatalf("Use after Recheck: %v", err)
	}
	done()
}

// TestServiceInstallFails checks that a download that doesn't match its
// SHA-256 leaves the service failed, with the reason.
func TestServiceInstallFails(t *testing.T) {
	s, _ := testService(t)
	s.m.Install.Files = slices.Clone(s.m.Install.Files)
	s.m.Install.Files[0].SHA256 = sum([]byte("changed"))
	s.Configure(config.Connector{"enabled": true}, nil)
	st := waitState(t, s, StateFailed)
	if !strings.Contains(st.Sentence, "couldn't install") || !strings.Contains(st.Sentence, "SHA-256") {
		t.Errorf("sentence = %q", st.Sentence)
	}
}

// TestServiceNoBuild checks that a platform the manifest has no download
// for stays off, says why, and downloads nothing. It says why whether the
// connector is switched on or off, so the user isn't told to turn on what
// can't run.
func TestServiceNoBuild(t *testing.T) {
	off, _ := testService(t)
	off.in.Platform = "linux_amd64"
	off.Configure(config.Connector{"enabled": false}, nil)
	if st := waitState(t, off, StateOff); !strings.Contains(st.Sentence, "isn't available on this computer yet") {
		t.Errorf("switched off: sentence = %q", st.Sentence)
	}

	s, srv := testService(t)
	s.in.Platform = "linux_amd64"
	s.Configure(config.Connector{"enabled": true}, nil)
	st := waitState(t, s, StateOff)
	if !strings.Contains(st.Sentence, "isn't available on this computer yet") || !strings.Contains(st.Sentence, "linux_amd64") {
		t.Errorf("sentence = %q", st.Sentence)
	}
	if _, _, err := s.Use(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't available") {
		t.Errorf("Use = %v", err)
	}
	if got := srv.requests.Load(); got != 0 {
		t.Errorf("%d downloads, want none", got)
	}
}

// TestNewServiceRefusesOtherKinds checks the kind check.
func TestNewServiceRefusesOtherKinds(t *testing.T) {
	if _, err := NewService(Manifest{ID: "x", Kind: KindStdio}, nil, nil); err == nil {
		t.Error("NewService took a stdio connector")
	}
}

// TestExpectBody checks each kind of expect.
func TestExpectBody(t *testing.T) {
	tests := []struct {
		expect, body string
		ok           bool
	}{
		{"", "", true},
		{"nonempty", " \n", false},
		{"nonempty", "x", true},
		{"contains:ok", "all ok", true},
		{"contains:ok", "no", false},
		{"json_key:status", `{"status":"ok"}`, true},
		{"json_key:status", `{"other":1}`, false},
		{"json_key:status", `<html>`, false},
	}
	for _, tt := range tests {
		if err := expectBody(tt.expect, []byte(tt.body)); (err == nil) != tt.ok {
			t.Errorf("expectBody(%q, %q) = %v", tt.expect, tt.body, err)
		}
	}
}
