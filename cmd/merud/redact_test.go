// This file tests that a key an MCP server echoes back in an error stays out
// of merud.log and out of what mcp_status sends the clients (#238).

package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/config"
	"github.com/aarora79/meru/internal/index"
	"github.com/aarora79/meru/internal/memory"
	"github.com/aarora79/meru/internal/obs"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
)

func TestServerErrorKeyIsRedacted(t *testing.T) {
	const key = "opensesame-opensesame"
	// The server refuses every request with a JSON-RPC error whose message
	// holds the key it was sent, as a careless server does. The SDK puts that
	// message in its error, which the pool logs and keeps as LastError.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"invalid key %s"}}`, r.Header.Get("X-Key"))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.toml"), []byte("test_key = \""+key+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf("[[mcp.servers]]\nname = \"careless\"\nurl = %q\nheaders = { X-Key = \"secret:test_key\" }\nallow = [\"x\"]\n", srv.URL)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// The log merud would write, through the same handler and redactor.
	var logBuf bytes.Buffer
	red := obs.NewRedactor()
	log := slog.New(obs.LogHandler(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}), red))

	eng := &fakeEngine{version: "0.40.5"}
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "meru.db"), EmbedModel: cfg.Models.Embed, Dims: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mem, err := memory.Open(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.New(cfg.Index, st, eng, log)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := newToolService(ctx, cfg, cfgPath, st, mem, ix, searchAdapter{st: st, eng: eng}, eng, nil, log, red)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tools.Close)

	var rows []rpc.MCPStatus
	if err := tools.handleMCPStatus(func(ev rpc.Event) error {
		rows = append(rows, ev.MCP...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Err == "" {
		t.Fatalf("mcp_status rows = %+v, want one failed server", rows)
	}
	if strings.Contains(rows[0].Err, key) || !strings.Contains(rows[0].Err, "[secret:test_key]") {
		t.Errorf("mcp_status Err = %q, want the key hidden", rows[0].Err)
	}
	for _, s := range tools.dispatcher.Servers() {
		if strings.Contains(s.LastError, key) {
			t.Errorf("connections LastError for %s holds the key: %q", s.Name, s.LastError)
		}
	}
	if out := logBuf.String(); strings.Contains(out, key) || !strings.Contains(out, "[secret:test_key]") {
		t.Errorf("the log holds the key, or never logged the error:\n%s", out)
	}
}
