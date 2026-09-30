// Command fakemcp is a small MCP server for Meru's end-to-end tests. It
// speaks MCP over stdin and stdout, the way merud starts most servers, and
// offers three tools:
//
//   - search returns a fixed note that names the query, so a test can see
//     the arguments arrived; it carries readOnlyHint;
//   - send pretends to send a message; tests put it in a confirm list; it
//     carries destructiveHint;
//   - secret is never allowlisted, so a call to it must be denied.
//
// Started with the Obsidian connector's arguments, which hold "--vault",
// it offers a fourth: obsidian_list_vaults, which names one vault and the
// process ID. The connector tests run it in place of the Obsidian server,
// whose health check calls that tool, and kill it by that process ID. It
// is test code, built by test/e2e, and never ships.
package main

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Argument types. The struct tags name the JSON keys, and the SDK derives
// each tool's input schema from them.
type (
	searchArgs struct {
		Query string `json:"query"`
	}
	sendArgs struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	noArgs struct{}
)

// main serves MCP on stdio until merud closes stdin.
func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "fakemcp", Version: "1"}, nil)
	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}
	// The hints let a probe tell a reading tool from one that acts.
	yes := true
	mcp.AddTool(s, &mcp.Tool{Name: "search", Description: "Search the user's notes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in searchArgs) (*mcp.CallToolResult, any, error) {
			return text("note garden.md: the garden plan for " + in.Query + ": sow tomatoes on 12 April"), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "send", Description: "Send a message.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}},
		func(_ context.Context, _ *mcp.CallToolRequest, in sendArgs) (*mcp.CallToolResult, any, error) {
			return text("sent to " + in.To), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "secret", Description: "Never allowed."},
		func(context.Context, *mcp.CallToolRequest, noArgs) (*mcp.CallToolResult, any, error) {
			return text("you should not see this"), nil, nil
		})
	if slices.Contains(os.Args[1:], "--vault") {
		mcp.AddTool(s, &mcp.Tool{Name: "obsidian_list_vaults", Description: "List the vaults.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(context.Context, *mcp.CallToolRequest, noArgs) (*mcp.CallToolResult, any, error) {
				return text(fmt.Sprintf("vault notes (pid %d)", os.Getpid())), nil, nil
			})
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "fakemcp:", err)
		os.Exit(1)
	}
}
