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
	"slices"

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
// It names the model from config and says how to check another one, and
// names none itself: which models have vision changes with each release.
func noVisionAnswer(model string) string {
	return model + " can't look at images, so Meru didn't send it this question. " +
		"Pick a model with vision for [models] main in config.toml. " +
		"To check a model, run `ollama show <model>` and look for \"vision\" under Capabilities."
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
	ok, err := a.vision(ctx, a.models.Main)
	if err != nil {
		return fmt.Errorf("check whether %s can look at images: %w", a.models.Main, err)
	}
	a.log.DebugContext(ctx, "vision checked", "model", a.models.Main, "vision", ok, "images", len(t.images))
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
