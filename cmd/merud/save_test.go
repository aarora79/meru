// This file tests which client a save's tool call names as its source.

package main

import (
	"testing"

	"github.com/aarora79/meru/internal/rpc"
)

func TestSaveSource(t *testing.T) {
	for _, tt := range []struct{ in, want rpc.Source }{
		{rpc.SourceTUI, rpc.SourceTUI},
		{rpc.SourceDesktop, rpc.SourceDesktop},
		{"", rpc.SourceDesktop},
		{rpc.SourceCLI, rpc.SourceDesktop},
	} {
		if got := saveSource(tt.in); got != tt.want {
			t.Errorf("saveSource(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
