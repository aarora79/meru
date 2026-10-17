// This file holds what a turn does with the images a question carries:
// read them from the uploads folder, check that the main model can look
// at images, and answer plainly when it can't. prompt puts the images on
// the question's message (see finishPrompt). ARCHITECTURE.md, "Agent
// loop", says why an image turn skips the router.

package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/engine"
	"github.com/aarora79/meru/internal/rpc"
)

// endNoVision is the outcome of a turn whose question carried images when
// the main model can't look at images. It sits beside the other ways a
// turn ends without a full answer; see endOf.
const endNoVision = "no_vision"

// errNoVision stops a turn whose question carried images when the main
// model lacks vision. Handle turns it into noVisionAnswer and the outcome
// endNoVision, not a failed turn.
var errNoVision = errors.New("the main model can't look at images")

// noVisionAnswer is the answer to a turn that stopped with errNoVision.
// It names the model from config and ends with visionSuggestion, which
// names a set of the user's own whose model can look at pictures.
func noVisionAnswer(model string, sets []VisionSet) string {
	return model + " can't look at images, so Meru didn't send it this question. " + visionSuggestion(sets)
}

// maxViewed caps the pictures one turn shows the model through view_image.
// Each costs 500 to 1,500 prompt tokens (675 for Gemma 4 and 1,575 for
// Qwen 3.6 on one ride receipt), and the prompt holds 32,768, so eight
// leave room for the question, the excerpts and the answer. A question
// about one trip's tickets rarely needs more.
const maxViewed = 8

// VisionSet is a model set whose answer model can look at pictures and
// call tools, which a model without vision suggests the user switch to.
type VisionSet struct {
	Set   string // the set's name, as /model takes it
	Model string // its answer model, as Ollama names it
}

// UseVisionSets tells the agent how to find the sets it may suggest. merud
// passes a function over config's [[models.sets]] and the engine's cached
// capabilities, so model names stay in config. Call it once, before the
// first Handle. Without it, the suggestion names no model.
func (a *Agent) UseVisionSets(sets func(ctx context.Context) []VisionSet) {
	a.visionSets = sets
}

// suggestSets returns the sets UseVisionSets finds, or nil without it.
func (a *Agent) suggestSets(ctx context.Context) []VisionSet {
	if a.visionSets == nil {
		return nil
	}
	return a.visionSets(ctx)
}

// visionSuggestion is the one sentence that tells the user how to get a
// model that can look at pictures. noVisionText, noVisionAnswer and
// similarAnswer all end with it, so the words live in one place. With a
// set to suggest, it names the first one's model and both ways to switch;
// with none, it points to the lists that show what each model can do.
func visionSuggestion(sets []VisionSet) string {
	if len(sets) == 0 {
		return "To have Meru read pictures, pick a model that can see them with /model in meru chat, " +
			"or in Settings, Models."
	}
	v := sets[0]
	return v.Model + " can: switch to it with `/model " + v.Set + "` in meru chat, " +
		"or Use for answers in Settings, Models."
}

// noVisionText is the notice under an answer that names pictures the main
// model can't look at; see namesImages.
func noVisionText(model string, sets []VisionSet) string {
	return model + " reads text only, so it can't look at these pictures. " + visionSuggestion(sets)
}

// imagePath matches a path or file name that ends in an image ending, as an
// answer writes one: "~/trips/amtrak.png" or "ticket.JPG". The (?i) makes
// it ignore case; \b keeps "x.pngs" from matching.
var imagePath = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp)\b`)

// namesImages reports whether an answer refers to a picture: one of its
// cited sources is an image, or its text names a file with an image
// ending. The second catches pictures the model found through a folder
// listing or a command's output, which it names without citing.
func namesImages(text string, cited []rpc.Citation) bool {
	for _, c := range cited {
		if c.Kind == "image" {
			return true
		}
	}
	return imagePath.MatchString(text)
}

// withoutViewing drops view_image from specs when the main model can't
// look at pictures, and notes the model in t.blind for the notice. A model
// without vision never sees the tool, so it can't call it and fail; the
// notice tells the user instead. A failed check counts as no vision: the
// tool is worth nothing to a model that might not see. Without UseImages
// there is no check at all, so the tool goes and no notice follows.
func (a *Agent) withoutViewing(ctx context.Context, t *turn, specs []engine.ToolSpec) []engine.ToolSpec {
	model := a.Main()
	if a.vision != nil {
		ok, err := a.vision(ctx, model)
		if err != nil {
			a.log.DebugContext(ctx, "couldn't check whether the answer model sees pictures", "model", model, "err", err)
		}
		if err == nil && ok {
			return specs
		}
		t.blind = model
	}
	// slices.Clone copies specs first, since DeleteFunc works in place and
	// the caller's slice may be shared.
	return slices.DeleteFunc(slices.Clone(specs), func(s engine.ToolSpec) bool {
		return s.Name == builtin.ViewImage
	})
}

// UseImages lets later turns take images. read returns the bytes of the
// image at a path a question names, and fails for any path that isn't an
// image the user attached; merud passes the built-in tools' Image. vision
// reports whether a model can look at images; merud passes a check of
// OllamaEngine.Capabilities. Call it once, before the first Handle.
// Without it, a question with images fails.
func (a *Agent) UseImages(read func(path string) ([]byte, error), vision func(ctx context.Context, model string) (bool, error)) {
	a.readImage = read
	a.vision = vision
}

// loadImages reads the images req carries and returns their paths, as
// the transcript keeps them, and their bytes, as the model gets them. A
// request with none returns nil for both.
//
// It fails when req names more than rpc.MaxImages images, when UseImages
// was never called, when read refuses a path (one outside the uploads
// folder, a symbolic link, a missing file, or one that isn't an image), or
// when UseImages gave no vision check.
func (a *Agent) loadImages(req rpc.Request) ([]string, [][]byte, error) {
	if req.Images == nil || len(req.Images.Paths) == 0 {
		return nil, nil, nil
	}
	paths := req.Images.Paths
	if len(paths) > rpc.MaxImages {
		return nil, nil, fmt.Errorf("a question takes %d images at most, and this one has %d", rpc.MaxImages, len(paths))
	}
	if a.readImage == nil {
		return nil, nil, errors.New("this merud can't take images")
	}
	images := make([][]byte, 0, len(paths))
	for _, p := range paths {
		b, err := a.readImage(p)
		if err != nil {
			return nil, nil, err
		}
		images = append(images, b)
	}
	// Without a way to ask what a model can do, merud can't tell whether
	// the model would see the images or ignore them, so it refuses.
	if a.vision == nil {
		return nil, nil, errors.New("this merud can't tell whether its model can look at images")
	}
	return slices.Clone(paths), images, nil
}

// checkVision asks whether the main model can look at images. When it
// can't, checkVision sends the "route" event the answer needs, the route
// "direct" with nothing guessed, and returns errNoVision; Handle then
// sends noVisionAnswer as the answer, and the turn calls no model. It
// fails when the check itself fails, as when Ollama is down or the model
// isn't pulled, or when emit fails.
func (a *Agent) checkVision(ctx context.Context, t *turn) error {
	model := a.Main()
	ok, err := a.vision(ctx, model)
	if err != nil {
		return fmt.Errorf("check whether %s can look at images: %w", model, err)
	}
	a.log.DebugContext(ctx, "vision checked", "model", model, "vision", ok, "images", len(t.images))
	if ok {
		return nil
	}
	if err := t.emit(rpc.Event{Type: rpc.EventRoute, Route: "direct", Confidence: 1}); err != nil {
		return err
	}
	return errNoVision
}

// withImages puts images on the last message of msgs, the question, and
// returns msgs. Only this turn's question carries them: a later turn's
// history holds a text note for each one instead (see transcript's
// History), because each image costs the model hundreds of tokens.
func withImages(msgs []engine.Message, images [][]byte) []engine.Message {
	if len(images) > 0 && len(msgs) > 0 {
		msgs[len(msgs)-1].Images = images
	}
	return msgs
}

// imageBytes returns the size of images added up, for the debug line.
func imageBytes(images [][]byte) int {
	n := 0
	for _, b := range images {
		n += len(b)
	}
	return n
}
