// This file tests the Bridge methods behind the Library, Setup and the new
// chat actions against an in-process rpc server: each sends the request
// merud expects and hands the reply back, saves show their approval card,
// and the slash commands match `meru chat`'s. attach_test.go covers the
// attachments.

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

// fakeMerud answers the settings ops with canned events and records every
// request it gets.
type fakeMerud struct {
	mu   sync.Mutex // guards reqs
	reqs []rpc.Request
}

// handler is the rpc.Handler for f.
func (f *fakeMerud) handler(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, approve rpc.ApproveFunc) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	conns := rpc.Event{Type: rpc.EventConnections, Connections: []rpc.Connection{{Name: "google", Kind: "mcp", State: rpc.MCPConnected,
		Tools: []rpc.ToolPolicy{{Name: "search_gmail_messages", Policy: rpc.PolicyAllow}}}}}
	folders := rpc.Event{Type: rpc.EventFolders, Folders: []rpc.FolderInfo{{Path: "~/Notes", Exists: true, Files: 12}}}
	switch req.Op {
	case rpc.OpConnections, rpc.OpToolPolicy, rpc.OpMCPAdd, rpc.OpMCPRemove:
		return emit(conns)
	case rpc.OpSecretSet, rpc.OpMemoryForget:
		return nil
	case rpc.OpFolders, rpc.OpFolderAdd, rpc.OpFolderRemove:
		return emit(folders)
	case rpc.OpMemoryList, rpc.OpMemoryAdd:
		return emit(rpc.Event{Type: rpc.EventMemories, Memories: []rpc.MemoryInfo{{ID: "me/name-dana-reyes.md", Kind: "me", Text: "Name is Dana Reyes"}}})
	case rpc.OpSkills, rpc.OpSkillEnable, rpc.OpSkillDisable:
		return emit(rpc.Event{Type: rpc.EventSkills, Skills: []rpc.SkillInfo{{Name: "writing", Builtin: true}}, Text: "odd: no SKILL.md"})
	case rpc.OpModels:
		return emit(rpc.Event{Type: rpc.EventModels, Models: &rpc.ModelsInfo{Profile: "lite", Main: "main-model", Loaded: []string{"main-model"}}})
	case rpc.OpLog:
		return emit(rpc.Event{Type: rpc.EventLog, Log: []rpc.LogEntry{{Tool: "search_gmail_messages", Outcome: "ok"}}})
	case rpc.OpUsage:
		return emit(rpc.Event{Type: rpc.EventUsage, Usage: []rpc.UsageWindow{{Name: rpc.UsageToday, Turns: 3}}})
	case rpc.OpIndexStatus:
		return emit(rpc.Event{Type: rpc.EventStatus, Status: &rpc.IndexStatus{Folders: []string{"~/Notes"}, Documents: 12, Profile: 1}})
	case rpc.OpSaveFile:
		c, err := approve(ctx, rpc.Approval{Name: "write_file", Kind: "builtin",
			Args: json.RawMessage(`{"path":"chats/a.md","content":"# A"}`), Choices: []rpc.Choice{rpc.ChoiceOnce, rpc.ChoiceDeny}})
		if err != nil {
			return err
		}
		if c != rpc.ChoiceOnce {
			return errors.New("not saved: you didn't allow write_file")
		}
		return emit(rpc.Event{Type: rpc.EventSaved, Text: "/Users/dana/meru-output/chats/a.md"})
	}
	return errors.New("unexpected op " + string(req.Op))
}

// requests returns a copy of the requests so far.
func (f *fakeMerud) requests() []rpc.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rpc.Request(nil), f.reqs...)
}

func TestSettingsCalls(t *testing.T) {
	f := &fakeMerud{}
	b, _ := newBridge(startServer(t, f.handler))
	ctx := context.Background()

	steps := []struct {
		name string
		do   func() (any, error)
		want rpc.Request // the last request merud got
	}{
		{"connections", func() (any, error) { return b.Connections(ctx) }, rpc.Request{Op: rpc.OpConnections}},
		{"policy", func() (any, error) { return b.SetPolicy(ctx, "mcp", "google", "send_gmail_message", "ask") },
			rpc.Request{Op: rpc.OpToolPolicy, Policy: &rpc.PolicyChange{Kind: "mcp", Server: "google", Tool: "send_gmail_message", Policy: "ask"}}},
		{"add", func() (any, error) { return b.AddConnection(ctx, "obsidian", "obsidian_api_key", "k-1") }, rpc.Request{Op: rpc.OpMCPAdd, ID: "obsidian"}},
		{"add custom", func() (any, error) {
			return b.AddCustomServer(ctx, rpc.CustomServer{Name: "notes", Command: "npx", Args: []string{"-y", "notes-mcp"},
				Env: []rpc.EnvVar{{Name: "NOTES_TOKEN", Value: "t-1", Secret: true}}})
		}, rpc.Request{Op: rpc.OpMCPAdd, Custom: &rpc.CustomServer{Name: "notes", Command: "npx", Args: []string{"-y", "notes-mcp"},
			Env: []rpc.EnvVar{{Name: "NOTES_TOKEN", Value: "t-1", Secret: true}}}}},
		{"remove", func() (any, error) { return b.RemoveConnection(ctx, "google") }, rpc.Request{Op: rpc.OpMCPRemove, ID: "google"}},
		{"folders", func() (any, error) { return b.Folders(ctx) }, rpc.Request{Op: rpc.OpFolders}},
		{"add folder", func() (any, error) { return b.AddFolder(ctx, "/Users/dana/Notes") }, rpc.Request{Op: rpc.OpFolderAdd, Path: "/Users/dana/Notes"}},
		{"remove folder", func() (any, error) { return b.RemoveFolder(ctx, "~/Notes") }, rpc.Request{Op: rpc.OpFolderRemove, Path: "~/Notes"}},
		{"memories", func() (any, error) { return b.Memories(ctx) }, rpc.Request{Op: rpc.OpMemoryList}},
		{"add memory", func() (any, error) { return b.AddMemory(ctx, "me", "Name is Dana Reyes") }, rpc.Request{Op: rpc.OpMemoryAdd, Kind: "me", Text: "Name is Dana Reyes"}},
		{"forget", func() (any, error) { return nil, b.ForgetMemory(ctx, "me/name-dana-reyes.md") }, rpc.Request{Op: rpc.OpMemoryForget, ID: "me/name-dana-reyes.md"}},
		{"skills", func() (any, error) { return b.Skills(ctx) }, rpc.Request{Op: rpc.OpSkills}},
		{"skill off", func() (any, error) { return b.SetSkill(ctx, "explainer", false) }, rpc.Request{Op: rpc.OpSkillDisable, ID: "explainer"}},
		{"skill on", func() (any, error) { return b.SetSkill(ctx, "explainer", true) }, rpc.Request{Op: rpc.OpSkillEnable, ID: "explainer"}},
		{"models", func() (any, error) { return b.Models(ctx) }, rpc.Request{Op: rpc.OpModels}},
		{"activity", func() (any, error) { return b.Activity(ctx) }, rpc.Request{Op: rpc.OpLog, Limit: defaultActivity}},
		{"usage", func() (any, error) { return b.Usage(ctx) }, rpc.Request{Op: rpc.OpUsage}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if _, err := st.do(); err != nil {
				t.Fatalf("%s: %v", st.name, err)
			}
			reqs := f.requests()
			if got := reqs[len(reqs)-1]; !reflect.DeepEqual(got, st.want) {
				t.Errorf("merud got %+v, want %+v", got, st.want)
			}
		})
	}

	// A catalog server's key goes first, in its own request, and the add
	// after it. A server of the user's own carries its secrets in the add.
	reqs := f.requests()
	for i, r := range reqs {
		if r.Op == rpc.OpMCPAdd && r.Custom == nil && (i == 0 || reqs[i-1].Op != rpc.OpSecretSet || reqs[i-1].ID != "obsidian_api_key" || reqs[i-1].Text != "k-1") {
			t.Errorf("the request before mcp_add is %+v, want the key", reqs[i-1])
		}
	}

	// What comes back is shaped for the page.
	sk, _ := b.Skills(ctx)
	if !reflect.DeepEqual(sk.Warnings, []string{"odd: no SKILL.md"}) {
		t.Errorf("warnings = %v", sk.Warnings)
	}
	cv, _ := b.Connections(ctx)
	if len(cv.Connections) != 1 || cv.Catalog == nil {
		t.Errorf("connections = %+v; want one, and an empty catalog rather than null", cv)
	}
	if err := b.SetSecret(ctx, "obsidian_api_key", "  "); err == nil {
		t.Error("an empty key went to merud")
	}
	if _, err := b.AddMemory(ctx, "me", " "); err == nil {
		t.Error("an empty memory went to merud")
	}
	if _, err := b.AddCustomServer(ctx, rpc.CustomServer{Command: "npx"}); err == nil {
		t.Error("a server with no name went to merud")
	}
}

// TestConnectionsNeverNull checks that a server with no tools reaches the
// page as [] rather than null, which the page's code can't filter: a
// server the user just added, not connected yet, lists none.
func TestConnectionsNeverNull(t *testing.T) {
	b, _ := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		return emit(rpc.Event{Type: rpc.EventConnections, Connections: []rpc.Connection{{Name: "far", Kind: "mcp", State: rpc.MCPNotConnected}}})
	}))
	cv, err := b.Connections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("the view holds null: %s", raw)
	}
}

func TestStatusCounts(t *testing.T) {
	f := &fakeMerud{}
	b, _ := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, a rpc.ApproveFunc) error {
		if req.Op == rpc.OpMCPStatus {
			return emit(rpc.Event{Type: rpc.EventMCPStatus})
		}
		return f.handler(ctx, req, emit, a)
	}))
	s := b.Status(context.Background())
	if !s.Up || s.Folders != 1 || s.Profile != 1 || s.Documents != 12 {
		t.Errorf("status = %+v; want up, one folder, one profile memory, 12 files", s)
	}
}

// TestSaveShowsApproval checks a save: the approval card reaches the page
// as a "save" card, Approve answers it, and the path comes back; a no
// comes back as the error merud sends.
func TestSaveShowsApproval(t *testing.T) {
	for _, tt := range []struct {
		choice  string
		wantErr bool
	}{{"once", false}, {"deny", true}} {
		t.Run(tt.choice, func(t *testing.T) {
			f := &fakeMerud{}
			b, r := newBridge(startServer(t, f.handler))
			type result struct {
				path string
				err  error
			}
			done := make(chan result, 1)
			go func() {
				p, err := b.SaveChat(context.Background(), "2026-09-25T100000-e5f6")
				done <- result{p, err}
			}()
			card := r.waitFor(t, "save approval", func(u Update) bool { return u.Kind == KindApproval })
			if card.Approval.Task != saveTask || !strings.HasPrefix(card.Approval.ID, "save1-") || card.Approval.Label != "Wrote a.md" {
				t.Errorf("card = %+v; want a save card", card.Approval)
			}
			if err := b.Approve(card.Approval.ID, tt.choice); err != nil {
				t.Fatal(err)
			}
			res := <-done
			if tt.wantErr != (res.err != nil) || (!tt.wantErr && res.path != "/Users/dana/meru-output/chats/a.md") {
				t.Errorf("SaveChat = %q, %v", res.path, res.err)
			}
			if got := f.requests()[0]; got.Kind != rpc.SaveChat || got.Source != rpc.SourceDesktop {
				t.Errorf("request = %+v", got)
			}
		})
	}
	b, _ := newBridge("/nonexistent/merud.sock")
	if _, err := b.SaveNote(context.Background(), "", "text"); err == nil {
		t.Error("a save with no session went ahead")
	}
}

func TestReveal(t *testing.T) {
	out := t.TempDir()
	saved := filepath.Join(out, "chats", "a.md")
	if err := os.MkdirAll(filepath.Dir(saved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saved, []byte("# A"), 0o600); err != nil {
		t.Fatal(err)
	}
	var opened string
	b := New(Options{Socket: "/nonexistent", OutputDir: out, Open: func(u string) error { opened = u; return nil }})
	if err := b.Reveal(saved); err != nil || !strings.HasPrefix(opened, "file://") || !strings.HasSuffix(opened, "/chats") {
		t.Errorf("Reveal = %v, opened %q", err, opened)
	}
	if err := b.Reveal("/etc/hosts"); err == nil {
		t.Error("Reveal opened a file outside the output folder")
	}
}

// TestCommandsMatchChat fails when the app's slash commands and `meru
// chat`'s differ. It reads commandList from internal/tui's source, since
// the app may not import that package.
func TestCommandsMatchChat(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "tui", "commands.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const commandList = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no commandList in internal/tui/commands.go")
	}
	want := strings.Split(string(m[1]), ", ")
	var got []string
	for _, c := range (&Bridge{}).Commands() {
		got = append(got, c.Name)
		if c.Description == "" {
			t.Errorf("%s has no description", c.Name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("app commands = %v, meru chat's = %v", got, want)
	}
}

func TestSendScope(t *testing.T) {
	var got rpc.Request
	b, r := newBridge(startServer(t, func(ctx context.Context, req rpc.Request, emit func(rpc.Event) error, _ rpc.ApproveFunc) error {
		got = req
		return nil
	}))
	defer b.ServiceShutdown()
	if err := b.Send("", "what's on Friday?", rpc.ScopeMail); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "end", isEnd(1))
	if got.Scope != rpc.ScopeMail {
		t.Errorf("scope = %q, want mail", got.Scope)
	}
	if err := b.Send("", "hi", "everywhere"); err == nil {
		t.Error("an unknown scope went to merud")
	}
}

func TestQuit(t *testing.T) {
	if err := (&Bridge{}).Quit(); err == nil {
		t.Error("Quit with no quit function = nil")
	}
	quit := false
	b := New(Options{Quit: func() { quit = true }})
	if err := b.Quit(); err != nil || !quit {
		t.Errorf("Quit = %v, quit %v", err, quit)
	}
}
