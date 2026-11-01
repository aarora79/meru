// This file holds the rule for Ollama's cloud models. Ollama can run a
// model on ollama.com instead of this machine: a model whose tag ends in
// "cloud", such as gpt-oss:120b-cloud, answers through the local Ollama
// API, so a request to 127.0.0.1 sends the whole prompt off the machine.
// Meru's loopback rule can't see that, so config refuses such a model by
// name, and the engine checks what Ollama says about each model as well
// (AGENTS.md, non-negotiable 1).

package config

import (
	"fmt"
	"strings"
)

// CloudModel reports whether name, an Ollama model name such as
// "qwen3.6:35b" or "gpt-oss:120b-cloud", names one of Ollama's cloud
// models: its tag, after the last ":", is "cloud" or ends in "-cloud".
// A name with no tag is the "latest" tag, which is local.
func CloudModel(name string) bool {
	i := strings.LastIndex(name, ":")
	if i < 0 {
		return false
	}
	tag := strings.ToLower(strings.TrimSpace(name[i+1:]))
	return tag == "cloud" || strings.HasSuffix(tag, "-cloud")
}

// errCloudModel is the line for a cloud model named at key, such as
// "models.main".
func errCloudModel(key, name string) error {
	return fmt.Errorf("%s: %q is an Ollama cloud model, which runs on ollama.com and would send every prompt "+
		"off this machine; Meru runs models on this machine only, so name a model Ollama runs here", key, name)
}

// checkCloudModels returns one error for each model in models and sets
// that CloudModel says runs in Ollama's cloud.
func checkCloudModels(models Models) []error {
	var errs []error
	check := func(key, name string) {
		if CloudModel(name) {
			errs = append(errs, errCloudModel(key, name))
		}
	}
	check("models.fast", models.Fast)
	check("models.main", models.Main)
	check("models.embed", models.Embed)
	for _, s := range models.Sets {
		check(fmt.Sprintf("models.sets %q main", s.Name), s.Main)
		check(fmt.Sprintf("models.sets %q fast", s.Name), s.Fast)
		check(fmt.Sprintf("models.sets %q embed", s.Name), s.Embed)
	}
	return errs
}
