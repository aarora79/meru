// This file tests how a citation prints and which sources count as cited.

package rpc

import (
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
		{"no marks gives all", "It is 4,200 dollars.", []int{1, 2, 3}},
		{"unknown number gives all", "See [9].", []int{1, 2, 3}},
		{"not a mark", "an array a[i] of [x]", []int{1, 2, 3}},
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
