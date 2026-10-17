// This file tests the image search a turn runs when the question carries
// an image: a model with vision gets the similar images as sources and in
// its prompt; a model without vision answers with the list, where it used
// to refuse; and a turn with no images found answers as before.

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aarora79/meru/internal/retrieve"
	"github.com/aarora79/meru/internal/rpc"
	"github.com/aarora79/meru/internal/store"
	"github.com/aarora79/meru/internal/testutil/fakeollama"
)

// fakeImages is an ImageSearcher that returns fixed results and records
// the query it got.
type fakeImages struct {
	results []retrieve.Result
	err     error
	got     retrieve.ImageQuery
}

func (f *fakeImages) SearchImages(ctx context.Context, q retrieve.ImageQuery, n int) ([]retrieve.Result, error) {
	f.got = q
	return f.results, f.err
}

// twoImages are the results a fake search finds: two pictures in the
// user's folders.
func twoImages() []retrieve.Result {
	img := func(p string) retrieve.Result {
		return retrieve.Result{ChunkWithDoc: store.ChunkWithDoc{Path: p, Kind: retrieve.KindImage}}
	}
	return []retrieve.Result{img("/data/img/beds-may.png"), img("/data/img/beds-june.png")}
}

// sourcesOf returns the sources of the last "sources" event in evs.
func sourcesOf(evs []rpc.Event) []rpc.Citation {
	var out []rpc.Citation
	for _, ev := range evs {
		if ev.Type == rpc.EventSources {
			out = ev.Sources
		}
	}
	return out
}

func TestSimilarImagesWithVision(t *testing.T) {
	it := newImageTurn(t, true)
	search := &fakeImages{results: twoImages()}
	it.agent.UseImageSearch(search)
	it.srv.Enqueue(it.cfg.Models.Main, fakeollama.Reply{Text: "The first one, [1], has the same beds."})

	evs, err := run(context.Background(), it.agent, rpc.Request{
		Text: "find images like this", Images: &rpc.Images{Paths: []string{it.path}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if string(search.got.Image) != string(it.png) || search.got.Text != "" {
		t.Errorf("the search got %d bytes and text %q; want the attached image", len(search.got.Image), search.got.Text)
	}
	src := sourcesOf(evs)
	if len(src) != 2 || src[0].N != 1 || src[0].Kind != "image" || src[0].Path != "/data/img/beds-may.png" {
		t.Errorf("sources = %+v; want the two images, numbered from 1", src)
	}
	// The similar images reach the prompt the model sees.
	chats := it.srv.Requests("/api/chat")
	if len(chats) != 1 {
		t.Fatalf("got %d chat requests, want 1", len(chats))
	}
	var prompt strings.Builder
	for _, m := range chatMessages(t, chats[0]) {
		prompt.WriteString(m.Content)
	}
	if !strings.Contains(prompt.String(), "[1] /data/img/beds-may.png (image)") {
		t.Errorf("the prompt lacks the similar images:\n%s", prompt.String())
	}
}

func TestSimilarImagesWithoutVision(t *testing.T) {
	it := newImageTurn(t, false)
	it.agent.UseImageSearch(&fakeImages{results: twoImages()})

	evs, err := run(context.Background(), it.agent, rpc.Request{
		Text: "find images like this", Images: &rpc.Images{Paths: []string{it.path}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(it.srv.Requests("/api/chat")); n != 0 {
		t.Errorf("%d chat requests; a model without vision gets none", n)
	}
	answer := answerOf(evs)
	for _, want := range []string{"most like the one you attached", "[1] /data/img/beds-may.png", "[2] /data/img/beds-june.png", "can't look at pictures"} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer lacks %q:\n%s", want, answer)
		}
	}
	if len(sourcesOf(evs)) != 2 {
		t.Errorf("sources = %+v, want the two images", sourcesOf(evs))
	}
	// The answer's [n] marks cite both sources, so the clients show them.
	if cited := rpc.Cited(answer, sourcesOf(evs)); len(cited) != 2 {
		t.Errorf("cited = %+v, want both images", cited)
	}
}

func TestSimilarImagesNoneOrFailed(t *testing.T) {
	for _, search := range []*fakeImages{{}, {err: errors.New("ollama down")}} {
		it := newImageTurn(t, false)
		it.agent.UseImageSearch(search)
		evs, err := run(context.Background(), it.agent, rpc.Request{
			Text: "find images like this", Images: &rpc.Images{Paths: []string{it.path}},
		})
		if err != nil {
			t.Fatalf("Handle: %v", err)
		}
		// With nothing found the turn answers as it did before image
		// search: the model can't look at the picture.
		if a := answerOf(evs); !strings.Contains(a, noVisionAnswer(it.cfg.Models.Main)) || len(sourcesOf(evs)) != 0 {
			t.Errorf("answer = %q, sources %+v; want the no-vision answer and no sources", a, sourcesOf(evs))
		}
	}
}
