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

// TestEmbedPrefixFingerprint checks that either prompt changes the hash and
// unrelated model settings do not.
func TestEmbedPrefixFingerprint(t *testing.T) {
	model := EmbedModel{Query: "query {query}", Document: "title: {title} | text: {text}", Context: 8192, Images: true}
	samePrefixes := EmbedModel{Query: model.Query, Document: model.Document}
	if got, want := samePrefixes.PrefixFingerprint(), model.PrefixFingerprint(); got != want {
		t.Errorf("PrefixFingerprint = %q, want stable %q", got, want)
	}

	for _, changed := range []EmbedModel{
		{Query: "new query {query}", Document: model.Document},
		{Query: model.Query, Document: "new document {text}"},
	} {
		if changed.PrefixFingerprint() == model.PrefixFingerprint() {
			t.Errorf("PrefixFingerprint did not change for %+v", changed)
		}
	}

	metadataOnly := model
	metadataOnly.Context++
	metadataOnly.Images = false
	if metadataOnly.PrefixFingerprint() != model.PrefixFingerprint() {
		t.Error("PrefixFingerprint changed when only non-prefix model settings changed")
	}
}
