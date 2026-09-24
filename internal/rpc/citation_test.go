// This file tests how a citation prints and which sources count as cited.

package rpc

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCitationString(t *testing.T) {
	tests := []struct {
		c    Citation
		want string
	}{
		{Citation{N: 1, Path: "~/notes/garden.md", Heading: "Budget", StartLine: 3, EndLine: 8}, `[1] ~/notes/garden.md, "Budget", lines 3–8`},
		{Citation{N: 2, Path: "~/notes/a.md", StartLine: 7, EndLine: 7}, `[2] ~/notes/a.md, line 7`},
		{Citation{N: 3, Path: "/srv/paper.pdf", Page: 4}, `[3] /srv/paper.pdf, page 4`},
		{Citation{N: 4, Path: "~/x.txt"}, `[4] ~/x.txt`},
	}
	for _, tt := range tests {
		if got := tt.c.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestCited(t *testing.T) {
	srcs := []Citation{{N: 1, Path: "a"}, {N: 2, Path: "b"}, {N: 3, Path: "c"}}
	tests := []struct {
		name   string
		answer string
		want   []int
	}{
		{"one mark", "It is 4,200 dollars [1].", []int{1}},
		{"marks in any order", "See [3] and [1].", []int{1, 3}},
		{"list in one pair", "Both say so [2, 3].", []int{2, 3}},
		{"no marks gives none", "It is 4,200 dollars.", nil},
		{"unknown number gives none", "See [9].", nil},
		{"not a mark", "an array a[i] of [x]", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			for _, c := range Cited(tt.answer, srcs) {
				got = append(got, c.N)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Cited(%q) = %v, want %v", tt.answer, got, tt.want)
			}
		})
	}
	if got := Cited("[1]", nil); got != nil {
		t.Errorf("Cited with no sources = %v, want nil", got)
	}
}

func TestFileURL(t *testing.T) {
	home := filepath.FromSlash("/Users/amit")
	tests := []struct {
		path, home, want string
	}{
		{filepath.FromSlash("~/notes/garden.md"), home, "file:///Users/amit/notes/garden.md"},
		{filepath.FromSlash("~/Desktop/My Notes/a b.md"), home, "file:///Users/amit/Desktop/My%20Notes/a%20b.md"},
		{filepath.FromSlash("/srv/shared/plan.md"), home, "file:///srv/shared/plan.md"},
		{filepath.FromSlash("~/notes/garden.md"), "", ""},
	}
	for _, tt := range tests {
		if got := FileURL(tt.path, tt.home); got != tt.want {
			t.Errorf("FileURL(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
		}
	}
}

func TestHyperlink(t *testing.T) {
	got := Hyperlink("file:///a.md", "[1] a.md")
	if want := "\x1b]8;;file:///a.md\x1b\\[1] a.md\x1b]8;;\x1b\\"; got != want {
		t.Errorf("Hyperlink = %q, want %q", got, want)
	}
	if got := Hyperlink("", "[1] a.md"); got != "[1] a.md" {
		t.Errorf("Hyperlink with no URL = %q, want the text alone", got)
	}
}
