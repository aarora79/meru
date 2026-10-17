// This file holds -image, which sends a picture with one question, as the
// desktop app's attach button and meru chat's /attach do. merud copies the
// file into its uploads folder first, under the same rules: an image of up
// to 20 MiB, no link, no secret's name. With an index that reads images,
// merud then finds the pictures in your folders most like it. See
// ARCHITECTURE.md, "Desktop app".

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aarora79/meru/internal/rpc"
)

// attachImage asks merud to copy the image at path into its uploads
// folder and returns the copy's path, the one a question names. A relative
// path is taken from the working folder. It fails when merud can't be
// reached, refuses the file, or reports it isn't an image.
func attachImage(ctx context.Context, socket, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("-image %s: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("-image %s: %w", path, err)
	}
	var saved *rpc.Event
	for ev, err := range rpc.Do(ctx, socket, rpc.Request{Op: rpc.OpAttachFile, Path: abs}, nil) {
		if err != nil {
			return "", err
		}
		switch ev.Type {
		case rpc.EventSaved:
			saved = &ev
		case rpc.EventError:
			return "", fmt.Errorf("-image %s: %s", path, ev.Error)
		}
	}
	switch {
	case saved == nil:
		return "", errors.New("-image: merud didn't copy the image")
	case saved.Kind != rpc.AttachImage:
		return "", fmt.Errorf("-image %s: not a PNG, JPEG, GIF or WebP image", path)
	}
	return saved.Text, nil
}

// withImage puts the image merud saved at saved on req, or leaves req as
// it is when saved is "".
func withImage(req rpc.Request, saved string) rpc.Request {
	if saved != "" {
		req.Images = &rpc.Images{Paths: []string{saved}}
	}
	return req
}
