// This file holds [[endpoints]]: answer models that run on a server other
// than the local Ollama, named in [models] as "@name". Only an answer
// model may name one; the fast and embedding models always run on the
// local Ollama, because the router needs token probabilities and search
// must not send files anywhere (docs/design/model-router.md).
//
// For now an endpoint must be on this machine (reach = "local"), such as
// Ollama's own OpenAI-style API or another model server on loopback.
// Endpoints off the machine wait for the consent prompt that goes with
// them.

package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aarora79/meru/internal/loopback"
)

// The reaches an endpoint may have: how far its requests may travel.
const (
	ReachLocal    = "local"     // this machine only
	ReachNearEdge = "near-edge" // private addresses: your network, your private cloud network
	ReachRemote   = "remote"    // any address, over HTTPS
)

// The APIs an endpoint may speak.
const (
	APIOllama    = "ollama"    // Ollama's own API: /api/chat
	APIOpenAI    = "openai"    // OpenAI chat completions: /chat/completions
	APIAnthropic = "anthropic" // Anthropic Messages: /messages
)

// apis lists the APIs in the order an error names them.
var apis = []string{APIOllama, APIOpenAI, APIAnthropic}

// minEndpointContext is the smallest context_length an endpoint may give.
// Below 2048 tokens not even the system prompt fits, as with
// [ollama] context_length.
const minEndpointContext = 2048

// Endpoint is one [[endpoints]] entry: a model the answer can come from,
// on a server other than the local Ollama. Code never names a provider;
// the user types the URL.
type Endpoint struct {
	// Name is how [models] and the sets refer to it, as "@name", and what
	// an answer shows as its model. Letters, digits, ".", "-" and "_".
	Name string `toml:"name"`
	// Reach says how far requests may travel; see ReachLocal.
	Reach string `toml:"reach"`
	// API is how to talk to it: "ollama", "openai" or "anthropic".
	API string `toml:"api"`
	// URL is the server's address. For "ollama" it is the server itself,
	// such as http://127.0.0.1:11435; for "openai" and "anthropic" it is
	// the API's base up to and including its version, such as
	// http://127.0.0.1:11434/v1.
	URL string `toml:"url"`
	// APIKey, when set, must be "secret:<name>", an entry in
	// ~/.meru/secrets.toml. A key written into config.toml would sit in a
	// file people paste into chats and issues.
	APIKey string `toml:"api_key"`
	// Model is the name the server knows the model by.
	Model string `toml:"model"`
	// ContextLength is the most tokens the model reads at once. The
	// OpenAI and Anthropic APIs don't report it, so the entry gives it.
	ContextLength int `toml:"context_length"`
	// Vision says the model can look at pictures.
	Vision bool `toml:"vision"`
	// Tools says the model can call tools. It is a pointer so that "left
	// out" (nil), which means true, differs from false.
	Tools *bool `toml:"tools"`
}

// CallsTools reports whether the model can call tools: true unless the
// entry says tools = false.
func (e Endpoint) CallsTools() bool {
	return e.Tools == nil || *e.Tools
}

// EndpointRef reports whether model, a name from [models] or a set, names
// an endpoint, and returns the endpoint's name without its "@".
func EndpointRef(model string) (string, bool) {
	return strings.CutPrefix(model, "@")
}

// FindEndpoint returns the endpoint called name from eps, and whether
// there is one.
func FindEndpoint(eps []Endpoint, name string) (Endpoint, bool) {
	i := slices.IndexFunc(eps, func(e Endpoint) bool { return e.Name == name })
	if i < 0 {
		return Endpoint{}, false
	}
	return eps[i], true
}

// checkEndpoints checks the [[endpoints]] entries, and each "@name" in
// [models] and the sets, and returns one error per problem.
func checkEndpoints(eps []Endpoint, models Models) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	var seen []string
	for i, e := range eps {
		// key names the entry in each message: its name, or its place in
		// the list when it has none.
		key := fmt.Sprintf("endpoints %q", e.Name)
		switch {
		case e.Name == "":
			key = fmt.Sprintf("endpoints: entry %d", i+1)
			add("%s has no name", key)
		case !setName.MatchString(e.Name):
			add("%s: the name may hold only letters, digits, \".\", \"-\" and \"_\"", key)
		case slices.Contains(seen, e.Name):
			add("%s: the name is used twice", key)
		}
		seen = append(seen, e.Name)

		switch e.Reach {
		case ReachLocal:
			if err := loopback.CheckURL(e.URL); err != nil {
				add("%s: reach is \"local\", so the url must be on this machine: %w", key, err)
			}
		case ReachNearEdge, ReachRemote:
			add("%s: reach %q isn't available yet; an endpoint must be on this machine (reach = \"local\") "+
				"until Meru can ask you before a question leaves it", key, e.Reach)
		default:
			add("%s: reach %q is unknown; use \"local\"", key, e.Reach)
		}
		if !slices.Contains(apis, e.API) {
			add("%s: api %q is unknown; use one of %s", key, e.API, strings.Join(apis, ", "))
		}
		if e.APIKey != "" && e.API == APIOllama {
			add("%s: api_key is set, but Ollama's API takes no key; leave it out", key)
		} else if e.APIKey != "" {
			if _, ok := strings.CutPrefix(e.APIKey, "secret:"); !ok {
				add("%s: api_key must name an entry in secrets.toml, as \"secret:<name>\", "+
					"never hold the key itself", key)
			}
		}
		switch {
		case e.Model == "":
			add("%s has no model; give the name the server knows it by", key)
		case e.API == APIOllama && CloudModel(e.Model):
			errs = append(errs, errCloudModel(key+" model", e.Model))
		}
		if e.ContextLength < minEndpointContext {
			add("%s: context_length is %d; give the most tokens the model reads at once, %d or more",
				key, e.ContextLength, minEndpointContext)
		}
	}

	// ref checks one model name from [models] or a set: an "@name" must
	// name an entry above, and only an answer model may name one.
	ref := func(key, model string, answer bool) {
		name, ok := EndpointRef(model)
		if !ok {
			return
		}
		if !answer {
			add("%s: %q names an endpoint, but this model runs on the local Ollama; "+
				"only main may name an endpoint", key, model)
			return
		}
		if _, ok := FindEndpoint(eps, name); !ok {
			add("%s: %q names no [[endpoints]] entry; add one called %q", key, model, name)
		}
	}
	ref("models.main", models.Main, true)
	ref("models.fast", models.Fast, false)
	ref("models.embed", models.Embed, false)
	for _, s := range models.Sets {
		ref(fmt.Sprintf("models.sets %q main", s.Name), s.Main, true)
		ref(fmt.Sprintf("models.sets %q fast", s.Name), s.Fast, false)
		ref(fmt.Sprintf("models.sets %q embed", s.Name), s.Embed, false)
	}
	return errs
}
