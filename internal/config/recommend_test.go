// This file tests the model recommendation by memory.

package config

import (
	"slices"
	"testing"
)

// TestRecommend checks which set each size of Mac gets.
func TestRecommend(t *testing.T) {
	tests := []struct {
		memGB    int
		wantMain string // "" means the profile's own answer model
	}{
		{8, ""},
		{16, ""},
		{31, ""},
		{32, "gemma4:26b-a4b-it-qat"},
		{48, "gemma4:26b-a4b-it-qat"},
		{63, "gemma4:26b-a4b-it-qat"},
		{64, "qwen3.6:35b-a3b-mxfp8"},
		{128, "qwen3.6:35b-a3b-mxfp8"},
	}
	for _, tt := range tests {
		r := Recommend(tt.memGB)
		if r.Main != tt.wantMain || r.Profile != "lite" {
			t.Errorf("Recommend(%d) = profile %q main %q, want lite and %q", tt.memGB, r.Profile, r.Main, tt.wantMain)
		}
	}
}

// TestRecommendationModels checks that each row names a known profile and
// lists its models once each.
func TestRecommendationModels(t *testing.T) {
	for _, r := range Recommendations() {
		models := r.Models()
		if len(models) == 0 {
			t.Errorf("%s: no models; is profile %q known?", r.Label, r.Profile)
		}
		if r.Main != "" && !slices.Contains(models, r.Main) {
			t.Errorf("%s: models %v leave out main %q", r.Label, models, r.Main)
		}
		lite, _ := ProfileModels("lite")
		if r.Main == "" && !slices.Equal(models, []string{lite.Fast, lite.Embed}) {
			t.Errorf("%s: models = %v", r.Label, models)
		}
	}
	// The table must start at 0 GB, so every Mac gets a row.
	if Recommendations()[0].MinMemoryGB != 0 {
		t.Error("the first row must suit every Mac")
	}
}
