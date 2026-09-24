// This file tests parseFrontmatter on the YAML shapes skill files use and on
// the mistakes it must reject.

package skills

import (
	"maps"
	"strings"
	"testing"
)

// TestParseFrontmatter feeds parseFrontmatter good and bad headers and checks
// the fields, the body, or the error.
func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantFields map[string]string
		wantBody   string
		wantErr    string // a piece of the error text; "" means no error
	}{
		{
			name:       "simple",
			in:         "---\nname: writing\ndescription: Write well.\n---\n\n# Body\n\ntext\n",
			wantFields: map[string]string{"name": "writing", "description": "Write well."},
			wantBody:   "# Body\n\ntext",
		},
		{
			name:       "plain value over indented lines",
			in:         "---\nname: portfolio-review\ndescription: Review holdings. Use when asked about\n  positions, concentration, or trades.\n---\nbody\n",
			wantFields: map[string]string{"name": "portfolio-review", "description": "Review holdings. Use when asked about positions, concentration, or trades."},
			wantBody:   "body",
		},
		{
			name:       "folded block",
			in:         "---\nname: a\ndescription: >\n  one\n  two\n\n  three\n---\nbody",
			wantFields: map[string]string{"name": "a", "description": "one two\nthree\n"},
			wantBody:   "body",
		},
		{
			name:       "literal block, strip",
			in:         "---\nname: a\ndescription: |-\n  one\n  two\n---\nbody",
			wantFields: map[string]string{"name": "a", "description": "one\ntwo"},
			wantBody:   "body",
		},
		{
			name:       "nested map and quotes",
			in:         "---\nname: 'it''s'\nlicense: MIT-0\nmetadata:\n  author: aarora79\n  version: \"1.5\"\n---\nbody",
			wantFields: map[string]string{"name": "it's", "license": "MIT-0", "metadata.author": "aarora79", "metadata.version": "1.5"},
			wantBody:   "body",
		},
		{
			name:       "list kept as text",
			in:         "---\nname: a\ntools:\n  - Read\n  - Grep\n---\nbody",
			wantFields: map[string]string{"name": "a", "tools": "- Read\n- Grep"},
			wantBody:   "body",
		},
		{
			name:       "windows line endings and comments",
			in:         "\uFEFF---\r\n# a comment\r\nname: a\r\n---\r\nbody\r\n",
			wantFields: map[string]string{"name": "a"},
			wantBody:   "body",
		},
		{name: "missing frontmatter", in: "# Just a title\n", wantErr: "no frontmatter"},
		{name: "empty file", in: "", wantErr: "no frontmatter"},
		{name: "never closes", in: "---\nname: a\n", wantErr: "never closes"},
		{name: "bad key", in: "---\nna me: a\n---\n", wantErr: "want \"key: value\""},
		{name: "no space after colon", in: "---\nname:a\n---\n", wantErr: "space after"},
		{name: "duplicate key", in: "---\nname: a\nname: b\n---\n", wantErr: "appears twice"},
		{name: "duplicate nested key", in: "---\nmetadata:\n  a: 1\nmetadata:\n  b: 2\n---\n", wantErr: "appears twice"},
		{name: "tab indent", in: "---\ndescription: a\n\tb\n---\n", wantErr: "tabs"},
		{name: "stray indent", in: "---\n  name: a\n---\n", wantErr: "no key above"},
		{name: "unclosed quote", in: "---\nname: \"a\n---\n", wantErr: "closing quote"},
		{name: "multi-line quote", in: "---\nname: \"a\"\n  b\n---\n", wantErr: "one line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, body, err := parseFrontmatter([]byte(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !maps.Equal(fields, tt.wantFields) {
				t.Errorf("fields = %q, want %q", fields, tt.wantFields)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}
