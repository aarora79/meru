// Tests for the usage report's shared words and example.

package rpc

import (
	"encoding/json"
	"testing"
)

// TestUsageReportExample checks that the example is a whole v2 report and
// that only a release version shows as itself.
func TestUsageReportExample(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal([]byte(UsageReportExample("v0.7.7", "darwin", "arm64", "lite")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 13 || got["v"] != 2.0 || got["meru"] != "0.7.7" || got["first"] != true || got["previous"] != "none" {
		t.Errorf("example = %v, want the thirteen fields of a new v0.7.7 install", got)
	}
	for v, want := range map[string]string{
		"v0.8.1": "0.8.1", "v0.7.7-acme": "dev", "(devel) 5325b3e, modified": "dev",
		"v0.4.2-0.20260927021103-5325b3ef94cb": "dev",
	} {
		if got := releaseOrDev(v); got != want {
			t.Errorf("releaseOrDev(%q) = %q, want %q", v, got, want)
		}
	}
}
