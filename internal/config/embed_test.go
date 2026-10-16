// This file tests the table of embedding models and their prefixes.

package config

import (
	"strings"
	"testing"
)

func TestFindEmbedModel(t *testing.T) {
	g := FindEmbedModel("embeddinggemma-2:440m")
	if g.Query != "task: search result | query: {query}" || g.Document != "title: {title} | text: {text}" || g.Context != 8192 {
		t.Errorf("embeddinggemma-2:440m = %+v", g)
	}
	// A model Meru knows nothing about gets no prefixes, so its text goes
	// to Ollama as it is.
	if n := FindEmbedModel("nomic-embed-text"); n != (EmbedModel{}) {
		t.Errorf("nomic-embed-text = %+v, want the zero value", n)
	}
	for _, m := range EmbedModels() {
		if !strings.Contains(m.Query, "{query}") || !strings.Contains(m.Document, "{text}") {
			t.Errorf("%s: a prefix lacks its placeholder: %+v", m.Name, m)
		}
	}
}
