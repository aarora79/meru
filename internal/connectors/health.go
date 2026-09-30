// This file holds the health check for an MCP connector, a cheap tool
// call whose answer must hold what the manifest's [health] expect says,
// and the tool list the supervisor keeps from the last check in
// ~/.meru/runtime/state/<id>.json, so the model sees a ready connector's
// tools before the program runs. See ARCHITECTURE.md, "The supervisor".

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runHealthCheck calls the manifest's health tool on cs and checks the
// answer against h.Expect:
//
//   - "nonempty": the answer holds some text;
//   - "contains:<text>": the answer's text holds <text>;
//   - "json_key:<key>": the answer, as a JSON object, has the key <key>;
//   - "": any answer that isn't an error.
//
// A tool that reports an error fails the check too. The error says what
// was wrong, in words a status line can show.
func runHealthCheck(ctx context.Context, cs *mcp.ClientSession, h Health) error {
	if h.Tool == "" {
		return nil
	}
	args := h.Args
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: h.Tool, Arguments: args})
	if err != nil {
		return fmt.Errorf("the check %s failed: %w", h.Tool, err)
	}
	text := resultText(res)
	if res.IsError {
		return fmt.Errorf("the check %s reported an error: %s", h.Tool, firstLine(text))
	}
	switch {
	case h.Expect == "":
		return nil
	case h.Expect == "nonempty":
		if strings.TrimSpace(text) == "" && res.StructuredContent == nil {
			return fmt.Errorf("the check %s gave an empty answer", h.Tool)
		}
	case strings.HasPrefix(h.Expect, "contains:"):
		want := strings.TrimPrefix(h.Expect, "contains:")
		if !strings.Contains(text, want) {
			return fmt.Errorf("the check %s's answer doesn't mention %q", h.Tool, want)
		}
	case strings.HasPrefix(h.Expect, "json_key:"):
		key := strings.TrimPrefix(h.Expect, "json_key:")
		if !hasJSONKey(res, text, key) {
			return fmt.Errorf("the check %s's answer has no %q", h.Tool, key)
		}
	default:
		return fmt.Errorf("the check %s: unknown expect %q", h.Tool, h.Expect)
	}
	return nil
}

// resultText joins the text parts of a tool's answer, one per line.
func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		// c.(*mcp.TextContent) is a type assertion: ok says whether this
		// part is text.
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// hasJSONKey reports whether the answer, its structured part or else its
// text read as JSON, is an object with the key key.
func hasJSONKey(res *mcp.CallToolResult, text, key string) bool {
	var obj map[string]any
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err == nil && json.Unmarshal(b, &obj) == nil {
			_, ok := obj[key]
			return ok
		}
	}
	if json.Unmarshal([]byte(text), &obj) != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

// firstLine returns the first line of s, trimmed, so a long error fits a
// status line.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// toolCache is what state/<id>.json holds: the tools a connector offered
// at its last health check, and the version that offered them. A new
// version may offer other tools, so a cache from another version counts
// as none.
type toolCache struct {
	ID      string      `json:"id"`
	Version string      `json:"version"`
	Tools   []*mcp.Tool `json:"tools"`
}

// statePath is the file that holds connector id's tool list:
// <meruDir>/runtime/state/<id>.json.
func statePath(meruDir, id string) string {
	return filepath.Join(meruDir, "runtime", "state", id+".json")
}

// readToolCache returns the tools saved in path for connector id at
// version, and false when the file is missing, doesn't parse, or belongs
// to another connector or version.
func readToolCache(path, id, version string) ([]*mcp.Tool, bool) {
	data, err := os.ReadFile(path) // #nosec G304 -- a file under ~/.meru/runtime that merud writes
	if err != nil {
		return nil, false
	}
	var c toolCache
	if json.Unmarshal(data, &c) != nil || c.ID != id || c.Version != version || c.Tools == nil {
		return nil, false
	}
	return c.Tools, true
}

// writeToolCache saves tools as connector id's list at version. It writes
// a temporary file and renames it over the old one, so a reader never
// sees half a file.
func writeToolCache(path, id, version string, tools []*mcp.Tool) error {
	if tools == nil {
		tools = []*mcp.Tool{}
	}
	data, err := json.MarshalIndent(toolCache{ID: id, Version: version, Tools: tools}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}
