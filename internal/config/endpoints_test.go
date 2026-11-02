// This file tests the [[endpoints]] checks: what an entry must hold, and
// that only an answer model may name one.

package config

import (
	"strings"
	"testing"
)

// labEndpoint is a valid entry, for tests that change one line of it.
const labEndpoint = `
[[endpoints]]
name = "lab"
reach = "local"
api = "openai"
url = "http://127.0.0.1:11434/v1"
api_key = "secret:lab_key"
model = "qwen3.6:35b"
context_length = 32768
vision = true
`

func TestLoadEndpoint(t *testing.T) {
	cfg, err := Load(writeConfig(t, "[models]\nmain = \"@lab\"\n"+labEndpoint+
		"\n[[models.sets]]\nname = \"far\"\nmain = \"@lab\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := FindEndpoint(cfg.Endpoints, "lab")
	if !ok || e.API != APIOpenAI || e.ContextLength != 32768 || !e.Vision || !e.CallsTools() {
		t.Errorf("endpoint = %+v", e)
	}
	if name, ok := EndpointRef(cfg.Models.Main); !ok || name != "lab" {
		t.Errorf("EndpointRef(%q) = %q, %v", cfg.Models.Main, name, ok)
	}
}

func TestNoEndpointsLoadsAsBefore(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Endpoints) != 0 {
		t.Errorf("endpoints = %v, want none", cfg.Endpoints)
	}
	if _, ok := EndpointRef(cfg.Models.Main); ok {
		t.Errorf("the default main model %q names an endpoint", cfg.Models.Main)
	}
}

func TestLoadRefusesBadEndpoints(t *testing.T) {
	// edit replaces one line of labEndpoint.
	edit := func(old, new string) string {
		if !strings.Contains(labEndpoint, old) {
			t.Fatalf("labEndpoint has no %q", old)
		}
		return strings.Replace(labEndpoint, old, new, 1)
	}
	tests := map[string]struct {
		body string
		want string
	}{
		"fast names an endpoint":   {"[models]\nfast = \"@lab\"\n" + labEndpoint, `models.fast: "@lab" names an endpoint, but this model runs on the local Ollama`},
		"embed names an endpoint":  {"[models]\nembed = \"@lab\"\n" + labEndpoint, `models.embed: "@lab" names an endpoint`},
		"a set's fast":             {"[[models.sets]]\nname = \"s\"\nfast = \"@lab\"\n" + labEndpoint, `models.sets "s" fast: "@lab" names an endpoint`},
		"no such endpoint":         {"[models]\nmain = \"@nope\"\n" + labEndpoint, `"@nope" names no [[endpoints]] entry`},
		"off the machine":          {edit(`url = "http://127.0.0.1:11434/v1"`, `url = "http://10.0.0.5:8000/v1"`), `the url must be on this machine`},
		"near-edge for now":        {edit(`reach = "local"`, `reach = "near-edge"`), `reach "near-edge" isn't available yet`},
		"remote for now":           {edit(`reach = "local"`, `reach = "remote"`), `reach "remote" isn't available yet`},
		"unknown reach":            {edit(`reach = "local"`, `reach = "far"`), `reach "far" is unknown`},
		"unknown api":              {edit(`api = "openai"`, `api = "grpc"`), `api "grpc" is unknown`},
		"key written in":           {edit(`api_key = "secret:lab_key"`, `api_key = "sk-abc"`), `api_key must name an entry in secrets.toml`},
		"key on an Ollama":         {edit(`api = "openai"`, `api = "ollama"`), `Ollama's API takes no key`},
		"no model":                 {edit(`model = "qwen3.6:35b"`, `model = ""`), `has no model`},
		"no context length":        {edit("context_length = 32768", "context_length = 0"), `context_length is 0`},
		"no name":                  {edit(`name = "lab"`, `name = ""`), `endpoints: entry 1 has no name`},
		"bad name":                 {edit(`name = "lab"`, `name = "my lab"`), `the name may hold only letters`},
		"the same name used twice": {labEndpoint + labEndpoint, `the name is used twice`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// A cloud model through an Ollama endpoint is still a cloud model.
	body := strings.NewReplacer(`api = "openai"`, `api = "ollama"`, "api_key = \"secret:lab_key\"\n", "",
		`model = "qwen3.6:35b"`, `model = "gpt-oss:120b-cloud"`).Replace(labEndpoint)
	if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "is an Ollama cloud model") {
		t.Errorf("a cloud model on an Ollama endpoint: err = %v", err)
	}
}
