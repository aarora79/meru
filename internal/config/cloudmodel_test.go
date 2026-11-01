// This file tests the rule that refuses Ollama's cloud models.

package config

import (
	"strings"
	"testing"
)

func TestCloudModel(t *testing.T) {
	tests := map[string]bool{
		"gpt-oss:120b-cloud":       true,
		"glm-4.6:cloud":            true,
		"Qwen3-Coder:480B-Cloud":   true,
		"qwen3.6:35b-a3b-mxfp8":    false,
		"embeddinggemma-2:440m":    false,
		"cloudy-model:latest":      false, // "cloud" in the name, not the tag
		"hf.co/org/cloud-model:q4": false,
		"llama3":                   false,
		"":                         false,
	}
	for name, want := range tests {
		if got := CloudModel(name); got != want {
			t.Errorf("CloudModel(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLoadRefusesCloudModels(t *testing.T) {
	tests := map[string]string{
		"[models]\nmain = \"gpt-oss:120b-cloud\"\n":                      `models.main: "gpt-oss:120b-cloud" is an Ollama cloud model`,
		"[models]\nembed = \"nomic:cloud\"\n":                            `models.embed: "nomic:cloud" is an Ollama cloud model`,
		"[[models.sets]]\nname = \"big\"\nmain = \"kimi-k2:1t-cloud\"\n": `models.sets "big" main: "kimi-k2:1t-cloud" is an Ollama cloud model`,
	}
	for body, want := range tests {
		_, err := Load(writeConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%q) = %v, want %q", body, err, want)
		}
	}
}
