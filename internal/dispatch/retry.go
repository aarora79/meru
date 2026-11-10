// This file holds the hint dispatch adds to a call a server refused for
// missing arguments, so the model can fix the call on its next round. A
// real turn called a server's list tool without the account ID the tool
// needs, read only the server's refusal, never called again, and answered
// from an old file as if the call had told it something.

package dispatch

import (
	"regexp"
	"strings"
)

// missingArgs finds the argument names a schema check says are missing,
// as in `required: missing properties: ["account_number"]`, the error the
// MCP Go SDK and many servers give. The group captures what sits between
// the brackets.
var missingArgs = regexp.MustCompile(`missing properties: \[([^\]]*)\]`)

// retryHint returns a sentence for the end of a failed call's text that
// tells the model which arguments to add and where it might find them, or
// "" when text names no missing argument.
func retryHint(text string) string {
	m := missingArgs.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	names := strings.ReplaceAll(m[1], `"`, "")
	if strings.TrimSpace(names) == "" {
		return ""
	}
	return " Call it again with " + names + ". If you don't know the value, another of your tools may give it first, " +
		"such as one that lists the accounts or items it belongs to."
}
