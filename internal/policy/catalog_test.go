// This file holds the rule for the starter servers in internal/catalog:
// a tool whose name says it sends, shares, deletes, creates or changes
// something asks before it runs. A user who adds a catalog server trusts
// the catalog's lists, so a send that runs unasked there would act on
// any instruction a hostile email or page slips to the model
// (docs/design/security.md).

package policy

import (
	"regexp"
	"slices"
	"testing"

	"github.com/aarora79/meru/internal/catalog"
)

// actionWord matches a tool name that acts rather than reads: one of
// these words as a whole part of the name, between underscores, dots or
// dashes, so "send_gmail_message" and "obsidian_append_content" match and
// "get_events" doesn't. (?i) ignores case.
var actionWord = regexp.MustCompile(`(?i)(^|[_.-])(send|share|delete|remove|trash|create|update|manage|import|append|upload|post|write|move|rename|patch|put|modify|edit|forward|reply)([_.-]|$)`)

// actsUnasked returns the tools of e that actionWord matches and that
// neither confirm nor always_confirm names.
func actsUnasked(e catalog.Entry) []string {
	var out []string
	for _, tool := range e.Allow {
		if actionWord.MatchString(tool) && !slices.Contains(e.Confirm, tool) && !slices.Contains(e.AlwaysConfirm, tool) {
			out = append(out, tool)
		}
	}
	return out
}

func TestCatalogActionsAsk(t *testing.T) {
	for _, e := range catalog.Entries() {
		if bad := actsUnasked(e); len(bad) > 0 {
			t.Errorf("catalog entry %q allows %v without confirm or always_confirm; a tool that acts must ask first", e.Name, bad)
		}
	}
}

// TestCatalogActionsAskCatches checks the rule against made-up entries, so
// a broken pattern can't pass the real catalog by matching nothing.
func TestCatalogActionsAskCatches(t *testing.T) {
	tests := []struct {
		name string
		e    catalog.Entry
		want []string
	}{
		{"a send that asks", catalog.Entry{Allow: []string{"send_mail"}, AlwaysConfirm: []string{"send_mail"}}, nil},
		{"a send that doesn't", catalog.Entry{Allow: []string{"search_mail", "send_mail"}}, []string{"send_mail"}},
		{"a share in confirm", catalog.Entry{Allow: []string{"share_file"}, Confirm: []string{"share_file"}}, nil},
		{"a delete, dotted", catalog.Entry{Allow: []string{"files.delete"}}, []string{"files.delete"}},
		{"reads only", catalog.Entry{Allow: []string{"get_events", "list_calendars", "search_drive_files"}}, nil},
		{"a word inside another", catalog.Entry{Allow: []string{"get_postal_code"}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := actsUnasked(tc.e); !slices.Equal(got, tc.want) {
				t.Errorf("actsUnasked = %v, want %v", got, tc.want)
			}
		})
	}
}
