// This file tests savedLinks, which adds the links to files a turn saved
// when the answer leaves them out.

package agent

import "testing"

func TestSavedLinks(t *testing.T) {
	report := savedFile{name: "report.html", link: "file:///Users/you/meru-output/report.html"}
	spaced := savedFile{name: "my notes.md", link: "file:///Users/you/meru-output/my%20notes.md"}
	tests := []struct {
		name   string
		saved  []savedFile
		answer string
		want   string
	}{
		{"nothing saved", nil, "Here you go.", ""},
		{"the answer points elsewhere", []savedFile{report}, "Click the link above to open it.",
			"\n\nSaved: [report.html](file:///Users/you/meru-output/report.html)"},
		{"the answer holds the link", []savedFile{report}, "Open [it](file:///Users/you/meru-output/report.html).", ""},
		{"the answer holds the path", []savedFile{spaced}, "Saved to /Users/you/meru-output/my notes.md.", ""},
		{"a made-up name doesn't count", []savedFile{report}, "[report](file:///Users/you/meru-output/old-report.html)",
			"\n\nSaved: [report.html](file:///Users/you/meru-output/report.html)"},
		{"two files, one written twice", []savedFile{report, spaced, report}, "Done.",
			"\n\nSaved: [report.html](file:///Users/you/meru-output/report.html), [my notes.md](file:///Users/you/meru-output/my%20notes.md)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := savedLinks(tt.saved, tt.answer); got != tt.want {
				t.Errorf("savedLinks = %q\nwant %q", got, tt.want)
			}
		})
	}
}
