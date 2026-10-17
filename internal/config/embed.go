// This file lists the embedding models Meru knows something about beyond
// their name: the task prefixes each was trained with, and the context it
// should load with. A model not listed works too, with plain text and
// Ollama's own context. See ARCHITECTURE.md, "Model tiers".

package config

// EmbedModel is an embedding model Meru knows. The prefixes come from the
// model's card; a wrong or missing prefix makes search worse without an
// error, which is why they sit here, next to the model's name, rather than
// at the places that embed.
type EmbedModel struct {
	// Name is the model as Ollama names it, as [models] embed holds it.
	Name string
	// Query is the prefix for a search query, with {query} where the query
	// goes.
	Query string
	// Document is the prefix for an indexed text, with {title} and {text}.
	Document string
	// Context is the num_ctx to load the model with, its full context:
	// Ollama may load an embedding model with less, and /api/embed cuts a
	// longer input without an error.
	Context int
}

// EmbedModels returns the embedding models Meru knows. It returns a new
// slice each call, so a caller can't change the list for the next one.
func EmbedModels() []EmbedModel {
	return []EmbedModel{
		{
			// EmbeddingGemma 2: text and images in one 768-number space.
			// The prefixes are the model card's "SearchQuery" and
			// "Document" prompts; images take none.
			Name:     "embeddinggemma-2:440m",
			Query:    "task: search result | query: {query}",
			Document: "title: {title} | text: {text}",
			Context:  8192,
		},
	}
}

// FindEmbedModel returns the entry for the model named name, or the zero
// EmbedModel when Meru knows nothing about it: no prefixes, Ollama's own
// context.
func FindEmbedModel(name string) EmbedModel {
	for _, m := range EmbedModels() {
		if m.Name == name {
			return m
		}
	}
	return EmbedModel{}
}
